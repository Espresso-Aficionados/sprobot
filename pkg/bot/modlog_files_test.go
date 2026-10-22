package bot

import (
	"context"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"

	"github.com/sadbox/sprobot/pkg/s3client"
)

func TestAttachmentFieldsPacksUnderLimit(t *testing.T) {
	link := "https://bot.espressoaf.com/files/mod/726985544038612993/abcdefghijABCDEFGHIJ0123456789.png"
	var lines []string
	for range 20 {
		lines = append(lines, link)
	}

	fields := attachmentFields(lines)
	if len(fields) < 2 {
		t.Fatalf("got %d fields, want the links split across several", len(fields))
	}
	var got []string
	for i, f := range fields {
		if len(f.Value) > embedSplitSize {
			t.Errorf("field %d is %d chars, over %d", i, len(f.Value), embedSplitSize)
		}
		wantName := "Attachments"
		if i > 0 {
			wantName = "Attachments (cont.)"
		}
		if f.Name != wantName {
			t.Errorf("field %d name = %q, want %q", i, f.Name, wantName)
		}
		got = append(got, strings.Split(f.Value, "\n")...)
	}
	if len(got) != len(lines) {
		t.Errorf("fields hold %d lines, want %d", len(got), len(lines))
	}
}

func TestAttachmentFieldsSingle(t *testing.T) {
	fields := attachmentFields([]string{"a", "b"})
	if len(fields) != 1 || fields[0].Value != "a\nb" {
		t.Errorf("fields = %+v, want one field %q", fields, "a\nb")
	}
	if attachmentFields(nil) != nil {
		t.Error("no lines should produce no fields")
	}
}

func TestSaveModAttachmentTooLarge(t *testing.T) {
	b := &Bot{} // rejected before any download, so no S3 client is needed
	att := discord.Attachment{Filename: "clip`.mov", Size: s3client.MaxFileSize + 1}

	saved := b.saveModAttachment(context.Background(), "1", att)
	if saved.key != "" {
		t.Fatal("oversized attachment should fail")
	}
	for _, want := range []string{"`clip.mov`", "500.0 MiB", "not saved"} {
		if !strings.Contains(saved.line, want) {
			t.Errorf("line %q missing %q", saved.line, want)
		}
	}
}
