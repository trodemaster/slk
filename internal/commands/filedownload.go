package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/howar31/slk/internal/api"
	"github.com/howar31/slk/internal/output"
	"github.com/spf13/cobra"
)

const defaultFileDownloadMaxBytes int64 = 25 * 1024 * 1024

type fileDownloadResult struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mimetype string `json:"mimetype"`
	Size     int64  `json:"size"`
	Path     string `json:"path"`
}

func (r fileDownloadResult) Concise() string { return r.Path }

func newFileDownloadCommand(g *GlobalFlags) *cobra.Command {
	var fileID, directURL, destination string
	var maxBytes int64
	cmd := &cobra.Command{
		Use:   "download",
		Short: "Download an authenticated Slack file to a private local path",
		Long: "Download a Slack file without printing binary data. With no --output, create a private " +
			"temporary directory and print the absolute path. The downloaded file remains until you explicitly remove it. " +
			"--url accepts only a known files.slack.com image URL; URLs and credentials are never printed.",
		Args: cobra.NoArgs,
		Annotations: map[string]string{
			"slackMethod": "files.info",
			"userScopes":  "files:read",
			"botScopes":   "files:read",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if g.Raw {
				return errors.New("file download: --raw is not supported; binary output is not allowed")
			}
			if maxBytes <= 0 {
				return errors.New("file download: --max-bytes must be positive")
			}
			if (strings.TrimSpace(fileID) == "") == (strings.TrimSpace(directURL) == "") {
				return errors.New("file download: provide exactly one of --file or --url")
			}
			switch g.Format {
			case "", "concise", "json", "jsonl", "table":
			default:
				return errors.New("file download: unsupported output format")
			}
			if directURL != "" {
				if err := api.New("").ValidateDownloadURL(directURL); err != nil {
					return err
				}
			}
			if g.DryRun {
				target := "a private temporary file"
				if destination != "" {
					target = fmt.Sprintf("%q (no overwrite)", destination)
				}
				source := "known Slack-hosted image URL"
				if fileID != "" {
					source = fmt.Sprintf("files.info file=%s, then authenticated download", fileID)
				}
				_, err := fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] %s -> %s; max-bytes=%d\n", source, target, maxBytes)
				return err
			}
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			result, err := fileDownload(cmd.Context(), client, fileID, directURL, destination, maxBytes)
			if err != nil {
				return err
			}
			return output.Emit(cmd.OutOrStdout(), g.Format, []fileDownloadResult{result})
		},
	}
	cmd.Flags().StringVar(&fileID, "file", "", "Slack file ID")
	cmd.Flags().StringVar(&directURL, "url", "", "known HTTPS files.slack.com protected image URL (alternative to --file)")
	cmd.Flags().StringVar(&destination, "output", "", "destination file path; existing targets and file symlinks are rejected")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", defaultFileDownloadMaxBytes, "maximum downloaded bytes (must be positive)")
	cmd.MarkFlagsMutuallyExclusive("file", "url")
	cmd.MarkFlagsOneRequired("file", "url")
	return cmd
}

