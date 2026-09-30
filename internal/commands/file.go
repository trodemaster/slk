package commands

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/howar31/slk/internal/output"
	"github.com/spf13/cobra"
)

func newFileCommand(g *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "file", Short: "List and manage files"}
	cmd.AddCommand(
		newFileListCommand(g),
		newFileInfoCommand(g),
		newFileDownloadCommand(g),
		newFileUploadCommand(g),
		newFileDeleteCommand(g),
		newFilePublicCommand(g),
		newFileRevokePublicCommand(g),
	)
	return cmd
}

// newFileListCommand returns a command that pages through files.list and
// returns trimmed file hits. --raw is not offered here: a multi-page response
// has no single raw envelope.
func newFileListCommand(g *GlobalFlags) *cobra.Command {
	var channel, user, types string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List files",
		// --raw is not offered here: a multi-page response has no single raw envelope.
		Annotations: map[string]string{
			"slackMethod": "files.list",
			"userScopes":  "files:read",
			"botScopes":   "files:read",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			hits, err := fileFetchList(client, channel, user, types)
			if err != nil {
				return err
			}
			return output.Emit(cmd.OutOrStdout(), g.Format, hits)
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "filter by channel ID (optional)")
	cmd.Flags().StringVar(&user, "user", "", "filter by user ID (optional)")
	cmd.Flags().StringVar(&types, "types", "", "filter by file types, comma-separated (optional)")
	return cmd
}

// fileFetchList pages through files.list (page-based) and returns trimmed hits.
// Stops after 10 pages.
func fileFetchList(client interface {
	Call(string, map[string]string, []byte) ([]byte, error)
}, channel, user, types string) ([]searchHit, error) {
	params := map[string]string{"page": "1"}
	if channel != "" {
		params["channel"] = channel
	}
	if user != "" {
		params["user"] = user
	}
	if types != "" {
		params["types"] = types
	}

	var hits []searchHit
	const maxPages = 10
	for page := 1; page <= maxPages; page++ {
		params["page"] = fmt.Sprint(page)
		raw, err := client.Call("files.list", params, nil)
		if err != nil {
			return hits, err
		}
		var resp struct {
			Files []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				Filetype string `json:"filetype"`
			} `json:"files"`
			Paging struct {
				Page  int `json:"page"`
				Pages int `json:"pages"`
			} `json:"paging"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return hits, err
		}
		for _, f := range resp.Files {
			hits = append(hits, searchHit{Name: f.Name, ID: f.ID, Extra: f.Filetype})
		}
		if resp.Paging.Page >= resp.Paging.Pages {
			break
		}
	}
	return hits, nil
}

// parseFileInfo extracts the file object from a files.info raw response and
// returns a single searchHit.
func parseFileInfo(raw []byte) (searchHit, error) {
	var resp struct {
		File struct {
			ID       string `json:"id"`
			Name     string `json:"name"`
			Filetype string `json:"filetype"`
		} `json:"file"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return searchHit{}, err
	}
	return searchHit{Name: resp.File.Name, ID: resp.File.ID, Extra: resp.File.Filetype}, nil
}

func newFileInfoCommand(g *GlobalFlags) *cobra.Command {
	var fileID string
	cmd := &cobra.Command{
		Use:   "info",
		Short: "Show file details",
		Long: `Show file metadata, the best available URL, and whether that URL requires
Slack authentication. No file bytes are fetched or saved.

Start with file info (or the file metadata from msg read / thread read) to inspect
image attachments. Use file download only when local bytes are needed.
--raw returns the original files.info response.`,
		Annotations: map[string]string{
			"slackMethod": "files.info",
			"userScopes":  "files:read",
			"botScopes":   "files:read",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			raw, err := client.Call("files.info", map[string]string{"file": fileID}, nil)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			item, err := parseFileDetails(raw)
			if err != nil {
				return err
			}
			return output.Emit(cmd.OutOrStdout(), g.Format, []fileItem{item})
		},
	}
	cmd.Flags().StringVar(&fileID, "file", "", "file ID")
	cmd.MarkFlagRequired("file")
	return cmd
}

