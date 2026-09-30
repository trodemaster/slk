package api

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var downloadTestPNG = []byte("\x89PNG\r\n\x1a\nimage-data")

func downloadTestClient(srv *httptest.Server) *Client {
	c := New("xoxp-download-test")
	c.BaseURL = srv.URL
	c.HTTP = srv.Client()
	c.MaxRetries = 0
	return c
}

func TestDownloadAuthenticatedBoundedStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer xoxp-download-test" {
			t.Errorf("unexpected download request: %s, authorization present=%t", r.Method, r.Header.Get("Authorization") != "")
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(downloadTestPNG)
	}))
	defer srv.Close()
	c := downloadTestClient(srv)
	var out bytes.Buffer
	result, err := c.Download(context.Background(), srv.URL+"/file?signature=private", &out, DownloadOptions{
		MaxBytes: int64(len(downloadTestPNG)), ExpectedSize: int64(len(downloadTestPNG)), ExpectedMIME: "image/png",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(out.Bytes(), downloadTestPNG) || result.Size != int64(len(downloadTestPNG)) || result.Mimetype != "image/png" {
		t.Fatalf("unexpected download result: %+v, bytes=%q", result, out.Bytes())
	}
}

func TestDownloadHTTPErrorMappingsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   int
		slack  string
	}{
		{401, 3, "invalid_auth"},
		{403, 3, "invalid_auth"},
		{404, 4, "file_not_found"},
		{429, 5, "ratelimited"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				fmt.Fprint(w, "sensitive error body with xoxp-download-test")
			}))
			defer srv.Close()
			c := downloadTestClient(srv)
			var out bytes.Buffer
			_, err := c.Download(context.Background(), srv.URL+"/file?signature=secret-signature", &out, DownloadOptions{MaxBytes: 100})
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.SlackError != tc.slack {
				t.Fatalf("got %v, want APIError %s", err, tc.slack)
			}
			if apiErr.ExitCode() != tc.code {
				t.Fatalf("exit code=%d, want %d", apiErr.ExitCode(), tc.code)
			}
			if out.Len() != 0 || strings.Contains(err.Error(), "secret-signature") || strings.Contains(err.Error(), c.Token) || strings.Contains(err.Error(), srv.URL) {
				t.Fatalf("error leaked content or wrote body: %v", err)
			}
		})
	}
}

func TestDownload429RetryAndCancellation(t *testing.T) {
	t.Run("retry success", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls == 1 {
				w.Header().Set("Retry-After", "1")
				w.WriteHeader(429)
				return
			}
			fmt.Fprint(w, "file")
		}))
		defer srv.Close()
		c := downloadTestClient(srv)
		c.MaxRetries = 1
		var out bytes.Buffer
		_, err := c.Download(context.Background(), srv.URL, &out, DownloadOptions{MaxBytes: 20})
		if err != nil || calls != 2 || out.String() != "file" {
			t.Fatalf("calls=%d, body=%q, err=%v", calls, out.String(), err)
		}
	})
	t.Run("bounded wait honors context", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(429)
		}))
		defer srv.Close()
		c := downloadTestClient(srv)
		c.MaxRetries = 1000
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
		defer cancel()
		_, err := c.Download(ctx, srv.URL, io.Discard, DownloadOptions{MaxBytes: 20})
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("got %v, want context deadline", err)
		}
	})
	t.Run("long wait does not retry early", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Retry-After", "999999")
			w.WriteHeader(429)
		}))
		defer srv.Close()
		c := downloadTestClient(srv)
		_, err := c.Download(context.Background(), srv.URL, io.Discard, DownloadOptions{MaxBytes: 20})
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.ExitCode() != 5 || calls != 1 {
			t.Fatalf("calls=%d, err=%v; expected rate limit without an early retry", calls, err)
		}
	})
}

