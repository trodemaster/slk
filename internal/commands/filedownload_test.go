package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/howar31/slk/internal/api"
	"github.com/howar31/slk/internal/output"
	"github.com/spf13/cobra"
)

var fileDownloadTestPNG = []byte("\x89PNG\r\n\x1a\nimage-data")

func fileDownloadTestServer(t *testing.T, metadata map[string]any, bodyHandler http.HandlerFunc) (*api.Client, *int) {
	t.Helper()
	requests := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if r.Header.Get("Authorization") != "Bearer xoxp-file-download-test" {
			t.Error("missing bearer on the authenticated request")
		}
		if strings.HasSuffix(r.URL.Path, "/files.info") {
			if err := r.ParseForm(); err != nil || r.Form.Get("file") != "F01234567" {
				t.Error("files.info missing requested file ID")
			}
			file := map[string]any{
				"id": "F01234567", "name": "alice.png", "mimetype": "image/png",
				"filetype": "png", "mode": "hosted", "file_access": "visible",
				"size": int64(len(fileDownloadTestPNG)), "url_private_download": "http://" + r.Host + "/content",
			}
			for key, value := range metadata {
				file[key] = value
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": file})
			return
		}
		if bodyHandler != nil {
			bodyHandler(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(fileDownloadTestPNG)
	}))
	t.Cleanup(srv.Close)
	client := api.New("xoxp-file-download-test")
	client.BaseURL, client.HTTP, client.MaxRetries = srv.URL, srv.Client(), 0
	return client, requests
}

func TestFileDownloadPrivateTemporaryOutput(t *testing.T) {
	privateRoot := t.TempDir()
	t.Setenv("TMPDIR", privateRoot)
	client, calls := fileDownloadTestServer(t, nil, nil)
	result, err := fileDownload(context.Background(), client, "F01234567", "", "", defaultFileDownloadMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(result.Path)
		_ = os.Remove(filepath.Dir(result.Path))
	})
	if *calls != 2 || !filepath.IsAbs(result.Path) || filepath.Base(result.Path) != "alice.png" {
		t.Fatalf("unexpected result=%+v, calls=%d", result, *calls)
	}
	body, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(body, fileDownloadTestPNG) {
		t.Fatalf("body=%q, error=%v", body, err)
	}
	if result.ID != "F01234567" || result.Name != "alice.png" || result.Mimetype != "image/png" || result.Size != int64(len(body)) {
		t.Fatalf("unexpected metadata: %+v", result)
	}
	for _, tc := range []struct {
		path string
		mode os.FileMode
	}{
		{result.Path, 0600},
		{filepath.Dir(result.Path), 0700},
	} {
		info, err := os.Stat(tc.path)
		if err != nil || info.Mode().Perm() != tc.mode {
			t.Fatalf("path=%s, stat=%v, error=%v, want mode=%o", tc.path, info, err, tc.mode)
		}
	}
	entries, err := os.ReadDir(privateRoot)
	if err != nil || len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "slk-download-") {
		t.Fatalf("unexpected private directory contents: %v, error=%v", entries, err)
	}
}

func TestFileDownloadOutputFormats(t *testing.T) {
	client, _ := fileDownloadTestServer(t, nil, nil)
	path := filepath.Join(t.TempDir(), "saved.png")
	result, err := fileDownload(context.Background(), client, "F01234567", "", path, defaultFileDownloadMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"concise", "", "json", "jsonl", "table"} {
		t.Run(format, func(t *testing.T) {
			var out bytes.Buffer
			if err := output.Emit(&out, format, []fileDownloadResult{result}); err != nil {
				t.Fatal(err)
			}
			if format == "concise" || format == "" {
				if out.String() != result.Path+"\n" {
					t.Fatalf("concise output must contain only the path: %q", out.String())
				}
				return
			}
			if strings.Contains(out.String(), "url_private") || strings.Contains(out.String(), client.Token) {
				t.Fatal("output contains a URL or credential")
			}
			switch format {
			case "json":
				var records []fileDownloadResult
				if err := json.Unmarshal(out.Bytes(), &records); err != nil || !reflect.DeepEqual(records, []fileDownloadResult{result}) {
					t.Fatalf("JSON=%s error=%v", out.String(), err)
				}
			case "jsonl":
				var record fileDownloadResult
				if err := json.Unmarshal(out.Bytes(), &record); err != nil || record != result || strings.Count(out.String(), "\n") != 1 {
					t.Fatalf("JSONL=%s error=%v", out.String(), err)
				}
			case "table":
				for _, header := range []string{"ID", "NAME", "MIMETYPE", "SIZE", "PATH", result.Path} {
					if !strings.Contains(out.String(), header) {
						t.Fatalf("missing %q in table %q", header, out.String())
					}
				}
			}
		})
	}
}

