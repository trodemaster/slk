package commands

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMessage_FlagsAndDryRun(t *testing.T) {
	dir := t.TempDir()
	first, second := filepath.Join(dir, "first,image.png"), filepath.Join(dir, "second.png")
	for _, name := range []string{first, second} {
		if err := os.WriteFile(name, []byte("image fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name string
		args []string
	}{
		{"message", []string{"msg", "send", "--channel", "C0123456789", "--file", first, "--file", second, "--text", "Caption", "--alt-text", "Diagram"}},
		{"image-only", []string{"msg", "send", "--channel", "C0123456789", "--file", first}},
		{"thread", []string{"thread", "reply", "--channel", "C0123456789", "--thread", "1790780000.000001", "--file", first, "--text", "Caption"}},
		{"message-thread", []string{"msg", "send", "--channel", "C0123456789", "--thread", "1790780000.000001", "--file", first}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := NewRootCommand("test")
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(append(tc.args, "--dry-run"))
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"files.getUploadURLExternal", "files.completeUploadExternal", "first,image.png"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q: %s", want, out.String())
				}
			}
			if strings.Contains(tc.name, "thread") && !strings.Contains(out.String(), "1790780000.000001") {
				t.Errorf("missing thread: %s", out.String())
			}
		})
	}
	for _, path := range [][]string{{"msg", "send"}, {"thread", "reply"}} {
		cmd, _, err := NewRootCommand("test").Find(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, flag := range []string{"file", "alt-text"} {
			if cmd.Flags().Lookup(flag) == nil {
				t.Errorf("%v missing --%s", path, flag)
			}
		}
	}
}

func TestFileMessage_InvalidInputs(t *testing.T) {
	file := filepath.Join(t.TempDir(), "image.png")
	if err := os.WriteFile(file, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"msg", "send", "--channel", "C0123456789"},
		{"msg", "send", "--channel", "C0123456789", "--text", "Caption", "--alt-text", "Diagram"},
		{"msg", "send", "--channel", "C0123456789", "--file", file, "--text", "Caption", "--text-file", file},
		{"msg", "send", "--channel", "C0123456789", "--file", file, "--reply-broadcast"},
		{"thread", "reply", "--channel", "C0123456789", "--thread", "1.0"},
		{"thread", "reply", "--channel", "C0123456789", "--thread", "1.0", "--text", "Caption", "--alt-text", "Diagram"},
	} {
		cmd := NewRootCommand("test")
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetArgs(append(args, "--dry-run"))
		if err := cmd.Execute(); err == nil {
			t.Errorf("expected input error: %v", args)
		}
	}
}

func TestReadMessageContent_TextFileCaption(t *testing.T) {
	caption := filepath.Join(t.TempDir(), "caption.txt")
	if err := os.WriteFile(caption, []byte("First line\nSecond line"), 0600); err != nil {
		t.Fatal(err)
	}
	content, err := readMessageContent("", caption, []string{"image.png"})
	if err != nil {
		t.Fatal(err)
	}
	if content != "First line\nSecond line" {
		t.Fatalf("caption = %q", content)
	}
}
