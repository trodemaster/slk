package commands

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/howar31/slk/internal/output"
	"github.com/howar31/slk/internal/resolve"
)

type slackFile struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	Title              string `json:"title"`
	Filetype           string `json:"filetype"`
	Mimetype           string `json:"mimetype"`
	Mode               string `json:"mode"`
	FileAccess         string `json:"file_access"`
	Size               int64  `json:"size"`
	URLPrivate         string `json:"url_private"`
	URLPrivateDownload string `json:"url_private_download"`
	Permalink          string `json:"permalink"`
	ExternalURL        string `json:"external_url"`
	IsExternal         bool   `json:"is_external"`
	OriginalW          int    `json:"original_w"`
	OriginalH          int    `json:"original_h"`
	AltText            string `json:"alt_txt"`
}

type fileItem struct {
	ID           string `json:"id"`
	Name         string `json:"name,omitempty"`
	Title        string `json:"title,omitempty"`
	Filetype     string `json:"filetype,omitempty"`
	Mimetype     string `json:"mimetype,omitempty"`
	Size         int64  `json:"size,omitempty"`
	URL          string `json:"url,omitempty"`
	RequiresAuth bool   `json:"requires_auth"`
	External     bool   `json:"external,omitempty"`
	Width        int    `json:"width,omitempty"`
	Height       int    `json:"height,omitempty"`
	AltText      string `json:"alt_text,omitempty"`
	FileAccess   string `json:"file_access,omitempty"`
}

func (f slackFile) item() fileItem {
	external := f.IsExternal || f.Mode == "external"
	fileURL := f.URLPrivate
	if fileURL == "" {
		fileURL = f.URLPrivateDownload
	}
	if external && f.ExternalURL != "" {
		fileURL = f.ExternalURL
	}
	return fileItem{
		ID: f.ID, Name: f.Name, Title: f.Title, Filetype: f.Filetype,
		Mimetype: f.Mimetype, Size: f.Size, URL: fileURL,
		RequiresAuth: !external, External: external,
		Width: f.OriginalW, Height: f.OriginalH, AltText: f.AltText,
		FileAccess: f.FileAccess,
	}
}

func (f fileItem) Concise() string {
	name := f.Name
	if name == "" {
		name = f.Title
	}
	if name == "" {
		name = "file"
	}
	summary := searchHit{Name: name, ID: f.ID, Extra: f.Filetype}.Concise()
	if f.RequiresAuth {
		if f.ID != "" {
			return summary + " [auth required; slk file download --file " + f.ID + "]"
		}
		if f.URL != "" {
			return summary + " [auth required; slk file download --url " + f.URL + "]"
		}
		return summary + " [auth required; file metadata unavailable]"
	}
	if f.URL != "" {
		return summary + " " + f.URL
	}
	return summary
}

