package bot

import (
	"strings"
	"testing"
)

func TestGetModLogConfigDev(t *testing.T) {
	config := getModLogCfg("dev")
	if config == nil {
		t.Fatal("dev config is nil")
	}
	if config.ChannelID != 1142519200682876938 {
		t.Errorf("ChannelID = %d, want %d", config.ChannelID, 1142519200682876938)
	}
}

func TestGetModLogConfigProd(t *testing.T) {
	config := getModLogCfg("prod")
	if config == nil {
		t.Fatal("prod config is nil")
	}
	if config.ChannelID != 1141477354129080361 {
		t.Errorf("ChannelID = %d, want %d", config.ChannelID, 1141477354129080361)
	}
}

func TestGetModLogConfigUnknown(t *testing.T) {
	if getModLogCfg("staging") != nil {
		t.Error("expected nil for unknown env")
	}
	if getModLogCfg("") != nil {
		t.Error("expected nil for empty env")
	}
}

func TestMessageLink(t *testing.T) {
	got := messageLink("111", "222", "333")
	want := "https://discord.com/channels/111/222/333"
	if got != want {
		t.Errorf("messageLink = %q, want %q", got, want)
	}
}

func TestSplitContent(t *testing.T) {
	// splitContent mimics the loop in handleModLogModalSubmit
	splitContent := func(s string, size int) []string {
		var chunks []string
		for idx := 0; idx < len(s); idx += size {
			end := idx + size
			if end > len(s) {
				end = len(s)
			}
			chunks = append(chunks, s[idx:end])
		}
		return chunks
	}

	t.Run("empty string", func(t *testing.T) {
		chunks := splitContent("", embedSplitSize)
		if len(chunks) != 0 {
			t.Errorf("expected 0 chunks, got %d", len(chunks))
		}
	})

	t.Run("shorter than limit", func(t *testing.T) {
		chunks := splitContent("hello", embedSplitSize)
		if len(chunks) != 1 || chunks[0] != "hello" {
			t.Errorf("expected [hello], got %v", chunks)
		}
	})

	t.Run("exactly at limit", func(t *testing.T) {
		s := strings.Repeat("x", embedSplitSize)
		chunks := splitContent(s, embedSplitSize)
		if len(chunks) != 1 || len(chunks[0]) != embedSplitSize {
			t.Errorf("expected 1 chunk of size %d, got %d chunks", embedSplitSize, len(chunks))
		}
	})

	t.Run("needs two chunks", func(t *testing.T) {
		s := strings.Repeat("a", embedSplitSize+1)
		chunks := splitContent(s, embedSplitSize)
		if len(chunks) != 2 {
			t.Fatalf("expected 2 chunks, got %d", len(chunks))
		}
		if len(chunks[0]) != embedSplitSize {
			t.Errorf("first chunk size = %d, want %d", len(chunks[0]), embedSplitSize)
		}
		if len(chunks[1]) != 1 {
			t.Errorf("second chunk size = %d, want 1", len(chunks[1]))
		}
	})

	t.Run("multiple full chunks", func(t *testing.T) {
		s := strings.Repeat("b", embedSplitSize*3)
		chunks := splitContent(s, embedSplitSize)
		if len(chunks) != 3 {
			t.Fatalf("expected 3 chunks, got %d", len(chunks))
		}
		for i, c := range chunks {
			if len(c) != embedSplitSize {
				t.Errorf("chunk %d size = %d, want %d", i, len(c), embedSplitSize)
			}
		}
	})
}
