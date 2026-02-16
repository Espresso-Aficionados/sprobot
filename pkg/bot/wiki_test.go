package bot

import (
	"strings"
	"testing"

	"github.com/sadbox/sprobot/pkg/sprobot"
)

func TestWikiAutocompleteMatchesShortcut(t *testing.T) {
	choices := wikiAutocomplete("water")

	found := false
	for _, c := range choices {
		if c == "water" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'water' in autocomplete results for 'water', got %v", choices)
	}
}

func TestWikiAutocompleteMatchesHint(t *testing.T) {
	choices := wikiAutocomplete("beans")

	found := false
	for _, c := range choices {
		if c == "coffee-reccomendations" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected 'coffee-reccomendations' via hint 'beans', got %v", choices)
	}
}

func TestWikiAutocompleteMatchesPartial(t *testing.T) {
	choices := wikiAutocomplete("grind")

	if len(choices) == 0 {
		t.Fatal("expected results for 'grind'")
	}
	for _, c := range choices {
		if !strings.Contains(c, "grind") {
			t.Errorf("choice %q doesn't match 'grind'", c)
		}
	}
}

func TestWikiAutocompleteLimitsTo20(t *testing.T) {
	// Empty string matches everything
	choices := wikiAutocomplete("")

	if len(choices) > 20 {
		t.Errorf("autocomplete returned %d choices, max should be 20", len(choices))
	}
}

func TestWikiAutocompleteNoMatch(t *testing.T) {
	choices := wikiAutocomplete("zzzznonexistent")

	if len(choices) != 0 {
		t.Errorf("expected 0 results for nonexistent term, got %d", len(choices))
	}
}

func TestWikiAutocompleteMultipleHints(t *testing.T) {
	// "homepage" has hints ["eaf", "wiki"]
	for _, hint := range []string{"eaf", "wiki"} {
		choices := wikiAutocomplete(hint)
		found := false
		for _, c := range choices {
			if c == "homepage" {
				found = true
			}
		}
		if !found {
			t.Errorf("expected 'homepage' via hint %q, got %v", hint, choices)
		}
	}
}

func TestWikiLinkLookup(t *testing.T) {
	url := wikiLookup("water")
	if url != "https://espressoaf.com/guides/water.html" {
		t.Errorf("wikiLookup(water) = %q, want water URL", url)
	}
}

func TestWikiLinkLookupNotFound(t *testing.T) {
	url := wikiLookup("nonexistent-page")
	if url != "" {
		t.Errorf("wikiLookup(nonexistent) = %q, want empty", url)
	}
}

// wikiAutocomplete extracts the autocomplete logic from handleAutocomplete for testing
func wikiAutocomplete(current string) []string {
	current = strings.ToLower(current)
	var choices []string

	for _, link := range sprobot.WikiLinks {
		if len(choices) >= 20 {
			break
		}

		if strings.Contains(link.Shortcut, current) {
			choices = append(choices, link.Shortcut)
			continue
		}

		for _, hint := range link.Hints {
			if strings.Contains(hint, current) {
				choices = append(choices, link.Shortcut)
				break
			}
		}
	}
	return choices
}

// wikiLookup extracts the lookup logic from handleWiki for testing
func wikiLookup(page string) string {
	for _, link := range sprobot.WikiLinks {
		if link.Shortcut == page {
			return link.URL
		}
	}
	return ""
}
