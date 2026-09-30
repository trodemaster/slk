package api

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// DownloadOptions bounds the response and optionally verifies file metadata.
type DownloadOptions struct {
	MaxBytes     int64
	ExpectedSize int64
	ExpectedMIME string
}

type DownloadResult struct {
	Size     int64
	Mimetype string
}

// ValidateDownloadURL allows bearer authentication only for Slack's file host.
// An exact loopback BaseURL origin is also supported for local HTTP tests.
func (c *Client) ValidateDownloadURL(raw string) error {
	_, err := c.downloadURL(raw)
	return err
}

func (c *Client) downloadURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("file download: invalid URL or credentials in URL")
	}
	if u.Scheme == "https" && strings.EqualFold(u.Hostname(), "files.slack.com") && (u.Port() == "" || u.Port() == "443") {
		return u, nil
	}
	base, err := url.Parse(c.BaseURL)
	if err == nil && base.User == nil && downloadOrigin(base) == downloadOrigin(u) && (u.Scheme == "http" || u.Scheme == "https") {
		ip := net.ParseIP(base.Hostname())
		if ip != nil && ip.IsLoopback() {
			return u, nil
		}
	}
	return nil, errors.New("file download: URL must use HTTPS on the official files.slack.com host")
}

func downloadOrigin(u *url.URL) string {
	port := u.Port()
	if port == "" {
		switch u.Scheme {
		case "https":
			port = "443"
		case "http":
			port = "80"
		}
	}
	return strings.ToLower(u.Scheme) + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port)
}

