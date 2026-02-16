package sprobot

import (
	"strings"
	"testing"
)

func TestWikiLinksNotEmpty(t *testing.T) {
	if len(WikiLinks) == 0 {
		t.Fatal("WikiLinks is empty")
	}
}

func TestWikiLinksCount(t *testing.T) {
	// Should match the Python wiki_links count (39)
	if len(WikiLinks) != 39 {
		t.Errorf("expected 39 wiki links, got %d", len(WikiLinks))
	}
}

func TestWikiLinksHaveValidURLs(t *testing.T) {
	for _, link := range WikiLinks {
		if link.Shortcut == "" {
			t.Error("found a wiki link with empty shortcut")
		}
		if link.URL == "" {
			t.Errorf("wiki link %q has empty URL", link.Shortcut)
		}
		if !strings.HasPrefix(link.URL, "https://") {
			t.Errorf("wiki link %q URL %q doesn't start with https://", link.Shortcut, link.URL)
		}
	}
}

func TestWikiLinksUniqueShortcuts(t *testing.T) {
	seen := make(map[string]bool)
	for _, link := range WikiLinks {
		if seen[link.Shortcut] {
			t.Errorf("duplicate wiki shortcut: %q", link.Shortcut)
		}
		seen[link.Shortcut] = true
	}
}

func TestWikiLinksWithHints(t *testing.T) {
	// Verify specific links have the expected hints
	hintsExpected := map[string][]string{
		"coffee-reccomendations": {"beans"},
		"coffee-scales":          {"weight"},
		"puck-prep":              {"wdt"},
		"espresso-profiling":     {"profiles"},
		"homepage":               {"eaf", "wiki"},
	}

	for _, link := range WikiLinks {
		if expected, ok := hintsExpected[link.Shortcut]; ok {
			if len(link.Hints) != len(expected) {
				t.Errorf("link %q has %d hints, want %d", link.Shortcut, len(link.Hints), len(expected))
				continue
			}
			for i, hint := range expected {
				if link.Hints[i] != hint {
					t.Errorf("link %q hint[%d] = %q, want %q", link.Shortcut, i, link.Hints[i], hint)
				}
			}
		}
	}
}

func TestHomepageLinkExists(t *testing.T) {
	for _, link := range WikiLinks {
		if link.Shortcut == "homepage" {
			if link.URL != "https://espressoaf.com" {
				t.Errorf("homepage URL = %q, want %q", link.URL, "https://espressoaf.com")
			}
			return
		}
	}
	t.Error("homepage link not found in WikiLinks")
}