func TestFileDownloadPrivateURLFallbackAndGenericFile(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer xoxp-file-download-test" {
			t.Error("missing bearer")
		}
		if strings.HasSuffix(r.URL.Path, "/files.info") {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "file": map[string]any{
				"id": "F01234567", "name": "report.txt", "mimetype": "text/plain",
				"url_private": "http://" + r.Host + "/fallback",
			}})
			return
		}
		if r.URL.Path != "/fallback" {
			t.Errorf("unexpected fallback path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "a generic file")
	}))
	defer srv.Close()
	client := api.New("xoxp-file-download-test")
	client.BaseURL, client.HTTP = srv.URL, srv.Client()
	result, err := fileDownload(context.Background(), client, "F01234567", "", filepath.Join(t.TempDir(), "report.txt"), 100)
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(result.Path)
	if err != nil || string(body) != "a generic file" || result.Size != int64(len(body)) || result.Mimetype != "text/plain" {
		t.Fatalf("result=%+v, body=%q, err=%v", result, body, err)
	}
	if calls != 2 {
		t.Fatalf("requests=%d, want 2", calls)
	}
}

func TestFileDownloadDirectProtectedImage(t *testing.T) {
	client, calls := fileDownloadTestServer(t, nil, nil)
	result, err := fileDownload(context.Background(), client, "", client.BaseURL+"/content", filepath.Join(t.TempDir(), "image.png"), 100)
	if err != nil {
		t.Fatal(err)
	}
	if *calls != 1 || result.ID != "" || result.Mimetype != "image/png" {
		t.Fatalf("result=%+v, calls=%d", result, *calls)
	}
}

func TestFileDownloadMetadataFailuresDoNotCreateFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta map[string]any
		want string
	}{
		{"external flag", map[string]any{"is_external": true}, "external files"},
		{"external mode", map[string]any{"mode": "external"}, "external files"},
		{"restricted access", map[string]any{"file_access": "check_file_info"}, "access is restricted"},
		{"missing URLs", map[string]any{"url_private_download": ""}, "no private download URL"},
		{"unknown host", map[string]any{"url_private_download": "https://example.com/file?signature=private"}, "official files.slack.com"},
		{"oversized metadata", map[string]any{"size": 1000}, "metadata size"},
		{"negative metadata", map[string]any{"size": -1}, "metadata size"},
		{"missing metadata", map[string]any{"id": ""}, "no file metadata"},
		{"conflicting image MIME", map[string]any{"mimetype": "text/html"}, "conflicts with metadata MIME"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			client, calls := fileDownloadTestServer(t, tc.meta, nil)
			_, err := fileDownload(context.Background(), client, "F01234567", "", "", 100)
			if err == nil || !strings.Contains(err.Error(), tc.want) || strings.Contains(err.Error(), "signature=private") {
				t.Fatalf("unexpected error: %v", err)
			}
			if *calls != 1 {
				t.Fatalf("unsafe metadata triggered a content request: calls=%d", *calls)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("created files on metadata error: %v, %v", entries, readErr)
			}
		})
	}
}