func parseFileDetails(raw []byte) (fileItem, error) {
	var resp struct {
		File slackFile `json:"file"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fileItem{}, fmt.Errorf("parsing file details: %w", err)
	}
	if resp.File.ID == "" {
		return fileItem{}, fmt.Errorf("files.info: missing file ID in response")
	}
	return resp.File.item(), nil
}

type slackImageFile struct {
	ID  string `json:"id"`
	URL string `json:"url"`
}

type slackImageBlock struct {
	Type      string            `json:"type"`
	ImageURL  string            `json:"image_url"`
	AltText   string            `json:"alt_text"`
	SlackFile *slackImageFile   `json:"slack_file"`
	Accessory *slackImageBlock  `json:"accessory"`
	Elements  []slackImageBlock `json:"elements"`
	Blocks    []slackImageBlock `json:"blocks"`
}

type slackAttachment struct {
	ImageURL string            `json:"image_url"`
	ThumbURL string            `json:"thumb_url"`
	Title    string            `json:"title"`
	Fallback string            `json:"fallback"`
	Blocks   []slackImageBlock `json:"blocks"`
}

type imageItem struct {
	FileID       string `json:"file,omitempty"`
	URL          string `json:"url,omitempty"`
	AltText      string `json:"alt_text,omitempty"`
	Source       string `json:"source"`
	RequiresAuth bool   `json:"requires_auth"`
}

func (i imageItem) Concise() string {
	if i.RequiresAuth {
		if i.FileID != "" {
			return "image " + i.FileID + " [auth required; slk file download --file " + i.FileID + "]"
		}
		return "image [auth required; slk file download --url " + i.URL + "]"
	}
	if i.AltText != "" {
		return fmt.Sprintf("image %s (%s)", i.URL, i.AltText)
	}
	return "image " + i.URL
}

var slackFileIDPattern = regexp.MustCompile(`^F[A-Z0-9]+$`)

func imageFileID(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || !isSlackImageURL(rawURL) {
		return ""
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if slackFileIDPattern.MatchString(segment) {
			return segment
		}
		if _, id, ok := strings.Cut(segment, "-"); ok && slackFileIDPattern.MatchString(id) {
			return id
		}
	}
	return ""
}

func isSlackImageURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "slack.com" || strings.HasSuffix(host, ".slack.com")
}

func messageMedia(m slackMessage) ([]fileItem, []imageItem) {
	var files []fileItem
	var images []imageItem
	fileIDs := make(map[string]int)
	fileURLs := make(map[string]int)
	for _, f := range m.Files {
		item := f.item()
		if _, exists := fileIDs[item.ID]; item.ID != "" && exists {
			continue
		}
		if item.ID != "" {
			fileIDs[item.ID] = len(files)
		}
		if item.URL != "" {
			fileURLs[item.URL] = len(files)
		}
		if f.Permalink != "" {
			fileURLs[f.Permalink] = len(files)
		}
		files = append(files, item)
	}
	seenImages := make(map[string]bool)
	add := func(image imageItem) {
		if image.FileID == "" && image.RequiresAuth {
			image.FileID = imageFileID(image.URL)
		}
		index, duplicate := fileIDs[image.FileID]
		if !duplicate && image.URL != "" {
			index, duplicate = fileURLs[image.URL]
		}
		if duplicate {
			if files[index].AltText == "" {
				files[index].AltText = image.AltText
			}
			if files[index].URL == "" {
				files[index].URL = image.URL
			}
			return
		}
		key := "url:" + image.URL
		if image.FileID != "" {
			key = "file:" + image.FileID
		}
		if (image.FileID == "" && image.URL == "") || seenImages[key] {
			return
		}
		seenImages[key] = true
		images = append(images, image)
	}
	var visit func(slackImageBlock)
	visit = func(block slackImageBlock) {
		if block.Type == "image" {
			if block.SlackFile != nil {
				add(imageItem{
					FileID: block.SlackFile.ID, URL: block.SlackFile.URL,
					AltText: block.AltText, Source: "slack_file", RequiresAuth: true,
				})
			} else if block.ImageURL != "" {
				add(imageItem{
					URL: block.ImageURL, AltText: block.AltText,
					Source: "block", RequiresAuth: isSlackImageURL(block.ImageURL),
				})
			}
		}
		if block.Accessory != nil {
			visit(*block.Accessory)
		}
		for _, child := range block.Elements {
			visit(child)
		}
		for _, child := range block.Blocks {
			visit(child)
		}
	}
	for _, block := range m.Blocks {
		visit(block)
	}
	for _, attachment := range m.Attachments {
		alt := attachment.Title
		if alt == "" {
			alt = attachment.Fallback
		}
		for _, imageURL := range []string{attachment.ImageURL, attachment.ThumbURL} {
			if imageURL != "" {
				add(imageItem{URL: imageURL, AltText: alt, Source: "attachment", RequiresAuth: isSlackImageURL(imageURL)})
			}
		}
		for _, block := range attachment.Blocks {
			visit(block)
		}
	}
	return files, images
}

func parseChannelMessages(raw []byte, r *resolve.Resolver, withThread bool) ([]msgItem, error) {
	var resp struct {
		Messages []slackMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	items := make([]msgItem, len(resp.Messages))
	for i, m := range resp.Messages {
		files, images := messageMedia(m)
		items[i] = msgItem{User: messageDisplay(r, m), Text: m.Text, TS: m.TS, Files: files, Images: images}
		if withThread {
			items[i].Thread = m.ThreadTS
		}
	}
	return items, nil
}

func parseSearchMessages(raw []byte) ([]msgItem, error) {
	var resp struct {
		Messages struct {
			Matches []slackMessage `json:"matches"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, err
	}
	items := make([]msgItem, len(resp.Messages.Matches))
	for i, m := range resp.Messages.Matches {
		files, images := messageMedia(m)
		items[i] = msgItem{User: m.Username, Text: m.Text, TS: m.TS, Files: files, Images: images}
	}
	return items, nil
}

func appendMediaSummary(summary string, files []fileItem, images []imageItem) string {
	for _, file := range files {
		summary += " [file: " + file.Concise() + "]"
	}
	for _, image := range images {
		summary += " [" + image.Concise() + "]"
	}
	return summary
}

type mediaHit struct {
	Name   string      `json:"name"`
	ID     string      `json:"id"`
	Extra  string      `json:"extra,omitempty"`
	Files  []fileItem  `json:"files,omitempty"`
	Images []imageItem `json:"images,omitempty"`
}

func (h mediaHit) Concise() string {
	return appendMediaSummary(searchHit{Name: h.Name, ID: h.ID, Extra: h.Extra}.Concise(), h.Files, h.Images)
}

func emitMediaHits(w io.Writer, format string, items []mediaHit) error {
	if format == "table" {
		hasMedia := false
		for _, item := range items {
			hasMedia = hasMedia || len(item.Files) > 0 || len(item.Images) > 0
		}
		if !hasMedia {
			plain := make([]searchHit, len(items))
			for i, item := range items {
				plain[i] = searchHit{Name: item.Name, ID: item.ID, Extra: item.Extra}
			}
			return output.Emit(w, format, plain)
		}
	}
	return output.Emit(w, format, items)
}

func emitMessages(w io.Writer, format string, items []msgItem) error {
	if format == "table" {
		hasMedia := false
		for _, item := range items {
			hasMedia = hasMedia || len(item.Files) > 0 || len(item.Images) > 0
		}
		if !hasMedia {
			plain := make([]struct {
				User   string `json:"user"`
				Text   string `json:"text"`
				TS     string `json:"ts"`
				Thread string `json:"thread,omitempty"`
			}, len(items))
			for i, item := range items {
				plain[i].User, plain[i].Text, plain[i].TS, plain[i].Thread = item.User, item.Text, item.TS, item.Thread
			}
			return output.Emit(w, format, plain)
		}
	}
	return output.Emit(w, format, items)
}
