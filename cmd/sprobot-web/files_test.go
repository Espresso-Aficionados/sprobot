package main

import (
	"net/http"
	"strings"
	"testing"
)

const testFileName = "abcdefghijABCDEFGHIJ0123456789"

func TestFileRedirectsToViewer(t *testing.T) {
	_, mux := newTestServer(t)
	tests := []struct {
		target, ua string
	}{
		{"/files/mod/1/" + testFileName + ".png", "Mozilla/5.0"},
		// Mod files never serve bytes, even to Discord.
		{"/files/mod/1/" + testFileName + ".png", discordUA},
		// Non-images never serve bytes, even for embeddable kinds.
		{"/files/sticky/1/" + testFileName + ".mp4", discordUA},
		{"/files/shortcut/1/" + testFileName + ".svg", discordUA},
		{"/files/shortcut/1/" + testFileName + ".png", "curl/8.0"},
	}
	for _, tt := range tests {
		rec := get(mux, tt.target, tt.ua)
		if rec.Code != http.StatusFound {
			t.Errorf("%s (%s): status = %d, want 302", tt.target, tt.ua, rec.Code)
			continue
		}
		if loc := rec.Header().Get("Location"); loc != tt.target+"/view" {
			t.Errorf("%s: Location = %q, want %q", tt.target, loc, tt.target+"/view")
		}
		if rec.Header().Get("Vary") != "User-Agent" {
			t.Errorf("%s: missing Vary: User-Agent", tt.target)
		}
	}
}

func TestFileServesImageToDiscord(t *testing.T) {
	fake, mux := newTestServer(t)
	img := []byte("\x89PNG\r\n\x1a\nfake")
	putObject(fake, "shortcut_files/1/"+testFileName+".png", img)

	rec := get(mux, "/files/shortcut/1/"+testFileName+".png", discordUA)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "sandbox") {
		t.Errorf("CSP = %q, want sandbox", csp)
	}
	if rec.Body.String() != string(img) {
		t.Error("body should be the stored bytes")
	}
}

func TestFileRejectsBadPaths(t *testing.T) {
	_, mux := newTestServer(t)
	for _, target := range []string{
		"/files/images/1/" + testFileName + ".png",   // not a file kind
		"/files/mod/abc/" + testFileName + ".png",    // guild ID not numeric
		"/files/mod/1/short.png",                     // name not 30 random chars
		"/files/mod/1/" + testFileName + ".png/view", // no such object
		"/files/mod/1/" + testFileName + "%2F..%2Fx/view",
	} {
		if rec := get(mux, target, discordUA); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", target, rec.Code)
		}
	}
}

func TestFileViewer(t *testing.T) {
	fake, mux := newTestServer(t)
	putObject(fake, "mod_files/1/"+testFileName+".png", []byte("img"))
	putObject(fake, "mod_files/1/"+testFileName+"..mov", []byte("video"))
	putObject(fake, "mod_files/1/"+testFileName+".pdf", []byte("pdf"))

	tests := []struct {
		name       string
		want       []string
		notWant    []string
		inlineType string
	}{
		{testFileName + ".png", []string{"<img src=", "3 B"}, []string{"<video"}, "image%2Fpng"},
		{testFileName + "..mov", []string{"<video src=", "download it instead"}, []string{`<img src="http`}, "video%2Fquicktime"},
		{testFileName + ".pdf", []string{"Download"}, []string{"<video", `<img src="http`}, ""},
	}
	for _, tt := range tests {
		rec := get(mux, "/files/mod/1/"+tt.name+"/view", "Mozilla/5.0")
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", tt.name, rec.Code)
			continue
		}
		body := rec.Body.String()
		for _, w := range append(tt.want, "response-content-disposition=attachment", `<meta name="robots" content="noindex, nofollow">`) {
			if !strings.Contains(body, w) {
				t.Errorf("%s: page missing %q", tt.name, w)
			}
		}
		for _, w := range tt.notWant {
			if strings.Contains(body, w) {
				t.Errorf("%s: page should not contain %q", tt.name, w)
			}
		}
		if tt.inlineType != "" && !strings.Contains(body, "response-content-type="+tt.inlineType) {
			t.Errorf("%s: inline URL should override Content-Type to %s", tt.name, tt.inlineType)
		}
		h := rec.Header()
		if h.Get("Cache-Control") != "no-store" || h.Get("X-Robots-Tag") == "" || h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s: headers = %v, want no-store, X-Robots-Tag, no-referrer", tt.name, h)
		}
		if csp := h.Get("Content-Security-Policy"); !strings.Contains(csp, "media-src http://127.0.0.1:") {
			t.Errorf("%s: CSP = %q, want media-src limited to the bucket origin", tt.name, csp)
		}
	}
}

func TestAvatar(t *testing.T) {
	_, mux := newTestServer(t)
	rec := get(mux, "/static/sprobot.png", "Mozilla/5.0")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Errorf("avatar: status %d, Content-Type %q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec := get(mux, "/static/", "Mozilla/5.0"); rec.Code == http.StatusOK {
		t.Error("/static/ should not list the directory")
	}
}