func newFileUploadCommand(g *GlobalFlags) *cobra.Command {
	var paths []string
	var channel, title, text, textFile, threadTS, altText string
	cmd := &cobra.Command{
		Use:   "upload",
		Short: "Upload files, optionally sharing them to a channel or DM",
		Long: `Upload nonempty, readable regular files using Slack's hosted upload flow.
Repeat --file to upload multiple files and share them together in one completion.
--title is supported only with a single file; otherwise each title defaults to
its filename. --alt-text applies the same image description to every file.

Omit --channel for a private upload, or pass a channel ID or U/W user ID for a DM.
--text or --text-file supplies an initial comment; --text-file - reads stdin.
An initial comment or --thread requires --channel.

--dry-run validates all local files and prints the actual getUploadURLExternal,
raw byte POST, and completeUploadExternal plan without resolving a token or
making network calls. --raw returns the final completion response. Partial
failures report allocated file IDs; completion is never automatically retried.`,
		Annotations: map[string]string{
			"slackMethod": "files.getUploadURLExternal",
			"write":       "true",
			"userScopes":  "files:write,im:write",
			"botScopes":   "files:write,im:write",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.Flags().Changed("text") && cmd.Flags().Changed("text-file") {
				return fmt.Errorf("specify --text or --text-file, not both")
			}
			if channel == "" && (cmd.Flags().Changed("text") || cmd.Flags().Changed("text-file") || threadTS != "") {
				return fmt.Errorf("--text, --text-file and --thread require --channel")
			}
			opts := fileUploadOptions{
				Paths: paths, Channel: channel, ThreadTS: threadTS,
				Title: title, Comment: text, AltText: altText,
			}
			if err := validateFileUploadOptions(opts); err != nil {
				return err
			}
			if textFile != "" {
				var err error
				opts.Comment, err = readContent("", textFile, "--text", "--text-file")
				if err != nil {
					return err
				}
			}
			if g.DryRun {
				return uploadDryRun(cmd.OutOrStdout(), opts)
			}
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			raw, ids, err := uploadFiles(client, opts)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			fmt.Fprintf(cmd.OutOrStdout(), "uploaded %s\n", strings.Join(ids, ", "))
			return nil
		},
	}
	cmd.Flags().StringArrayVar(&paths, "file", nil, "path to file to upload (repeatable)")
	cmd.Flags().StringVar(&channel, "channel", "", "channel ID or user ID for a DM (omit for a private upload)")
	cmd.Flags().StringVar(&title, "title", "", "file title (single file only; defaults to filename)")
	cmd.Flags().StringVar(&text, "text", "", "initial comment when sharing files")
	cmd.Flags().StringVar(&textFile, "text-file", "", "read initial comment from a file ('-' for stdin)")
	cmd.Flags().StringVar(&threadTS, "thread", "", "share files in this thread (requires --channel)")
	cmd.Flags().StringVar(&altText, "alt-text", "", "image description applied to each uploaded file")
	cmd.MarkFlagRequired("file")
	return cmd
}

func newFileDeleteCommand(g *GlobalFlags) *cobra.Command {
	var fileID string
	cmd := &cobra.Command{
		Use:   "delete",
		Short: "Delete a file",
		Annotations: map[string]string{
			"slackMethod": "files.delete",
			"write":       "true",
			"userScopes":  "files:write",
			"botScopes":   "files:write",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]string{"file": fileID}
			if g.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] files.delete %v\n", params)
				return nil
			}
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			raw, err := client.Call("files.delete", params, nil)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "deleted")
			return nil
		},
	}
	cmd.Flags().StringVar(&fileID, "file", "", "file ID")
	cmd.MarkFlagRequired("file")
	return cmd
}

func newFilePublicCommand(g *GlobalFlags) *cobra.Command {
	var fileID string
	cmd := &cobra.Command{
		Use:   "public",
		Short: "Make a file publicly accessible",
		Long:  "Makes the file accessible to anyone with the link.",
		Annotations: map[string]string{
			"slackMethod": "files.sharedPublicURL",
			"write":       "true",
			"userScopes":  "files:write",
			"botScopes":   "",
			"botCapable":  "false",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]string{"file": fileID}
			if g.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] files.sharedPublicURL %v\n", params)
				return nil
			}
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			raw, err := client.Call("files.sharedPublicURL", params, nil)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "made public")
			return nil
		},
	}
	cmd.Flags().StringVar(&fileID, "file", "", "file ID")
	cmd.MarkFlagRequired("file")
	return cmd
}

func newFileRevokePublicCommand(g *GlobalFlags) *cobra.Command {
	var fileID string
	cmd := &cobra.Command{
		Use:   "revoke-public",
		Short: "Revoke a file's public link",
		Annotations: map[string]string{
			"slackMethod": "files.revokePublicURL",
			"write":       "true",
			"userScopes":  "files:write",
			"botScopes":   "",
			"botCapable":  "false",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			params := map[string]string{"file": fileID}
			if g.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] files.revokePublicURL %v\n", params)
				return nil
			}
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			raw, err := client.Call("files.revokePublicURL", params, nil)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "public access revoked")
			return nil
		},
	}
	cmd.Flags().StringVar(&fileID, "file", "", "file ID")
	cmd.MarkFlagRequired("file")
	return cmd
}
