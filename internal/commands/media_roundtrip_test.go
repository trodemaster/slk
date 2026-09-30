package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/howar31/slk/internal/api"
)

func TestImageAttachment_UploadDownloadAndView(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 2, 2))
	pixel := color.RGBA{R: 255, G: 70, B: 80, A: 255}
	picture.SetRGBA(0, 0, pixel)
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, picture); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "diagram.png")
	if err := os.WriteFile(source, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", t.TempDir())
	var uploaded []byte
	var uploadMu sync.Mutex
	getUploaded := func() []byte {
		uploadMu.Lock()
		defer uploadMu.Unlock()
		return append([]byte(nil), uploaded...)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/files.getUploadURLExternal":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("filename") != "diagram.png" || r.Form.Get("alt_txt") != "A red pixel" {
				t.Errorf("upload metadata: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "file_id": "F01234567", "upload_url": "http://" + r.Host + "/upload",
			})
		case "/upload":
			if r.Header.Get("Authorization") != "" || r.ContentLength != int64(encoded.Len()) {
				t.Error("byte upload must have an exact length and no bearer token")
			}
			var err error
			uploadMu.Lock()
			uploaded, err = io.ReadAll(r.Body)
			uploadMu.Unlock()
			if err != nil {
				t.Error(err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
		case "/files.completeUploadExternal":
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			if r.Form.Get("channel_id") != "C0123456789" || r.Form.Get("initial_comment") != "Here is the image" {
				t.Errorf("shared image message: %v", r.Form)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "files": []map[string]string{{"id": "F01234567"}},
			})
		case "/files.info":
			content := getUploaded()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true, "file": map[string]any{
					"id": "F01234567", "name": "diagram.png", "mimetype": "image/png",
					"size": len(content), "url_private_download": "http://" + r.Host + "/content",
				},
			})
		case "/content":
			if r.Header.Get("Authorization") == "" {
				t.Error("protected content requires bearer authentication")
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(getUploaded())
		default:
			t.Errorf("unexpected endpoint: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer srv.Close()
	client := api.New("xoxp-image-roundtrip-test")
	client.BaseURL, client.HTTP = srv.URL, srv.Client()
	_, ids, err := uploadFiles(client, fileUploadOptions{
		Paths: []string{source}, Channel: "C0123456789", Comment: "Here is the image", AltText: "A red pixel",
	})
	if err != nil || len(ids) != 1 || !bytes.Equal(getUploaded(), encoded.Bytes()) {
		t.Fatalf("upload: ids=%v err=%v", ids, err)
	}
	result, err := fileDownload(context.Background(), client, ids[0], "", "", defaultFileDownloadMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(filepath.Dir(result.Path))
	defer os.Remove(result.Path)
	saved, err := os.Open(result.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer saved.Close()
	viewed, err := png.Decode(saved)
	if err != nil {
		t.Fatalf("agent path does not contain a viewable PNG: %v", err)
	}
	if viewed.Bounds() != picture.Bounds() || color.RGBAModel.Convert(viewed.At(0, 0)) != pixel ||
		result.Mimetype != "image/png" || result.Size != int64(encoded.Len()) {
		t.Fatalf("image or metadata changed: bounds=%v result=%+v", viewed.Bounds(), result)
	}
}
