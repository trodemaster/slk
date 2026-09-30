package commands

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/howar31/slk/internal/api"
)

func fileUploadTestDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp(".", ".fileupload-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func fileUploadTestSource(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(fileUploadTestDir(t), name)
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileUploadTestClient(srv *httptest.Server) *api.Client {
	c := api.New("xoxp-test")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	return c
}

func TestUploadFiles_CompleteBatch(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			paths := []string{fileUploadTestSource(t, "alice,diagram.png", "Alice's bytes")}
			bodies := []string{"Alice's bytes"}
			if count == 2 {
				paths = append(paths, fileUploadTestSource(t, "bob.png", "Bob's longer bytes"))
				bodies = append(bodies, "Bob's longer bytes")
			}
			opts := fileUploadOptions{
				Paths: paths, Channel: "C0123456789", ThreadTS: "1234567890.000001",
				Comment: "Pictures\nfor Alice and Bob", AltText: "A diagram",
			}
			if count == 1 {
				opts.Title = "Shared diagram"
			}
			var sequence []string
			var ticketCalls, byteCalls, completionCalls int
			ids := []string{"F01234567", "F01234568"}[:count]
			final := fmt.Sprintf(`{"ok":true,"files":[{"id":%q}]}`, ids[0])
			if count == 2 {
				final = fmt.Sprintf(`{"ok":true,"files":[{"id":%q},{"id":%q}]}`, ids[0], ids[1])
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sequence = append(sequence, r.URL.Path)
				if r.Method != http.MethodPost {
					t.Errorf("method: got %s, want POST", r.Method)
				}
				switch r.URL.Path {
				case "/files.getUploadURLExternal":
					_ = r.ParseForm()
					i := ticketCalls
					ticketCalls++
					if r.Header.Get("Authorization") != "Bearer xoxp-test" {
						t.Error("ticket call missing API authentication")
					}
					if got := r.Form.Get("filename"); got != filepath.Base(paths[i]) {
						t.Errorf("filename: got %q", got)
					}
					if got := r.Form.Get("length"); got != fmt.Sprint(len(bodies[i])) {
						t.Errorf("ticket length: got %q", got)
					}
					if r.Form.Get("alt_txt") != opts.AltText {
						t.Error("ticket missing alt_txt")
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"ok": true, "file_id": ids[i], "upload_url": "http://" + r.Host + fmt.Sprintf("/upload/%d?signed=private", i),
					})
				case "/upload/0", "/upload/1":
					i := byteCalls
					byteCalls++
					if r.Header.Get("Authorization") != "" {
						t.Error("byte upload included bearer token")
					}
					if r.Header.Get("Content-Type") != "application/octet-stream" {
						t.Errorf("byte upload Content-Type: %s", r.Header.Get("Content-Type"))
					}
					if r.ContentLength != int64(len(bodies[i])) || len(r.TransferEncoding) != 0 {
						t.Errorf("byte upload length: %d; transfer encoding: %v", r.ContentLength, r.TransferEncoding)
					}
					got, _ := io.ReadAll(r.Body)
					if string(got) != bodies[i] {
						t.Errorf("bytes: got %q, want %q", got, bodies[i])
					}
					fmt.Fprint(w, "OK")
				case "/files.completeUploadExternal":
					completionCalls++
					_ = r.ParseForm()
					if byteCalls != count {
						t.Error("completion preceded byte uploads")
					}
					var files []uploadCompletionFile
					if err := json.Unmarshal([]byte(r.Form.Get("files")), &files); err != nil {
						t.Error(err)
					}
					if len(files) != count {
						t.Fatalf("completion files: %v", files)
					}
					for i, file := range files {
						wantTitle := filepath.Base(paths[i])
						if count == 1 {
							wantTitle = opts.Title
						}
						if file.ID != ids[i] || file.Title != wantTitle {
							t.Errorf("completion file: got %+v, want %s/%s", file, ids[i], wantTitle)
						}
					}
					if r.Form.Get("channel_id") != opts.Channel || r.Form.Get("thread_ts") != opts.ThreadTS ||
						r.Form.Get("initial_comment") != opts.Comment {
						t.Errorf("completion parameters: %v", r.Form)
					}
					fmt.Fprint(w, final)
				default:
					t.Errorf("unexpected call: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer srv.Close()
			raw, gotIDs, err := uploadFiles(fileUploadTestClient(srv), opts)
			if err != nil {
				t.Fatal(err)
			}
			if string(raw) != final || !reflect.DeepEqual(gotIDs, ids) {
				t.Errorf("got raw=%s, IDs=%v", raw, gotIDs)
			}
			if ticketCalls != count || byteCalls != count || completionCalls != 1 {
				t.Errorf("calls: ticket=%d, bytes=%d, completion=%d; sequence=%v", ticketCalls, byteCalls, completionCalls, sequence)
			}
		})
	}
}

