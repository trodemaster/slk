package commands

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func readMessageContent(text, textFile string, files []string) (string, error) {
	if len(files) > 0 && text == "" && textFile == "" {
		return "", nil
	}
	return readContent(text, textFile, "--text", "--text-file")
}

func sendFileMessage(cmd *cobra.Command, g *GlobalFlags, opts fileUploadOptions, summary string) error {
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
		_, err = fmt.Fprintln(cmd.OutOrStdout(), string(raw))
		return err
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s %s\n", summary, strings.Join(ids, " "))
	return err
}
