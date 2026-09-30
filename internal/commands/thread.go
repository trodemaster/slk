package commands

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newThreadCommand(g *GlobalFlags) *cobra.Command {
	cmd := &cobra.Command{Use: "thread", Short: "Read and reply to threads"}
	cmd.AddCommand(newThreadReadCommand(g), newThreadReplyCommand(g))
	return cmd
}

func newThreadReadCommand(g *GlobalFlags) *cobra.Command {
	var channel, thread, oldest, latest, cursor string
	var limit int
	cmd := &cobra.Command{
		Use:   "read",
		Short: "Read replies in a thread",
		Long: "Read replies and file/image references without downloading anything. " +
			"View accessible URLs directly; for protected Slack images, run file download " +
			"and pass the returned local path to the agent's image viewer.",
		Annotations: map[string]string{
			"slackMethod": "conversations.replies",
			"userScopes":  "channels:history,groups:history,im:history,mpim:history",
			"botScopes":   "channels:history,groups:history,im:history,mpim:history",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			params := map[string]string{
				"channel": channel,
				"ts":      thread,
				"limit":   fmt.Sprintf("%d", limit),
			}
			if oldest != "" {
				params["oldest"] = oldest
			}
			if latest != "" {
				params["latest"] = latest
			}
			if cursor != "" {
				params["cursor"] = cursor
			}
			raw, err := client.Call("conversations.replies", params, nil)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			r := newResolver(g, client)
			items, err := parseChannelMessages(raw, r, false)
			if err != nil {
				return err
			}
			return emitMessages(cmd.OutOrStdout(), g.Format, items)
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "channel ID")
	cmd.Flags().StringVar(&thread, "thread", "", "parent message ts")
	cmd.Flags().IntVar(&limit, "limit", 100, "max replies")
	cmd.Flags().StringVar(&oldest, "oldest", "", "start of time range (ts)")
	cmd.Flags().StringVar(&latest, "latest", "", "end of time range (ts)")
	cmd.Flags().StringVar(&cursor, "cursor", "", "pagination cursor")
	cmd.MarkFlagRequired("channel")
	cmd.MarkFlagRequired("thread")
	return cmd
}

func newThreadReplyCommand(g *GlobalFlags) *cobra.Command {
	var channel, thread, text, textFile, altText string
	var files []string
	cmd := &cobra.Command{
		Use:   "reply",
		Short: "Reply within a thread",
		Long: "Reply with text, local files, or both. Repeat --file to attach multiple files " +
			"to the same reply. With files, text becomes the upload's initial comment.",
		Annotations: map[string]string{
			"slackMethod": "chat.postMessage / files.getUploadURLExternal / files.completeUploadExternal",
			"write":       "true",
			"userScopes":  "chat:write,files:write,im:write",
			"botScopes":   "chat:write,files:write,im:write",
			"botCapable":  "true",
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := readMessageContent(text, textFile, files)
			if err != nil {
				return err
			}
			if len(files) > 0 {
				return sendFileMessage(cmd, g, fileUploadOptions{
					Paths: files, Channel: channel, ThreadTS: thread,
					Comment: content, AltText: altText,
				}, "replied with files")
			}
			if altText != "" {
				return fmt.Errorf("--alt-text requires --file")
			}
			params := map[string]string{"channel": channel, "thread_ts": thread, "text": content}
			if g.DryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "[dry-run] chat.postMessage %v\n", params)
				return nil
			}
			client, err := buildClient(cmd, g)
			if err != nil {
				return err
			}
			raw, err := client.Call("chat.postMessage", params, nil)
			if err != nil {
				return err
			}
			if g.Raw {
				fmt.Fprintln(cmd.OutOrStdout(), string(raw))
				return nil
			}
			fmt.Fprintln(cmd.OutOrStdout(), "replied")
			return nil
		},
	}
	cmd.Flags().StringVar(&channel, "channel", "", "channel ID")
	cmd.Flags().StringVar(&thread, "thread", "", "parent message ts")
	cmd.Flags().StringVar(&text, "text", "", "reply text")
	cmd.Flags().StringVar(&textFile, "text-file", "", "path to text file (use - for stdin)")
	cmd.Flags().StringArrayVar(&files, "file", nil, "local file to attach (repeat for multiple files)")
	cmd.Flags().StringVar(&altText, "alt-text", "", "image description applied to attached files")
	cmd.MarkFlagRequired("channel")
	cmd.MarkFlagRequired("thread")
	return cmd
}
