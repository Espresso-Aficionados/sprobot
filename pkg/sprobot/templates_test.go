package sprobot

import (
	"encoding/json"
	"testing"
)

func TestProfileTemplate(t *testing.T) {
	if ProfileTemplate.Name != "Coffee Setup" {
		t.Errorf("ProfileTemplate.Name = %q, want %q", ProfileTemplate.Name, "Coffee Setup")
	}
	if ProfileTemplate.ShortName != "profile" {
		t.Errorf("ProfileTemplate.ShortName = %q, want %q", ProfileTemplate.ShortName, "profile")
	}
	if len(ProfileTemplate.Fields) != 4 {
		t.Errorf("len(ProfileTemplate.Fields) = %d, want 4", len(ProfileTemplate.Fields))
	}
	if ProfileTemplate.Image.Name != "Gear Picture" {
		t.Errorf("ProfileTemplate.Image.Name = %q, want %q", ProfileTemplate.Image.Name, "Gear Picture")
	}

	expectedFields := []string{"Machine", "Grinder", "Favorite Beans", "Location"}
	for i, want := range expectedFields {
		if ProfileTemplate.Fields[i].Name != want {
			t.Errorf("ProfileTemplate.Fields[%d].Name = %q, want %q", i, ProfileTemplate.Fields[i].Name, want)
		}
	}

	// Verify styles (first 3 long, last 1 short)
	for i := 0; i < 3; i++ {
		if ProfileTemplate.Fields[i].Style != TextStyleLong {
			t.Errorf("ProfileTemplate.Fields[%d].Style = %d, want TextStyleLong", i, ProfileTemplate.Fields[i].Style)
		}
	}
	if ProfileTemplate.Fields[3].Style != TextStyleShort {
		t.Errorf("ProfileTemplate.Fields[3].Style = %d, want TextStyleShort", ProfileTemplate.Fields[3].Style)
	}
}

func TestRoasterTemplate(t *testing.T) {
	if RoasterTemplate.Name != "Roasting Setup" {
		t.Errorf("RoasterTemplate.Name = %q, want %q", RoasterTemplate.Name, "Roasting Setup")
	}
	if RoasterTemplate.ShortName != "roaster" {
		t.Errorf("RoasterTemplate.ShortName = %q, want %q", RoasterTemplate.ShortName, "roaster")
	}
	if len(RoasterTemplate.Fields) != 4 {
		t.Errorf("len(RoasterTemplate.Fields) = %d, want 4", len(RoasterTemplate.Fields))
	}
}

func TestAllTemplatesDev(t *testing.T) {
	templates := AllTemplates("dev")
	if templates == nil {
		t.Fatal("AllTemplates(dev) returned nil")
	}
	tmpls, ok := templates[1013566342345019512]
	if !ok {
		t.Fatal("dev guild ID not found")
	}
	if len(tmpls) != 2 {
		t.Errorf("expected 2 templates for dev guild, got %d", len(tmpls))
	}
	if tmpls[0].ShortName != "profile" {
		t.Errorf("first template should be profile, got %q", tmpls[0].ShortName)
	}
	if tmpls[1].ShortName != "roaster" {
		t.Errorf("second template should be roaster, got %q", tmpls[1].ShortName)
	}
}

func TestAllTemplatesProd(t *testing.T) {
	templates := AllTemplates("prod")
	if templates == nil {
		t.Fatal("AllTemplates(prod) returned nil")
	}
	tmpls, ok := templates[726985544038612993]
	if !ok {
		t.Fatal("prod guild ID not found")
	}
	if len(tmpls) != 2 {
		t.Errorf("expected 2 templates for prod guild, got %d", len(tmpls))
	}
}

func TestAllTemplatesUnknownEnv(t *testing.T) {
	if AllTemplates("staging") != nil {
		t.Error("AllTemplates(staging) should return nil")
	}
	if AllTemplates("") != nil {
		t.Error("AllTemplates('') should return nil")
	}
}

func TestTemplateFieldLimit(t *testing.T) {
	// Discord modals support at most 5 top-level components. Each text field
	// and the image upload each consume one slot, so Fields + 1 must be <= 5.
	const maxComponents = 5
	for _, tmpl := range []Template{ProfileTemplate, RoasterTemplate} {
		total := len(tmpl.Fields) + 1 // +1 for the image field
		if total > maxComponents {
			t.Errorf("template %q has %d fields + 1 image = %d components, max is %d",
				tmpl.Name, len(tmpl.Fields), total, maxComponents)
		}
	}
}

func TestDevAndProdGuildsDiffer(t *testing.T) {
	dev := AllTemplates("dev")
	prod := AllTemplates("prod")

	for guildID := range dev {
		if _, ok := prod[guildID]; ok {
			t.Errorf("guild ID %d appears in both dev and prod", guildID)
		}
	}
}

func TestTemplateJSONRoundTrip(t *testing.T) {
	original := []Template{ProfileTemplate, RoasterTemplate}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded []Template
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(decoded) != len(original) {
		t.Fatalf("decoded %d templates, want %d", len(decoded), len(original))
	}

	for i, tmpl := range decoded {
		orig := original[i]
		if tmpl.Name != orig.Name {
			t.Errorf("[%d] Name = %q, want %q", i, tmpl.Name, orig.Name)
		}
		if tmpl.ShortName != orig.ShortName {
			t.Errorf("[%d] ShortName = %q, want %q", i, tmpl.ShortName, orig.ShortName)
		}
		if tmpl.Description != orig.Description {
			t.Errorf("[%d] Description = %q, want %q", i, tmpl.Description, orig.Description)
		}
		if len(tmpl.Fields) != len(orig.Fields) {
			t.Errorf("[%d] Fields count = %d, want %d", i, len(tmpl.Fields), len(orig.Fields))
			continue
		}
		for j, f := range tmpl.Fields {
			if f.Name != orig.Fields[j].Name {
				t.Errorf("[%d] Fields[%d].Name = %q, want %q", i, j, f.Name, orig.Fields[j].Name)
			}
			if f.Placeholder != orig.Fields[j].Placeholder {
				t.Errorf("[%d] Fields[%d].Placeholder = %q, want %q", i, j, f.Placeholder, orig.Fields[j].Placeholder)
			}
			if f.Style != orig.Fields[j].Style {
				t.Errorf("[%d] Fields[%d].Style = %d, want %d", i, j, f.Style, orig.Fields[j].Style)
			}
		}
		if tmpl.Image.Name != orig.Image.Name {
			t.Errorf("[%d] Image.Name = %q, want %q", i, tmpl.Image.Name, orig.Image.Name)
		}
	}
}

func TestFieldJSONTags(t *testing.T) {
	f := Field{Name: "Machine", Placeholder: "Describe your machine", Style: TextStyleLong}
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	s := string(data)
	for _, want := range []string{`"name"`, `"placeholder"`, `"style"`} {
		if !containsString(s, want) {
			t.Errorf("JSON %q missing key %s", s, want)
		}
	}
}

func containsString(haystack, needle string) bool {
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
