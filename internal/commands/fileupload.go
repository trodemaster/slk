package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/howar31/slk/internal/api"
)

type fileUploadOptions struct {
	Paths                                      []string
	Channel, ThreadTS, Title, Comment, AltText string
}

type uploadSource struct {
	file  *os.File
	name  string
	size  int64
	title string
}

type uploadCompletionFile struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

var uploadFileIDPattern = regexp.MustCompile(`^F[A-Z0-9]+$`)
var uploadDMIDPattern = regexp.MustCompile(`^D[A-Z0-9]+$`)
var uploadSlackErrorPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

func validateFileUploadOptions(opts fileUploadOptions) error {
	if len(opts.Paths) == 0 {
		return fmt.Errorf("at least one --file is required")
	}
	if opts.Title != "" && len(opts.Paths) > 1 {
		return fmt.Errorf("--title is supported only with a single file")
	}
	if opts.Channel == "" && (opts.Comment != "" || opts.ThreadTS != "") {
		return fmt.Errorf("an initial comment or thread requires a destination channel")
	}
	return nil
}

func preflightUploadFiles(opts fileUploadOptions) ([]uploadSource, error) {
	if err := validateFileUploadOptions(opts); err != nil {
		return nil, err
	}
	sources := make([]uploadSource, 0, len(opts.Paths))
	fail := func(err error) ([]uploadSource, error) {
		closeUploadSources(sources)
		return nil, err
	}
	for _, path := range opts.Paths {
		stat, err := os.Stat(path)
		if err != nil {
			return fail(fmt.Errorf("checking upload file %q: %w", path, err))
		}
		if !stat.Mode().IsRegular() {
			return fail(fmt.Errorf("upload file %q must be a regular file", path))
		}
		f, err := os.Open(path)
		if err != nil {
			return fail(fmt.Errorf("opening upload file %q: %w", path, err))
		}
		sources = append(sources, uploadSource{file: f})
		stat, err = f.Stat()
		if err != nil {
			return fail(fmt.Errorf("checking opened upload file %q: %w", path, err))
		}
		if !stat.Mode().IsRegular() || stat.Size() == 0 {
			return fail(fmt.Errorf("upload file %q must be a nonempty regular file", path))
		}
		// Check readability before any ticket is allocated, keeping descriptors
		// open so later path replacements cannot change the uploaded sources.
		var probe [1]byte
		if _, err := f.Read(probe[:]); err != nil {
			return fail(fmt.Errorf("reading upload file %q: %w", path, err))
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return fail(fmt.Errorf("rewinding upload file %q: %w", path, err))
		}
		name := filepath.Base(path)
		title := opts.Title
		if title == "" {
			title = name
		}
		sources[len(sources)-1] = uploadSource{file: f, name: name, size: stat.Size(), title: title}
	}
	return sources, nil
}

func closeUploadSources(sources []uploadSource) {
	for _, source := range sources {
		source.file.Close()
	}
}

func uploadTicketParams(source uploadSource, altText string) map[string]string {
	params := map[string]string{"filename": source.name, "length": fmt.Sprint(source.size)}
	if altText != "" {
		params["alt_txt"] = altText
	}
	return params
}

func uploadCompletionParams(opts fileUploadOptions, files []uploadCompletionFile) map[string]string {
	raw, _ := json.Marshal(files)
	params := map[string]string{"files": string(raw)}
	if opts.Channel != "" {
		params["channel_id"] = opts.Channel
	}
	if opts.ThreadTS != "" {
		params["thread_ts"] = opts.ThreadTS
	}
	if opts.Comment != "" {
		params["initial_comment"] = opts.Comment
	}
	return params
}

// uploadFiles performs all local validation before allocating tickets, streams
// bytes without bearer authentication, then completes the batch exactly once.
// On failure, IDs contains only validated ticket IDs already allocated.
func uploadFiles(client *api.Client, opts fileUploadOptions) ([]byte, []string, error) {
	sources, err := preflightUploadFiles(opts)
	if err != nil {
		return nil, nil, err
	}
	defer closeUploadSources(sources)
	if client == nil || client.HTTP == nil {
		return nil, nil, fmt.Errorf("file upload requires an HTTP client")
	}
	if strings.HasPrefix(opts.Channel, "U") || strings.HasPrefix(opts.Channel, "W") {
		raw, err := uploadAPICall(client, "conversations.open", map[string]string{"users": opts.Channel})
		if err != nil {
			return nil, nil, err
		}
		var opened struct {
			Channel struct {
				ID string `json:"id"`
			} `json:"channel"`
		}
		if json.Unmarshal(raw, &opened) != nil || !uploadDMIDPattern.MatchString(opened.Channel.ID) {
			return nil, nil, fmt.Errorf("conversations.open: response missing a valid DM channel ID")
		}
		opts.Channel = opened.Channel.ID
	}

	ids := make([]string, 0, len(sources))
	fail := func(err error) ([]byte, []string, error) {
		if len(ids) > 0 {
			err = fmt.Errorf("%w; allocated file IDs: %s (completion is not retried automatically)", err, strings.Join(ids, ", "))
		}
		return nil, ids, err
	}
	files := make([]uploadCompletionFile, 0, len(sources))
	for _, source := range sources {
		raw, err := uploadAPICall(client, "files.getUploadURLExternal", uploadTicketParams(source, opts.AltText))
		if err != nil {
			return fail(err)
		}
		var ticket struct {
			URL string `json:"upload_url"`
			ID  string `json:"file_id"`
		}
		if json.Unmarshal(raw, &ticket) != nil || !uploadFileIDPattern.MatchString(ticket.ID) {
			return fail(fmt.Errorf("files.getUploadURLExternal: response missing a valid file ID"))
		}
		for _, id := range ids {
			if id == ticket.ID {
				return fail(fmt.Errorf("files.getUploadURLExternal: duplicate file ID"))
			}
		}
		ids = append(ids, ticket.ID)
		if err := validateUploadURL(ticket.URL, client.BaseURL); err != nil {
			return fail(err)
		}
		if err := uploadFileBytes(client, ticket.URL, source); err != nil {
			return fail(err)
		}
		files = append(files, uploadCompletionFile{ID: ticket.ID, Title: source.title})
	}
	raw, err := uploadAPICall(client, "files.completeUploadExternal", uploadCompletionParams(opts, files))
	if err != nil {
		return fail(err)
	}
	var completed struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if json.Unmarshal(raw, &completed) != nil {
		return fail(fmt.Errorf("files.completeUploadExternal: invalid file list in response; completion may have succeeded"))
	}
	for _, id := range ids {
		found := false
		for _, file := range completed.Files {
			if file.ID == id {
				found = true
				break
			}
		}
		if !found {
			return fail(fmt.Errorf("files.completeUploadExternal: response did not confirm file %s; completion may have succeeded", id))
		}
	}
	return raw, ids, nil
}

