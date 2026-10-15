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