func TestFileDownloadErrorsCleanExactPartialFiles(t *testing.T) {
	for _, tc := range []struct {
		name string
		meta map[string]any
		body http.HandlerFunc
		max  int64
	}{
		{"unauthorized", nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(401) }, 100},
		{"forbidden", nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(403) }, 100},
		{"missing", nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) }, 100},
		{"rate limit", nil, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }, 100},
		{"HTML image", map[string]any{"size": 0}, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, "<html>login</html>")
		}, 100},
		{"chunked oversized", map[string]any{"size": 0, "mimetype": "text/plain", "filetype": "text"}, func(w http.ResponseWriter, r *http.Request) {
			w.(http.Flusher).Flush()
			fmt.Fprint(w, strings.Repeat("x", 1000))
		}, 50},
		{"interrupted", map[string]any{"size": 0}, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "1500")
			w.Header().Set("Content-Type", "image/png")
			w.Write(fileDownloadTestPNG)
			fmt.Fprint(w, strings.Repeat("x", 800))
		}, 2000},
	} {
		for _, explicit := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/explicit=%t", tc.name, explicit), func(t *testing.T) {
				dir := t.TempDir()
				t.Setenv("TMPDIR", dir)
				keep := filepath.Join(dir, "keep.txt")
				if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
					t.Fatal(err)
				}
				client, _ := fileDownloadTestServer(t, tc.meta, tc.body)
				destination := ""
				if explicit {
					destination = filepath.Join(dir, "partial.png")
				}
				_, err := fileDownload(context.Background(), client, "F01234567", "", destination, tc.max)
				if err == nil {
					t.Fatal("expected failure")
				}
				entries, readErr := os.ReadDir(dir)
				if readErr != nil || len(entries) != 1 || entries[0].Name() != "keep.txt" {
					t.Fatalf("partial output remains or unrelated file removed: %v, error=%v; download error=%v", entries, readErr, err)
				}
				body, readErr := os.ReadFile(keep)
				if readErr != nil || string(body) != "keep" {
					t.Fatal("unrelated file changed")
				}
			})
		}
	}
}

func TestFileDownloadRejectsExistingOutputsAndSymlinks(t *testing.T) {
	for _, kind := range []string{"existing file", "symlink", "dangling symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			keep := filepath.Join(dir, "keep.txt")
			if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(dir, "output.png")
			switch kind {
			case "existing file":
				target = keep
			case "symlink":
				if err := os.Symlink(keep, target); err != nil {
					t.Fatal(err)
				}
			case "dangling symlink":
				if err := os.Symlink(filepath.Join(dir, "missing"), target); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(target, 0700); err != nil {
					t.Fatal(err)
				}
			}
			client, calls := fileDownloadTestServer(t, nil, nil)
			_, err := fileDownload(context.Background(), client, "F01234567", "", target, 100)
			if err == nil || *calls != 1 {
				t.Fatalf("error=%v, requests=%d", err, *calls)
			}
			body, readErr := os.ReadFile(keep)
			if readErr != nil || string(body) != "keep" {
				t.Fatal("existing file was modified")
			}
			if _, err := os.Lstat(target); err != nil {
				t.Fatalf("existing output was removed: %v", err)
			}
		})
	}
}

func TestFileDownloadCanonicalizesOutputParent(t *testing.T) {
	dir := t.TempDir()
	realParent := filepath.Join(dir, "destination")
	if err := os.Mkdir(realParent, 0700); err != nil {
		t.Fatal(err)
	}
	linkedParent := filepath.Join(dir, "linked")
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	client, calls := fileDownloadTestServer(t, nil, nil)
	target := filepath.Join(linkedParent, "image.png")
	result, err := fileDownload(context.Background(), client, "F01234567", "", target, 100)
	if err != nil {
		t.Fatal(err)
	}
	canonicalParent, err := filepath.EvalSymlinks(realParent)
	if err != nil {
		t.Fatal(err)
	}
	if result.Path != filepath.Join(canonicalParent, "image.png") || *calls != 2 {
		t.Fatalf("path=%q, requests=%d", result.Path, *calls)
	}
	body, err := os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(body, fileDownloadTestPNG) {
		t.Fatalf("body=%q, error=%v", body, err)
	}
	if _, err := fileDownload(context.Background(), client, "F01234567", "", target, 100); err == nil || *calls != 3 {
		t.Fatalf("existing final file was not protected: requests=%d, error=%v", *calls, err)
	}
	symlinkTarget := filepath.Join(linkedParent, "file-link.png")
	if err := os.Symlink(result.Path, symlinkTarget); err != nil {
		t.Fatal(err)
	}
	if _, err := fileDownload(context.Background(), client, "F01234567", "", symlinkTarget, 100); err == nil || *calls != 4 {
		t.Fatalf("final symlink was not rejected: requests=%d, error=%v", *calls, err)
	}
	body, err = os.ReadFile(result.Path)
	if err != nil || !bytes.Equal(body, fileDownloadTestPNG) {
		t.Fatal("existing file changed through canonicalized parent")
	}
}

