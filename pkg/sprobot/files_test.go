package sprobot

import "testing"

const testName = "abcdefghijABCDEFGHIJ0123456789"

func TestFileKeyRoundTrip(t *testing.T) {
	for _, name := range []string{testName, testName + ".png", testName + "..jpg", testName + ".MOV"} {
		key, ok := FileKey("mod", "123", name)
		if !ok {
			t.Errorf("FileKey(mod, 123, %q) rejected", name)
			continue
		}
		kind, guildID, got, ok := ParseFileKey(key)
		if !ok || kind != "mod" || guildID != "123" || got != name {
			t.Errorf("ParseFileKey(%q) = %q, %q, %q, %v", key, kind, guildID, got, ok)
		}
	}
}

func TestFileKeyRejects(t *testing.T) {
	tests := []struct{ kind, guildID, name string }{
		{"images", "123", testName},
		{"mod", "12a", testName},
		{"mod", "", testName},
		{"mod", "123", "short.png"},
		{"mod", "123", testName + "x"},
		{"mod", "123", testName + "/../x"},
		{"mod", "123", testName + "...png"},
		{"mod", "123", testName + ".png.exe"},
	}
	for _, tt := range tests {
		if key, ok := FileKey(tt.kind, tt.guildID, tt.name); ok {
			t.Errorf("FileKey(%q, %q, %q) = %q, want rejected", tt.kind, tt.guildID, tt.name, key)
		}
	}
	for _, key := range []string{"mod_files/123", "mod_files/123/" + testName + "/x", "profiles/123/" + testName} {
		if _, _, _, ok := ParseFileKey(key); ok {
			t.Errorf("ParseFileKey(%q) accepted", key)
		}
	}
}

func TestParseProfileImageKey(t *testing.T) {
	g, tmpl, u, ok := ParseProfileImageKey("images/1/Coffee Setup/2.jpg")
	if !ok || g != "1" || tmpl != "Coffee Setup" || u != "2" {
		t.Errorf("ParseProfileImageKey = %q %q %q %v", g, tmpl, u, ok)
	}
	for _, key := range []string{
		"images/1/Coffee Setup", "images/1//2.jpg", "images/x/Coffee Setup/2.jpg",
		"images/1/Coffee Setup/abc.jpg", "mod_files/1/Coffee Setup/2.jpg", "images/1/a/b/2.jpg",
	} {
		if _, _, _, ok := ParseProfileImageKey(key); ok {
			t.Errorf("ParseProfileImageKey(%q) accepted", key)
		}
	}
}

func TestProfileImageS3PathRoundTrip(t *testing.T) {
	key := ProfileImageS3Path("1", "Coffee Setup", "2", ".png")
	if key != "images/1/Coffee Setup/2.png" {
		t.Errorf("ProfileImageS3Path = %q", key)
	}
	if g, tmpl, u, ok := ParseProfileImageKey(key); !ok || g != "1" || tmpl != "Coffee Setup" || u != "2" {
		t.Errorf("ParseProfileImageKey(%q) = %q %q %q %v", key, g, tmpl, u, ok)
	}
}

func TestValidID(t *testing.T) {
	for s, want := range map[string]bool{
		"1": true, "726985544038612993": true, "12345678901234567890": true,
		"": false, "123456789012345678901": false, "12a": false, "..": false, "1/2": false, " 1": false,
	} {
		if got := ValidID(s); got != want {
			t.Errorf("ValidID(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestFilePrefix(t *testing.T) {
	for kind, want := range map[string]string{
		FileKindMod: "mod_files", FileKindShortcut: "shortcut_files", FileKindSticky: "sticky_files",
	} {
		if got, ok := FilePrefix(kind); !ok || got != want {
			t.Errorf("FilePrefix(%q) = %q, %v; want %q", kind, got, ok, want)
		}
	}
	if _, ok := FilePrefix("images"); ok {
		t.Error("FilePrefix(images) should be unknown")
	}
}

func TestSafeFileExt(t *testing.T) {
	for ext, want := range map[string]string{
		".png": ".png", ".JPEG": ".JPEG", "": "", ".": "", ".tar.gz": "", ".a b": "", ".verylongextension": "",
	} {
		if got := SafeFileExt(ext); got != want {
			t.Errorf("SafeFileExt(%q) = %q, want %q", ext, got, want)
		}
	}
}

func TestInlineContentType(t *testing.T) {
	for name, want := range map[string]string{
		"x.png": "image/png", "x..JPG": "image/jpeg", "x.mov": "video/quicktime",
		"x.svg": "", "x.html": "", "x.pdf": "", "x": "",
	} {
		if got := InlineContentType(name); got != want {
			t.Errorf("InlineContentType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	for n, want := range map[int64]string{
		0: "0 B", 1023: "1023 B", 1024: "1.0 KiB", 1536: "1.5 KiB", 500 << 20: "500.0 MiB", 3 << 30: "3.0 GiB",
	} {
		if got := FormatBytes(n); got != want {
			t.Errorf("FormatBytes(%d) = %q, want %q", n, got, want)
		}
	}
}