func TestUploadFiles_Destinations(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "image bytes")
	for _, destination := range []string{"", "C0123456789", "D0123456789", "U0123456789", "W0123456789"} {
		t.Run(destination, func(t *testing.T) {
			var opened, completed bool
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				switch r.URL.Path {
				case "/conversations.open":
					opened = true
					if r.Form.Get("users") != destination {
						t.Errorf("users: %s", r.Form.Get("users"))
					}
					fmt.Fprint(w, `{"ok":true,"channel":{"id":"D0123456789"}}`)
				case "/files.getUploadURLExternal":
					if (strings.HasPrefix(destination, "U") || strings.HasPrefix(destination, "W")) && !opened {
						t.Error("DM not opened before upload")
					}
					fmt.Fprintf(w, `{"ok":true,"file_id":"F01234567","upload_url":"http://%s/upload"}`, r.Host)
				case "/upload":
					w.WriteHeader(http.StatusOK)
				case "/files.completeUploadExternal":
					completed = true
					want := destination
					if opened {
						want = "D0123456789"
					}
					if r.Form.Get("channel_id") != want {
						t.Errorf("destination: got %q, want %q", r.Form.Get("channel_id"), want)
					}
					if want == "" && r.Form.Has("channel_id") {
						t.Error("private upload should omit channel_id")
					}
					fmt.Fprint(w, `{"ok":true,"files":[{"id":"F01234567"}]}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			_, _, err := uploadFiles(fileUploadTestClient(srv), fileUploadOptions{Paths: []string{path}, Channel: destination})
			if err != nil || !completed {
				t.Fatalf("completed=%v, error=%v", completed, err)
			}
		})
	}
}

func TestUploadFiles_Preflight(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	empty := fileUploadTestSource(t, "empty.png", "")
	directory := fileUploadTestDir(t)
	for _, tc := range []struct {
		name string
		opts fileUploadOptions
		want string
	}{
		{"no files", fileUploadOptions{}, "--file"},
		{"multiple title", fileUploadOptions{Paths: []string{path, path}, Title: "ambiguous"}, "--title"},
		{"comment without channel", fileUploadOptions{Paths: []string{path}, Comment: "caption"}, "destination"},
		{"thread without channel", fileUploadOptions{Paths: []string{path}, ThreadTS: "123.456"}, "destination"},
		{"later missing", fileUploadOptions{Paths: []string{path, filepath.Join(directory, "missing")}}, "checking upload"},
		{"directory", fileUploadOptions{Paths: []string{directory}}, "regular"},
		{"empty file", fileUploadOptions{Paths: []string{empty}}, "nonempty"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				t.Error("preflight failure must not make network calls")
			}))
			defer srv.Close()
			_, ids, err := uploadFiles(fileUploadTestClient(srv), tc.opts)
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(ids) != 0 || calls != 0 {
				t.Errorf("error=%v, IDs=%v, calls=%d", err, ids, calls)
			}
		})
	}
}

func TestUploadFiles_UnreadablePreflight(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode-000 files")
	}
	path := fileUploadTestSource(t, "unreadable.png", "bytes")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0600) })
	_, _, err := uploadFiles(api.New("xoxp-test"), fileUploadOptions{Paths: []string{path}})
	if err == nil || !strings.Contains(err.Error(), "opening upload file") {
		t.Fatalf("got %v", err)
	}
}

func TestUploadFiles_TicketFailures(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	for _, tc := range []struct {
		name, response string
		status         int
		want           string
		wantIDs        int
	}{
		{"missing ID", `{"ok":true,"upload_url":"https://files.slack.com/upload?signed=private"}`, 200, "file ID", 0},
		{"unsafe ID", `{"ok":true,"file_id":"xoxp-private","upload_url":"https://files.slack.com/upload"}`, 200, "file ID", 0},
		{"missing URL", `{"ok":true,"file_id":"F01234567"}`, 200, "invalid upload URL", 1},
		{"invalid URL", `{"ok":true,"file_id":"F01234567","upload_url":"https://%zz"}`, 200, "invalid upload URL", 1},
		{"credentials", `{"ok":true,"file_id":"F01234567","upload_url":"https://private:password@files.slack.com/upload"}`, 200, "invalid upload URL", 1},
		{"wrong scheme", `{"ok":true,"file_id":"F01234567","upload_url":"ftp://files.slack.com/upload"}`, 200, "HTTPS", 1},
		{"insecure remote", `{"ok":true,"file_id":"F01234567","upload_url":"http://files.slack.com/upload"}`, 200, "HTTPS", 1},
		{"malformed JSON", `{"ok":`, 200, "invalid JSON", 0},
		{"Slack error", `{"ok":false,"error":"invalid_auth"}`, 200, "invalid_auth", 0},
		{"HTTP error", `{"ok":true,"file_id":"F01234567","upload_url":"https://files.slack.com/upload"}`, 500, "HTTP status 500", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/files.getUploadURLExternal" {
					t.Errorf("unexpected call %s", r.URL.Path)
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.response)
			}))
			defer srv.Close()
			_, ids, err := uploadFiles(fileUploadTestClient(srv), fileUploadOptions{Paths: []string{path}})
			if err == nil || !strings.Contains(err.Error(), tc.want) || len(ids) != tc.wantIDs || calls != 1 {
				t.Fatalf("error=%v, IDs=%v, calls=%d", err, ids, calls)
			}
			for _, secret := range []string{"xoxp-private", "signed=private", "password", "files.slack.com"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("error exposed %q: %v", secret, err)
				}
			}
			if tc.name == "Slack error" {
				var apiErr *api.APIError
				if !errors.As(err, &apiErr) || apiErr.ExitCode() != 3 {
					t.Errorf("lost auth error mapping: %v", err)
				}
			}
		})
	}
}

func TestUploadFiles_ByteFailures(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	for _, tc := range []struct {
		name, body, contentType string
		status                  int
	}{
		{"HTTP failure", "private response", "", 500},
		{"redirect", "", "", 307},
		{"JSON failure", `{"ok":false,"error":"private"}`, "application/json", 200},
		{"bad JSON", `{"ok":`, "application/json", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				switch r.URL.Path {
				case "/files.getUploadURLExternal":
					fmt.Fprintf(w, `{"ok":true,"file_id":"F01234567","upload_url":"http://%s/upload?signed=private"}`, r.Host)
				case "/upload":
					w.Header().Set("Content-Type", tc.contentType)
					w.Header().Set("Location", "/should-not-follow")
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.body)
				default:
					t.Error("failed byte upload must not complete or follow redirects")
				}
			}))
			defer srv.Close()
			_, ids, err := uploadFiles(fileUploadTestClient(srv), fileUploadOptions{Paths: []string{path}})
			if err == nil || !strings.Contains(err.Error(), "F01234567") || len(ids) != 1 || calls != 2 {
				t.Fatalf("error=%v, IDs=%v, calls=%d", err, ids, calls)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), srv.URL) {
				t.Errorf("error exposed signed URL or response: %v", err)
			}
		})
	}
}

func TestUploadFiles_CompletionFailures(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	for _, tc := range []struct {
		name, response string
		status         int
		want           string
	}{
		{"missing files", `{"ok":true}`, 200, "did not confirm"},
		{"empty files", `{"ok":true,"files":[]}`, 200, "did not confirm"},
		{"wrong ID", `{"ok":true,"files":[{"id":"F01234568"}]}`, 200, "did not confirm"},
		{"malformed files", `{"ok":true,"files":{}}`, 200, "invalid file list"},
		{"bad JSON", `{"ok":`, 200, "invalid JSON"},
		{"Slack error", `{"ok":false,"error":"channel_not_found"}`, 200, "channel_not_found"},
		{"HTTP error", `{"ok":true,"files":[{"id":"F01234567"}]}`, 500, "HTTP status 500"},
		{"rate limited", `{"ok":false,"error":"ratelimited"}`, 429, "ratelimited"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var completions int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/files.getUploadURLExternal":
					fmt.Fprintf(w, `{"ok":true,"file_id":"F01234567","upload_url":"http://%s/upload"}`, r.Host)
				case "/upload":
					w.WriteHeader(http.StatusOK)
				case "/files.completeUploadExternal":
					completions++
					w.WriteHeader(tc.status)
					fmt.Fprint(w, tc.response)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
				}
			}))
			defer srv.Close()
			c := fileUploadTestClient(srv)
			c.MaxRetries = 3
			_, ids, err := uploadFiles(c, fileUploadOptions{Paths: []string{path}, Channel: "C0123456789"})
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "F01234567") ||
				len(ids) != 1 || completions != 1 {
				t.Fatalf("error=%v, IDs=%v, completions=%d", err, ids, completions)
			}
			if c.MaxRetries != 3 {
				t.Error("helper modified caller retry policy")
			}
		})
	}
}

func TestUploadFiles_PartialBatch(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	var tickets, uploads, completions int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			tickets++
			if tickets == 2 {
				fmt.Fprint(w, `{"ok":false,"error":"file_uploads_disabled"}`)
				return
			}
			fmt.Fprintf(w, `{"ok":true,"file_id":"F01234567","upload_url":"http://%s/upload?signed=private"}`, r.Host)
		case "/upload":
			uploads++
		case "/files.completeUploadExternal":
			completions++
		}
	}))
	defer srv.Close()
	_, ids, err := uploadFiles(fileUploadTestClient(srv), fileUploadOptions{Paths: []string{path, path}})
	if err == nil || !strings.Contains(err.Error(), "F01234567") ||
		!reflect.DeepEqual(ids, []string{"F01234567"}) || tickets != 2 || uploads != 1 || completions != 0 {
		t.Fatalf("error=%v, IDs=%v, tickets=%d, uploads=%d, completions=%d", err, ids, tickets, uploads, completions)
	}
}

func TestUploadFiles_DMFailures(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	for _, response := range []string{
		`{"ok":true}`, `{"ok":true,"channel":{"id":"C0123456789"}}`,
		`{"ok":false,"error":"user_not_found"}`, `{"ok":`,
	} {
		t.Run(response, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/conversations.open" {
					t.Error("invalid DM response must prevent ticket creation")
				}
				fmt.Fprint(w, response)
			}))
			defer srv.Close()
			_, ids, err := uploadFiles(fileUploadTestClient(srv), fileUploadOptions{Paths: []string{path}, Channel: "U0123456789"})
			if err == nil || len(ids) != 0 || calls != 1 {
				t.Fatalf("error=%v, IDs=%v, calls=%d", err, ids, calls)
			}
		})
	}
}

func TestUploadURL_Validation(t *testing.T) {
	for _, tc := range []struct {
		raw, base string
		ok        bool
	}{
		{"https://files.slack.com/upload?signed=private", "https://slack.com/api", true},
		{"http://127.0.0.1:1234/upload", "http://127.0.0.1:1234", true},
		{"http://localhost:1234/upload", "http://localhost:1234", true},
		{"http://127.0.0.1:1234/upload", "https://slack.com/api", false},
		{"http://files.slack.com/upload", "http://127.0.0.1:1234", false},
		{"https://user:private@files.slack.com/upload", "https://slack.com/api", false},
		{"https://files.slack.com/upload#private", "https://slack.com/api", false},
		{"/upload", "https://slack.com/api", false},
		{"file:///etc/passwd", "https://slack.com/api", false},
		{"https://:443/upload", "https://slack.com/api", false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			err := validateUploadURL(tc.raw, tc.base)
			if (err == nil) != tc.ok {
				t.Errorf("got %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

type uploadTestTransport func(*http.Request) (*http.Response, error)

func (f uploadTestTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUploadFiles_TransportErrorSanitized(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	c := api.New("xoxp-test")
	c.HTTP.Transport = uploadTestTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/files.getUploadURLExternal" {
			return &http.Response{
				StatusCode: 200, Header: make(http.Header),
				Body: io.NopCloser(strings.NewReader(`{"ok":true,"file_id":"F01234567","upload_url":"https://files.slack.com/upload?signed=private"}`)),
			}, nil
		}
		return nil, fmt.Errorf("private transport error for %s (xoxp-test)", r.URL)
	})
	_, ids, err := uploadFiles(c, fileUploadOptions{Paths: []string{path}})
	if err == nil || len(ids) != 1 || !strings.Contains(err.Error(), "F01234567") {
		t.Fatalf("error=%v, IDs=%v", err, ids)
	}
	for _, secret := range []string{"private", "xoxp-test", "files.slack.com", "signed="} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaked %q: %v", secret, err)
		}
	}
}

func TestUploadFiles_ProductionRejectsHTTP(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	c := api.New("xoxp-test")
	var calls int
	c.HTTP.Transport = uploadTestTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Path != "/api/files.getUploadURLExternal" {
			t.Errorf("unexpected byte upload to %s", r.URL.Path)
		}
		return &http.Response{
			StatusCode: 200, Header: make(http.Header),
			Body: io.NopCloser(strings.NewReader(`{"ok":true,"file_id":"F01234567","upload_url":"http://127.0.0.1:1234/upload?signed=private"}`)),
		}, nil
	})
	_, ids, err := uploadFiles(c, fileUploadOptions{Paths: []string{path}})
	if err == nil || !strings.Contains(err.Error(), "HTTPS") || len(ids) != 1 || calls != 1 {
		t.Fatalf("error=%v, IDs=%v, calls=%d", err, ids, calls)
	}
}

func TestUploadFiles_ConfirmsEntireBatch(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	for _, duplicate := range []bool{false, true} {
		t.Run(fmt.Sprint(duplicate), func(t *testing.T) {
			var tickets, uploads, completions int
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/files.getUploadURLExternal":
					tickets++
					id := "F01234567"
					if tickets == 2 && !duplicate {
						id = "F01234568"
					}
					fmt.Fprintf(w, `{"ok":true,"file_id":%q,"upload_url":"http://%s/upload"}`, id, r.Host)
				case "/upload":
					uploads++
				case "/files.completeUploadExternal":
					completions++
					fmt.Fprint(w, `{"ok":true,"files":[{"id":"F01234567"}]}`)
				}
			}))
			defer srv.Close()
			_, ids, err := uploadFiles(fileUploadTestClient(srv), fileUploadOptions{Paths: []string{path, path}})
			if err == nil || tickets != 2 {
				t.Fatalf("error=%v, tickets=%d", err, tickets)
			}
			if duplicate {
				if len(ids) != 1 || uploads != 1 || completions != 0 || !strings.Contains(err.Error(), "duplicate") {
					t.Errorf("error=%v, IDs=%v, uploads=%d, completions=%d", err, ids, uploads, completions)
				}
			} else if len(ids) != 2 || uploads != 2 || completions != 1 || !strings.Contains(err.Error(), "F01234568") {
				t.Errorf("error=%v, IDs=%v, uploads=%d, completions=%d", err, ids, uploads, completions)
			}
		})
	}
}

func TestUploadDryRun_Plan(t *testing.T) {
	paths := []string{
		fileUploadTestSource(t, "alice,diagram.png", "bytes"),
		fileUploadTestSource(t, "bob.png", "more bytes"),
	}
	var out bytes.Buffer
	err := uploadDryRun(&out, fileUploadOptions{
		Paths: paths, Channel: "U0123456789", Comment: "caption", AltText: "image description", ThreadTS: "123.456",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"conversations.open", "Content-Length=5", "Content-Length=10", "no bearer token",
		"channel_id:<dm-channel-id>", "initial_comment:caption", "thread_ts:123.456", "alt_txt:image description"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("missing %q in %s", want, out.String())
		}
	}
	if strings.Count(out.String(), "files.getUploadURLExternal") != 2 ||
		strings.Count(out.String(), "[dry-run] POST ") != 2 ||
		strings.Count(out.String(), "files.completeUploadExternal") != 1 {
		t.Errorf("unexpected plan: %s", out.String())
	}
}

func TestFileUpload_CaptionAndRepeatedFiles(t *testing.T) {
	path := fileUploadTestSource(t, "alice,diagram.png", "bytes")
	caption := fileUploadTestSource(t, "caption.txt", "caption from file\n")
	cmd := newFileUploadCommand(&GlobalFlags{DryRun: true})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--file", path, "--file", path, "--channel", "C0123456789",
		"--text-file", caption, "--thread", "123.456", "--alt-text", "A diagram"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Count(out.String(), "files.getUploadURLExternal") != 2 ||
		!strings.Contains(out.String(), "initial_comment:caption from file") ||
		!strings.Contains(out.String(), "filename:alice,diagram.png") {
		t.Errorf("unexpected plan: %s", out.String())
	}
}

func TestFileUpload_InvalidFlags(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	for _, args := range [][]string{
		{"--file", path, "--text", "caption"},
		{"--file", path, "--text-file", "-"},
		{"--file", path, "--thread", "123.456"},
		{"--file", path, "--channel", "C0123456789", "--text", "", "--text-file", "-"},
		{"--file", path, "--file", path, "--title", "ambiguous"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			cmd := newFileUploadCommand(&GlobalFlags{DryRun: true})
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil {
				t.Fatal("expected invalid flag combination to fail")
			}
		})
	}
}

func TestFileUpload_StdinCaption(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdin
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = original
		r.Close()
	})
	if _, err := w.WriteString("caption from stdin\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	cmd := newFileUploadCommand(&GlobalFlags{DryRun: true})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--file", path, "--channel", "C0123456789", "--text-file", "-"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "initial_comment:caption from stdin") {
		t.Errorf("unexpected plan: %s", out.String())
	}
}

func TestFileUpload_CommandOutput(t *testing.T) {
	path := fileUploadTestSource(t, "diagram.png", "bytes")
	t.Setenv("SLK_TOKEN", "xoxp-test")
	t.Setenv("SLK_CONFIG", filepath.Join(fileUploadTestDir(t), "missing.toml"))
	t.Setenv("SLK_PROFILE", "")
	const completion = `{"ok":true,"files":[{"id":"F01234567","title":"Shared diagram"}]}`
	for _, raw := range []bool{false, true} {
		t.Run(fmt.Sprint(raw), func(t *testing.T) {
			var uploads, completions int
			original := http.DefaultTransport
			t.Cleanup(func() { http.DefaultTransport = original })
			http.DefaultTransport = uploadTestTransport(func(r *http.Request) (*http.Response, error) {
				body := ""
				switch r.URL.String() {
				case "https://slack.com/api/files.getUploadURLExternal":
					body = `{"ok":true,"file_id":"F01234567","upload_url":"https://files.slack.com/upload?signed=private"}`
				case "https://files.slack.com/upload?signed=private":
					uploads++
					if r.ContentLength != 5 || r.Header.Get("Authorization") != "" {
						t.Errorf("upload length=%d, auth=%q", r.ContentLength, r.Header.Get("Authorization"))
					}
					body = "OK"
				case "https://slack.com/api/files.completeUploadExternal":
					completions++
					_ = r.ParseForm()
					if r.Form.Get("initial_comment") != "caption" {
						t.Errorf("comment=%q", r.Form.Get("initial_comment"))
					}
					body = completion
				default:
					return nil, fmt.Errorf("unexpected request; network access disabled")
				}
				return &http.Response{
					StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)),
				}, nil
			})
			cmd := newFileUploadCommand(&GlobalFlags{Raw: raw})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"--file", path, "--title", "Shared diagram", "--channel", "C0123456789", "--text", "caption"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			want := "uploaded F01234567\n"
			if raw {
				want = completion + "\n"
			}
			if out.String() != want || uploads != 1 || completions != 1 {
				t.Errorf("output=%q, uploads=%d, completions=%d", out.String(), uploads, completions)
			}
		})
	}
}