func validateUploadURL(raw, baseURL string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return fmt.Errorf("files.getUploadURLExternal: invalid upload URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	base, err := url.Parse(baseURL)
	if u.Scheme == "http" && err == nil && base.Scheme == "http" &&
		isUploadLoopback(base.Hostname()) && isUploadLoopback(u.Hostname()) {
		return nil
	}
	return fmt.Errorf("files.getUploadURLExternal: upload URL must use HTTPS")
}

func isUploadLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func uploadFileBytes(client *api.Client, uploadURL string, source uploadSource) error {
	req, err := http.NewRequest(http.MethodPost, uploadURL, io.LimitReader(source.file, source.size))
	if err != nil {
		return fmt.Errorf("uploading file bytes: invalid request")
	}
	req.ContentLength = source.size
	req.Header.Set("Content-Type", "application/octet-stream")
	httpClient := *client.HTTP
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := httpClient.Do(req)
	if err != nil {
		// net/http errors include the signed upload URL; never propagate them.
		return fmt.Errorf("uploading file bytes: HTTP request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("uploading file bytes: HTTP status %d", resp.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return fmt.Errorf("uploading file bytes: reading response failed")
	}
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) || strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		var envelope struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(raw, &envelope) != nil || !envelope.OK {
			return fmt.Errorf("uploading file bytes: invalid or unsuccessful JSON response")
		}
	}
	return nil
}

type uploadStatusError struct {
	status int
}

func (e *uploadStatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.status) }

type uploadAPITransport struct {
	base http.RoundTripper
}

func (t uploadAPITransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if (resp.StatusCode < 200 || resp.StatusCode >= 300) && resp.StatusCode != http.StatusTooManyRequests {
		resp.Body.Close()
		return nil, &uploadStatusError{status: resp.StatusCode}
	}
	return resp, nil
}

func uploadAPICall(client *api.Client, method string, params map[string]string) ([]byte, error) {
	c := *client
	httpClient := *client.HTTP
	transport := httpClient.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	httpClient.Transport = uploadAPITransport{base: transport}
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	c.HTTP = &httpClient
	// Completion is non-idempotent, so no upload API call is retried implicitly.
	c.MaxRetries = 0
	raw, err := c.Call(method, params, nil)
	if err == nil {
		return raw, nil
	}
	var apiErr *api.APIError
	if errors.As(err, &apiErr) && uploadSlackErrorPattern.MatchString(apiErr.SlackError) {
		return nil, apiErr
	}
	var statusErr *uploadStatusError
	if errors.As(err, &statusErr) {
		return nil, fmt.Errorf("%s: HTTP status %d", method, statusErr.status)
	}
	return nil, fmt.Errorf("%s: request failed or invalid JSON response", method)
}

func uploadDryRun(w io.Writer, opts fileUploadOptions) error {
	sources, err := preflightUploadFiles(opts)
	if err != nil {
		return err
	}
	defer closeUploadSources(sources)
	if strings.HasPrefix(opts.Channel, "U") || strings.HasPrefix(opts.Channel, "W") {
		if _, err := fmt.Fprintf(w, "[dry-run] conversations.open users=%s -> <dm-channel-id>\n", opts.Channel); err != nil {
			return err
		}
		opts.Channel = "<dm-channel-id>"
	}
	files := make([]uploadCompletionFile, 0, len(sources))
	for i, source := range sources {
		if _, err := fmt.Fprintf(w, "[dry-run] files.getUploadURLExternal %v -> <upload-url-%d>, <file-id-%d>\n",
			uploadTicketParams(source, opts.AltText), i+1, i+1); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "[dry-run] POST <upload-url-%d> file=%q Content-Type=application/octet-stream Content-Length=%d (no bearer token)\n",
			i+1, opts.Paths[i], source.size); err != nil {
			return err
		}
		files = append(files, uploadCompletionFile{ID: fmt.Sprintf("<file-id-%d>", i+1), Title: source.title})
	}
	_, err = fmt.Fprintf(w, "[dry-run] files.completeUploadExternal %v\n", uploadCompletionParams(opts, files))
	return err
}