func TestFileDownloadSanitizesTemporaryName(t *testing.T) {
	dir := t.TempDir()
	canonicalDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	aliasDir := filepath.Join(t.TempDir(), "temporary-root")
	if err := os.Symlink(dir, aliasDir); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", aliasDir)
	client, _ := fileDownloadTestServer(t, map[string]any{"name": "../../unsafe\nname.png"}, nil)
	result, err := fileDownload(context.Background(), client, "F01234567", "", "", 100)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Remove(result.Path)
		_ = os.Remove(filepath.Dir(result.Path))
	})
	if filepath.Base(result.Path) != "unsafe_name.png" || filepath.Dir(filepath.Dir(result.Path)) != canonicalDir {
		t.Fatalf("unsafe output path %q", result.Path)
	}
}

func TestFileDownloadCommandValidationDryRunAndRaw(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
		ok   bool
	}{
		{"file dry run", []string{"--dry-run", "download", "--file", "F01234567"}, "files.info file=F01234567", true},
		{"URL dry run", []string{"--dry-run", "download", "--url", "https://files.slack.com/file?signature=private"}, "known Slack-hosted image URL", true},
		{"explicit output dry run", []string{"--dry-run", "download", "--file", "F01234567", "--output", "does-not-exist/image.png"}, "no overwrite", true},
		{"raw", []string{"--raw", "download", "--file", "F01234567"}, "--raw", false},
		{"raw dry run", []string{"--raw", "--dry-run", "download", "--file", "F01234567"}, "--raw", false},
		{"zero bound", []string{"--dry-run", "download", "--file", "F01234567", "--max-bytes", "0"}, "positive", false},
		{"negative bound", []string{"--dry-run", "download", "--file", "F01234567", "--max-bytes", "-1"}, "positive", false},
		{"both sources", []string{"--dry-run", "download", "--file", "F01234567", "--url", "https://files.slack.com/file"}, "file", false},
		{"neither source", []string{"--dry-run", "download"}, "file", false},
		{"external URL", []string{"--dry-run", "download", "--url", "https://example.com/private"}, "official files.slack.com", false},
		{"bad format", []string{"--dry-run", "--format", "invalid", "download", "--file", "F01234567"}, "output format", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("TMPDIR", dir)
			t.Setenv("HOME", dir)
			t.Setenv("SLK_TOKEN", "")
			g := &GlobalFlags{}
			root := &cobra.Command{Use: "slk", SilenceUsage: true, SilenceErrors: true}
			bindGlobalFlags(root, g)
			root.AddCommand(newFileDownloadCommand(g))
			var out bytes.Buffer
			root.SetOut(&out)
			root.SetArgs(tc.args)
			err := root.Execute()
			if (err == nil) != tc.ok {
				t.Fatalf("error=%v, output=%q", err, out.String())
			}
			text := out.String()
			if err != nil {
				text = err.Error()
			}
			if !strings.Contains(text, tc.want) || strings.Contains(text, "signature=private") {
				t.Fatalf("unexpected text: %q, want %q", text, tc.want)
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil || len(entries) != 0 {
				t.Fatalf("dry-run/validation wrote files or read config: %v, %v", entries, readErr)
			}
		})
	}
	cmd := newFileDownloadCommand(&GlobalFlags{})
	for _, flag := range []string{"file", "url", "output", "max-bytes"} {
		if cmd.Flags().Lookup(flag) == nil {
			t.Errorf("missing flag --%s", flag)
		}
	}
	if value := cmd.Flags().Lookup("max-bytes").DefValue; value != fmt.Sprint(defaultFileDownloadMaxBytes) {
		t.Fatalf("unexpected default bound %s", value)
	}
}