// Download streams a bounded response without exposing the token or URL in errors.
// The caller owns the destination and must remove any partial output on failure.
func (c *Client) Download(ctx context.Context, rawURL string, dst io.Writer, opts DownloadOptions) (DownloadResult, error) {
	var result DownloadResult
	if opts.MaxBytes <= 0 || opts.ExpectedSize < 0 {
		return result, errors.New("file download: max-bytes must be positive and expected size cannot be negative")
	}
	if opts.ExpectedSize > opts.MaxBytes {
		return result, errors.New("file download: metadata size exceeds max-bytes")
	}
	u, err := c.downloadURL(rawURL)
	if err != nil {
		return result, err
	}
	retries := c.MaxRetries
	if retries < 0 {
		retries = 0
	}
	if retries > 3 {
		retries = 3
	}
	for attempt := 0; ; attempt++ {
		hc := http.Client{Timeout: 30 * time.Second}
		if c.HTTP != nil {
			hc = *c.HTTP
		}
		originalRedirect := hc.CheckRedirect
		crossedOrigin := false
		var redirectErr error
		hc.CheckRedirect = func(req *http.Request, via []*http.Request) error {
			if len(via) >= 5 {
				redirectErr = errors.New("file download: too many redirects")
				return redirectErr
			}
			prev := via[len(via)-1].URL
			if req.URL.User != nil || req.URL.Scheme != "https" && !(prev.Scheme == "http" && downloadOrigin(req.URL) == downloadOrigin(u)) {
				redirectErr = errors.New("file download: unsafe redirect or HTTPS downgrade")
				return redirectErr
			}
			if downloadOrigin(req.URL) != downloadOrigin(prev) {
				crossedOrigin = true
			}
			if originalRedirect != nil {
				if err := originalRedirect(req, via); err != nil {
					redirectErr = errors.New("file download: redirect rejected by HTTP client")
					return redirectErr
				}
			}
			// net/http can re-copy initial headers when a redirect returns to
			// the original host, so removal must persist for the entire chain.
			if crossedOrigin {
				req.Header.Del("Authorization")
				req.Header.Del("Proxy-Authorization")
				req.Header.Del("Cookie")
				req.Header.Del("Referer")
			}
			return nil
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return result, errors.New("file download: cannot construct HTTP request")
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		resp, err := hc.Do(req)
		if err != nil {
			if redirectErr != nil {
				return result, redirectErr
			}
			if ctx.Err() != nil {
				return result, fmt.Errorf("file download: %w", ctx.Err())
			}
			return result, errors.New("file download: HTTP request failed (network, TLS, or timeout)")
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt < retries {
			resp.Body.Close()
			delay := parseRetryAfter(resp.Header.Get("Retry-After"))
			if delay > 5*time.Second {
				return result, &APIError{Method: "file download", SlackError: "ratelimited"}
			}
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return result, fmt.Errorf("file download: %w", ctx.Err())
			case <-timer.C:
				continue
			}
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			switch resp.StatusCode {
			case http.StatusUnauthorized, http.StatusForbidden:
				return result, &APIError{Method: "file download", SlackError: "invalid_auth"}
			case http.StatusNotFound:
				return result, &APIError{Method: "file download", SlackError: "file_not_found"}
			case http.StatusTooManyRequests:
				return result, &APIError{Method: "file download", SlackError: "ratelimited"}
			default:
				return result, fmt.Errorf("file download: HTTP status %d", resp.StatusCode)
			}
		}
		result, err = streamDownload(resp, dst, opts)
		resp.Body.Close()
		return result, err
	}
}

func streamDownload(resp *http.Response, dst io.Writer, opts DownloadOptions) (DownloadResult, error) {
	var result DownloadResult
	if resp.ContentLength > opts.MaxBytes {
		return result, errors.New("file download: response exceeds max-bytes")
	}
	if opts.ExpectedSize > 0 && resp.ContentLength >= 0 && resp.ContentLength != opts.ExpectedSize {
		return result, errors.New("file download: response size does not match metadata")
	}
	limited := &io.LimitedReader{R: resp.Body, N: opts.MaxBytes}
	reader := bufio.NewReader(limited)
	prefix, err := reader.Peek(512)
	if err != nil && !errors.Is(err, io.EOF) {
		return result, errors.New("file download: interrupted response body")
	}
	if len(prefix) == 0 {
		return result, errors.New("file download: empty response body")
	}
	detected := downloadMIME(prefix)
	expected := canonicalDownloadMIME(opts.ExpectedMIME)
	declared := canonicalDownloadMIME(resp.Header.Get("Content-Type"))
	if opts.ExpectedMIME != "" && expected == "" {
		return result, errors.New("file download: invalid metadata MIME")
	}
	if strings.HasPrefix(expected, "image/") || strings.HasPrefix(declared, "image/") {
		if !strings.HasPrefix(detected, "image/") {
			return result, errors.New("file download: expected image bytes, received non-image content (possibly a login or error page)")
		}
		if expected != "" && expected != "image/*" && expected != detected {
			return result, errors.New("file download: image MIME does not match metadata")
		}
		if declared != "" && declared != "application/octet-stream" && declared != "binary/octet-stream" && declared != detected {
			return result, errors.New("file download: image MIME does not match HTTP content type")
		}
	}
	result.Mimetype = detected
	if expected != "" && expected != "image/*" && expected != "application/octet-stream" && expected != "binary/octet-stream" {
		result.Mimetype = expected
	} else if declared != "" && declared != "application/octet-stream" && declared != "binary/octet-stream" {
		result.Mimetype = declared
	}
	result.Size, err = io.Copy(dst, reader)
	if err != nil {
		return DownloadResult{}, errors.New("file download: interrupted body or destination write failed")
	}
	var extra [1]byte
	n, err := io.ReadFull(resp.Body, extra[:])
	if n != 0 {
		return DownloadResult{}, errors.New("file download: response exceeds max-bytes")
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return DownloadResult{}, errors.New("file download: interrupted response body")
	}
	if resp.ContentLength >= 0 && result.Size != resp.ContentLength || opts.ExpectedSize > 0 && result.Size != opts.ExpectedSize {
		return DownloadResult{}, errors.New("file download: truncated response or metadata size mismatch")
	}
	return result, nil
}

func canonicalDownloadMIME(value string) string {
	value, _, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	switch strings.ToLower(value) {
	case "image/jpg":
		return "image/jpeg"
	case "image/x-png":
		return "image/png"
	case "image/x-icon", "image/vnd.microsoft.icon":
		return "image/x-icon"
	default:
		return strings.ToLower(value)
	}
}

func downloadMIME(prefix []byte) string {
	detected := canonicalDownloadMIME(http.DetectContentType(prefix))
	if strings.HasPrefix(detected, "image/") {
		return detected
	}
	if len(prefix) >= 4 && (string(prefix[:4]) == "II*\x00" || string(prefix[:4]) == "MM\x00*") {
		return "image/tiff"
	}
	if len(prefix) >= 16 && string(prefix[4:8]) == "ftyp" {
		size := int(binary.BigEndian.Uint32(prefix[:4]))
		if size > len(prefix) {
			size = len(prefix)
		}
		for i := 8; i+4 <= size; i += 4 {
			if i == 12 {
				continue
			}
			switch string(prefix[i : i+4]) {
			case "avif", "avis":
				return "image/avif"
			case "heic", "heix", "hevc", "hevx":
				return "image/heic"
			case "mif1", "msf1":
				return "image/heif"
			}
		}
	}
	decoder := xml.NewDecoder(strings.NewReader(string(prefix)))
	for {
		token, err := decoder.Token()
		if err != nil {
			break
		}
		if elem, ok := token.(xml.StartElement); ok {
			if elem.Name.Local == "svg" && (elem.Name.Space == "" || elem.Name.Space == "http://www.w3.org/2000/svg") {
				return "image/svg+xml"
			}
			break
		}
	}
	return detected
}
