package commands

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestParseChannelMessages_FilesAndImages(t *testing.T) {
	raw := []byte(`{"ok":true,"messages":[{
		"user":"U0123456789","text":"","ts":"1790780000.000001","thread_ts":"1790770000.000001",
		"files":[{"id":"F01234567","name":"diagram.png","title":"Diagram","mimetype":"image/png",
			"filetype":"png","size":123,"original_w":640,"original_h":480,
			"url_private":"https://files.slack.com/files-pri/T01234567-F01234567/diagram.png"}],
		"blocks":[
			{"type":"image","slack_file":{"id":"F01234567"},"alt_text":"Architecture"},
			{"type":"section","accessory":{"type":"image","image_url":"https://example.com/chart.png","alt_text":"Chart"}},
			{"type":"context","elements":[{"type":"image","slack_file":{"id":"F01234568"},"alt_text":"Another image"}]}
		],
		"attachments":[{"title":"Legacy screenshot","image_url":"https://example.com/legacy.png",
			"thumb_url":"https://example.com/legacy-thumb.png",
			"blocks":[{"type":"image","image_url":"https://example.com/chart.png","alt_text":"Duplicate"}]}]
	}]}`)
	for _, withThread := range []bool{false, true} {
		items, err := parseChannelMessages(raw, nil, withThread)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || len(items[0].Files) != 1 || len(items[0].Images) != 4 {
			t.Fatalf("unexpected items: %+v", items)
		}
		m := items[0]
		if m.Text != "" || m.User != "U0123456789" || m.TS != "1790780000.000001" {
			t.Fatalf("lost message identity: %+v", m)
		}
		if (m.Thread != "") != withThread {
			t.Fatalf("thread output: %q withThread=%v", m.Thread, withThread)
		}
		f := m.Files[0]
		if f.ID != "F01234567" || f.Mimetype != "image/png" || f.Width != 640 || f.Height != 480 ||
			f.Size != 123 || f.AltText != "Architecture" || !f.RequiresAuth || f.URL == "" {
			t.Fatalf("lost file metadata: %+v", f)
		}
		if m.Images[0].URL != "https://example.com/chart.png" || m.Images[0].RequiresAuth ||
			m.Images[0].AltText != "Chart" || m.Images[0].Source != "block" {
			t.Fatalf("public block image: %+v", m.Images[0])
		}
		if m.Images[1].FileID != "F01234568" || !m.Images[1].RequiresAuth {
			t.Fatalf("protected image: %+v", m.Images[1])
		}
		if m.Images[2].Source != "attachment" || m.Images[2].AltText != "Legacy screenshot" {
			t.Fatalf("legacy image: %+v", m.Images[2])
		}
		concise := m.Concise()
		for _, want := range []string{"diagram.png", "F01234567", "file download", "https://example.com/chart.png", "F01234568"} {
			if !strings.Contains(concise, want) {
				t.Errorf("concise missing %q: %s", want, concise)
			}
		}
	}
}

func TestMessageMedia_PlaceholdersExternalAndURLs(t *testing.T) {
	var message slackMessage
	if err := json.Unmarshal([]byte(`{
		"files":[
			{"id":"F01234567","file_access":"check_file_info"},
			{"id":"F01234568","name":"external.png","mode":"external","is_external":true,
				"url_private":"https://provider.example/old","external_url":"https://provider.example/image.png"},
			{"id":"F01234569","name":"fallback.png","url_private_download":"https://files.slack.com/download.png"},
			{"id":"F01234569","name":"duplicate.png"}
		],
		"blocks":[
			{"type":"image","slack_file":{"url":"https://files.slack.com/files-pri/T01234567-F01234570/image.png"},"alt_text":"Private"},
			{"type":"image","slack_file":{"url":"https://example.slack.com/files/U0123456789/F01234571/image.png"}},
			{"type":"image","image_url":"https://slack.com.attacker.example/image.png","alt_text":"External"},
			{"type":"image","slack_file":{"id":"F01234570"}},
			{"type":"image"}
		]
	}`), &message); err != nil {
		t.Fatal(err)
	}
	files, images := messageMedia(message)
	if len(files) != 3 || len(images) != 3 {
		t.Fatalf("unexpected files/images: %+v %+v", files, images)
	}
	if files[0].FileAccess != "check_file_info" || !files[0].RequiresAuth {
		t.Fatalf("placeholder: %+v", files[0])
	}
	if !files[1].External || files[1].RequiresAuth || files[1].URL != "https://provider.example/image.png" {
		t.Fatalf("external file: %+v", files[1])
	}
	if files[2].URL != "https://files.slack.com/download.png" {
		t.Fatalf("URL fallback: %+v", files[2])
	}
	if images[0].FileID != "F01234570" || images[1].FileID != "F01234571" || images[2].RequiresAuth {
		t.Fatalf("image access classification: %+v", images)
	}
}