func fileDownload(ctx context.Context, client *api.Client, fileID, directURL, destination string, maxBytes int64) (result fileDownloadResult, err error) {
	if maxBytes <= 0 {
		return result, errors.New("file download: --max-bytes must be positive")
	}
	if (fileID == "") == (directURL == "") {
		return result, errors.New("file download: provide exactly one of --file or --url")
	}
	opts := api.DownloadOptions{MaxBytes: maxBytes}
	source := directURL
	if fileID != "" {
		raw, callErr := client.Call("files.info", map[string]string{"file": fileID}, nil)
		if callErr != nil {
			return result, callErr
		}
		var envelope struct {
			File slackFile `json:"file"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return result, errors.New("file download: invalid files.info response")
		}
		file := envelope.File
		if file.ID == "" {
			return result, errors.New("file download: files.info returned no file metadata")
		}
		if file.IsExternal || file.Mode == "external" {
			return result, errors.New("file download: external files are not Slack-hosted; download from the original provider without a Slack bearer token")
		}
		if file.FileAccess != "" && file.FileAccess != "visible" {
			return result, errors.New("file download: file access is restricted; no downloadable content is available")
		}
		source = file.URLPrivateDownload
		if source == "" {
			source = file.URLPrivate
		}
		if source == "" {
			return result, errors.New("file download: file has no private download URL (missing URL or insufficient file access)")
		}
		result.ID, result.Name = file.ID, file.Name
		if result.Name == "" {
			result.Name = file.Title
		}
		opts.ExpectedSize, opts.ExpectedMIME = file.Size, file.Mimetype
		imageMIME := fileDownloadImageMIME(file.Filetype)
		if imageMIME != "" {
			if opts.ExpectedMIME != "" && !strings.HasPrefix(strings.ToLower(opts.ExpectedMIME), "image/") {
				return result, errors.New("file download: image file type conflicts with metadata MIME")
			}
			if opts.ExpectedMIME == "" {
				opts.ExpectedMIME = imageMIME
			}
		}
	} else {
		result.Name = "image"
		opts.ExpectedMIME = "image/*"
	}
	if opts.ExpectedSize < 0 || opts.ExpectedSize > maxBytes {
		return result, errors.New("file download: metadata size is invalid or exceeds --max-bytes")
	}
	if err := client.ValidateDownloadURL(source); err != nil {
		return result, err
	}
	var tempDir string
	if destination == "" {
		tempDir, err = os.MkdirTemp("", "slk-download-")
		if err != nil {
			return result, fmt.Errorf("file download: create private directory: %w", err)
		}
		defer func() {
			if err != nil {
				_ = os.Remove(tempDir)
			}
		}()
		if err = os.Chmod(tempDir, 0700); err != nil {
			return result, fmt.Errorf("file download: secure private directory: %w", err)
		}
		resolvedDir, resolveErr := filepath.EvalSymlinks(tempDir)
		err = resolveErr
		if err != nil {
			return result, fmt.Errorf("file download: resolve private directory: %w", err)
		}
		tempDir = resolvedDir
		destination = filepath.Join(tempDir, fileDownloadSafeName(result.Name))
	}
	absolute, err := filepath.Abs(destination)
	if err != nil {
		return result, fmt.Errorf("file download: resolve output path: %w", err)
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return result, fmt.Errorf("file download: resolve output directory: %w", err)
	}
	absolute = filepath.Join(parent, filepath.Base(absolute))
	root, err := os.OpenRoot(parent)
	if err != nil {
		return result, fmt.Errorf("file download: open output directory: %w", err)
	}
	defer root.Close()
	base := filepath.Base(absolute)
	f, err := root.OpenFile(base, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return result, fmt.Errorf("file download: create output (existing paths and symlinks are not allowed): %w", err)
	}
	created, statErr := f.Stat()
	defer func() {
		f.Close()
		if err != nil {
			current, currentErr := root.Lstat(base)
			if statErr == nil && currentErr == nil && os.SameFile(created, current) {
				_ = root.Remove(base)
			}
		}
	}()
	if err = f.Chmod(0600); err != nil {
		return result, fmt.Errorf("file download: secure output file: %w", err)
	}
	downloaded, err := client.Download(ctx, source, f, opts)
	if err != nil {
		return result, err
	}
	if err = f.Close(); err != nil {
		return result, fmt.Errorf("file download: close output file: %w", err)
	}
	result.Path, result.Size, result.Mimetype = absolute, downloaded.Size, downloaded.Mimetype
	if result.Name == "" {
		result.Name = base
	}
	return result, nil
}

func fileDownloadSafeName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, name)
	if len(name) > 200 {
		name = name[:200]
	}
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}

func fileDownloadImageMIME(filetype string) string {
	switch strings.ToLower(filetype) {
	case "png":
		return "image/png"
	case "jpg", "jpeg":
		return "image/jpeg"
	case "gif", "webp", "bmp", "tiff", "avif", "heic", "heif":
		return "image/" + strings.ToLower(filetype)
	case "svg":
		return "image/svg+xml"
	case "ico":
		return "image/x-icon"
	default:
		return ""
	}
}