func TestDownloadRejectsUnsafeInitialURLs(t *testing.T) {
	c := New("xoxp-download-test")
	for _, raw := range []string{
		"http://files.slack.com/file",
		"https://example.com/file",
		"https://files.slack.com.example.com/file",
		"https://files.slack.com:444/file",
		"https://alice:secret@files.slack.com/file",
		"file:///some/file",
		"https://files.slack.com/file#fragment",
		"https://files.slack.com/%zz?signature=secret",
	} {
		t.Run(raw, func(t *testing.T) {
			err := c.ValidateDownloadURL(raw)
			if err == nil {
				t.Fatalf("accepted unsafe URL")
			}
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), raw) {
				t.Fatalf("URL leaked in error: %v", err)
			}
		})
	}
	if err := c.ValidateDownloadURL("https://files.slack.com/files-pri/file.png"); err != nil {
		t.Fatalf("official URL rejected: %v", err)
	}
	c.BaseURL = "https://example.com/api"
	if c.ValidateDownloadURL("https://example.com/file") == nil {
		t.Fatal("non-loopback BaseURL allowed external bearer authentication")
	}
	c.BaseURL = "http://127.0.0.1:43210/api"
	if err := c.ValidateDownloadURL("http://127.0.0.1:43210/file"); err != nil {
		t.Fatal(err)
	}
	if c.ValidateDownloadURL("http://127.0.0.1:43211/file") == nil {
		t.Fatal("different test origin accepted")
	}
}

func TestDownloadRedirectsPermanentlyStripBearer(t *testing.T) {
	var source *httptest.Server
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("bearer forwarded across origins")
		}
		http.Redirect(w, r, source.URL+"/return", http.StatusFound)
	}))
	defer target.Close()
	source = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/return" {
			if r.Header.Get("Authorization") != "" {
				t.Error("bearer restored after returning to the original origin")
			}
			fmt.Fprint(w, "file")
			return
		}
		if r.Header.Get("Authorization") == "" {
			t.Error("initial bearer missing")
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer source.Close()
	c := downloadTestClient(source)
	var out bytes.Buffer
	if _, err := c.Download(context.Background(), source.URL, &out, DownloadOptions{MaxBytes: 20}); err != nil {
		t.Fatal(err)
	}
	if out.String() != "file" {
		t.Fatalf("got body %q", out.String())
	}
}

func TestDownloadRejectsDowngradeCredentialsAndRedirectLoop(t *testing.T) {
	for _, tc := range []struct {
		name, location, want string
	}{
		{"downgrade", "http://127.0.0.1:1/file?secret=yes", "downgrade"},
		{"credentials", "https://alice:secret@files.slack.com/file", "unsafe redirect"},
		{"loop", "/loop", "too many redirects"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, tc.location, http.StatusFound)
			}))
			defer srv.Close()
			c := downloadTestClient(srv)
			_, err := c.Download(context.Background(), srv.URL, io.Discard, DownloadOptions{MaxBytes: 20})
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), c.Token) {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestDownloadPreservesHTTPTimeoutAndRedirectPolicy(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(50 * time.Millisecond)
			fmt.Fprint(w, "file")
		}))
		defer srv.Close()
		c := downloadTestClient(srv)
		c.HTTP.Timeout = 5 * time.Millisecond
		_, err := c.Download(context.Background(), srv.URL+"/file?secret=yes", io.Discard, DownloadOptions{MaxBytes: 20})
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unexpected error: %v", err)
		}
		if c.HTTP.Timeout != 5*time.Millisecond || c.HTTP.CheckRedirect != nil {
			t.Fatal("original HTTP client modified")
		}
	})
	t.Run("custom redirect policy", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/destination", http.StatusFound)
		}))
		defer srv.Close()
		c := downloadTestClient(srv)
		c.HTTP.CheckRedirect = func(*http.Request, []*http.Request) error {
			return errors.New("private URL or token in existing callback error")
		}
		_, err := c.Download(context.Background(), srv.URL, io.Discard, DownloadOptions{MaxBytes: 20})
		if err == nil || strings.Contains(err.Error(), "private URL") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestDownloadOversizedChunkedAndInterruptedBodies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		serve func(http.ResponseWriter, *http.Request)
		opts  DownloadOptions
	}{
		{"content length", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "too much content") }, DownloadOptions{MaxBytes: 3}},
		{"chunked", func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			fmt.Fprint(w, strings.Repeat("x", 1000))
		}, DownloadOptions{MaxBytes: 50}},
		{"interrupted", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1000")
			fmt.Fprint(w, "short")
		}, DownloadOptions{MaxBytes: 2000}},
		{"interrupted after prefix", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1500")
			fmt.Fprint(w, strings.Repeat("x", 800))
		}, DownloadOptions{MaxBytes: 2000}},
		{"chunked metadata truncation", func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			fmt.Fprint(w, "short")
		}, DownloadOptions{MaxBytes: 100, ExpectedSize: 10}},
		{"metadata content length mismatch", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, "short")
		}, DownloadOptions{MaxBytes: 100, ExpectedSize: 10}},
		{"empty", func(w http.ResponseWriter, r *http.Request) {}, DownloadOptions{MaxBytes: 100}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(tc.serve))
			defer srv.Close()
			c := downloadTestClient(srv)
			var out bytes.Buffer
			_, err := c.Download(context.Background(), srv.URL, &out, tc.opts)
			if err == nil {
				t.Fatal("expected failed download")
			}
			if int64(out.Len()) > tc.opts.MaxBytes {
				t.Fatalf("wrote %d bytes over limit %d", out.Len(), tc.opts.MaxBytes)
			}
		})
	}
}