func TestParseSearchMessages_Media(t *testing.T) {
	items, err := parseSearchMessages([]byte(`{"ok":true,"messages":{"matches":[{
		"username":"Alice","text":"Image","ts":"1790780000.000001",
		"files":[{"id":"F01234567","name":"image.png","mimetype":"image/png"}],
		"blocks":[{"type":"image","image_url":"https://example.com/image.png","alt_text":"Image"}]
	}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].User != "Alice" || len(items[0].Files) != 1 || len(items[0].Images) != 1 {
		t.Fatalf("search metadata: %+v", items)
	}
	hits, err := parseSearchAll([]byte(`{"ok":true,"messages":{"matches":[{
		"username":"Alice","ts":"1790780000.000001",
		"files":[{"id":"F01234567","name":"image.png"}]
	}]},"files":{"matches":[{"id":"F01234568","name":"another.png",
		"url_private":"https://files.slack.com/another.png"}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 || len(hits[0].Files) != 1 || len(hits[1].Files) != 1 || !hits[1].Files[0].RequiresAuth {
		t.Fatalf("combined search metadata: %+v", hits)
	}
	for _, format := range []string{"concise", "json", "jsonl", "table"} {
		var out bytes.Buffer
		if err := emitMediaHits(&out, format, hits); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "F01234567") || !strings.Contains(out.String(), "F01234568") {
			t.Errorf("%s lost combined search metadata: %s", format, out.String())
		}
	}
}

func TestEmitMessages_PlainCompatibilityAndMediaFormats(t *testing.T) {
	plain, err := parseChannelMessages([]byte(`{"messages":[{"user":"Alice","text":"hello","ts":"1.0"}]}`), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := emitMessages(&out, "json", plain); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"files"`) || strings.Contains(out.String(), `"images"`) {
		t.Fatalf("plain JSON gained media fields: %s", out.String())
	}
	out.Reset()
	if err := emitMessages(&out, "table", plain); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "FILES") || strings.Contains(out.String(), "IMAGES") {
		t.Fatalf("plain table changed columns: %s", out.String())
	}
	withMedia := []msgItem{{
		User: "Alice", TS: "1.0",
		Files:  []fileItem{{ID: "F01234567", Name: "image.png", RequiresAuth: true}},
		Images: []imageItem{{URL: "https://example.com/image.png", Source: "block"}},
	}}
	for _, format := range []string{"concise", "json", "jsonl", "table"} {
		out.Reset()
		if err := emitMessages(&out, format, withMedia); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out.String(), "F01234567") || !strings.Contains(out.String(), "https://example.com/image.png") {
			t.Errorf("%s output lost media: %s", format, out.String())
		}
		if format == "json" || format == "jsonl" {
			if !strings.Contains(out.String(), `"requires_auth":true`) && !strings.Contains(out.String(), `"requires_auth": true`) {
				t.Errorf("%s output lost auth marker: %s", format, out.String())
			}
		}
	}
}

func TestParseFileDetails_MetadataAndErrors(t *testing.T) {
	item, err := parseFileDetails([]byte(`{"ok":true,"file":{
		"id":"F01234567","name":"image.png","filetype":"png","mimetype":"image/png",
		"url_private":"https://files.slack.com/image.png","alt_txt":"Diagram","original_w":300,"original_h":200
	}}`))
	if err != nil {
		t.Fatal(err)
	}
	if item.ID != "F01234567" || !item.RequiresAuth || item.AltText != "Diagram" || item.Width != 300 || item.Height != 200 {
		t.Fatalf("file details: %+v", item)
	}
	for _, raw := range []string{`{`, `{"ok":true}`, `{"file":{}}`} {
		if _, err := parseFileDetails([]byte(raw)); err == nil {
			t.Errorf("expected error for %s", raw)
		}
	}
	if _, err := parseChannelMessages([]byte(`{`), nil, false); err == nil {
		t.Error("expected malformed message error")
	}
	if _, err := parseSearchMessages([]byte(`{`)); err == nil {
		t.Error("expected malformed search error")
	}
}