func TestDownloadImageValidationAndGenericFiles(t *testing.T) {
	for _, tc := range []struct {
		name, expected, declared string
		body                     []byte
		wantError                bool
	}{
		{"login page", "image/png", "text/html", []byte("<html>login required</html>"), true},
		{"fake image header", "image/png", "image/png", []byte("<html>login</html>"), true},
		{"metadata mismatch", "image/jpeg", "image/png", downloadTestPNG, true},
		{"header mismatch", "image/png", "image/jpeg", downloadTestPNG, true},
		{"binary content type", "image/png", "application/octet-stream", downloadTestPNG, false},
		{"direct image", "image/*", "image/png", downloadTestPNG, false},
		{"direct nonimage", "image/*", "text/plain", []byte("not an image"), true},
		{"invalid metadata MIME", "image/png invalid", "text/html", []byte("<html>login</html>"), true},
		{"generic", "", "text/plain", []byte("a generic file"), false},
		{"generic html", "text/html", "text/html", []byte("<html>user document</html>"), false},
		{"svg", "image/svg+xml", "image/svg+xml", []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg"><path/></svg>`), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.declared)
				w.Write(tc.body)
			}))
			defer srv.Close()
			c := downloadTestClient(srv)
			var out bytes.Buffer
			result, err := c.Download(context.Background(), srv.URL, &out, DownloadOptions{MaxBytes: 1000, ExpectedMIME: tc.expected})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v, want error=%t", err, tc.wantError)
			}

			if err == nil && (!bytes.Equal(out.Bytes(), tc.body) || result.Mimetype == "") {
				t.Fatalf("unexpected result %+v body=%q", result, out.Bytes())
			}
			if err != nil && out.Len() != 0 {
				t.Fatal("invalid image was written to destination")
			}
		})
	}
}

func TestDownloadChunkedExactLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.(http.Flusher).Flush()
		w.Write(downloadTestPNG)
	}))
	defer srv.Close()
	var out bytes.Buffer
	c := downloadTestClient(srv)
	result, err := c.Download(context.Background(), srv.URL, &out, DownloadOptions{MaxBytes: int64(len(downloadTestPNG)), ExpectedMIME: "image/png"})
	if err != nil || !bytes.Equal(out.Bytes(), downloadTestPNG) || result.Size != int64(len(downloadTestPNG)) {
		t.Fatalf("result=%+v, bytes=%q, error=%v", result, out.Bytes(), err)
	}
}
