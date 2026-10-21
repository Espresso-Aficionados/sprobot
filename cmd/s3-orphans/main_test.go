package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sadbox/sprobot/pkg/testutil"
)

func name(c byte) string { return strings.Repeat(string(c), 30) }

// seedBucket fills a fake bucket with one object for every case the sweep
// decides, and returns the keys it should find orphaned.
func seedBucket(t *testing.T, fake *testutil.FakeS3, now time.Time) []string {
	t.Helper()
	put := func(key string, v any) {
		body, ok := v.([]byte)
		if !ok {
			var err error
			if body, err = json.Marshal(v); err != nil {
				t.Fatal(err)
			}
		}
		fake.Objects["/test-bucket/"+key] = body
	}
	fake.Mu.Lock()
	defer fake.Mu.Unlock()

	put("profiles/1/Coffee Setup/2.json", map[string]string{"Gear Picture": "images/1/Coffee Setup/2.png"})
	put("profiles/1/Coffee Setup/4.json", map[string]string{"Machine": "Rocket"})
	put("profiles/1/Coffee Setup/5.json", map[string]string{"Gear Picture": "https://cdn.example/5.png"})
	put("profiles/1/Retired Setup/6.json", map[string]string{"Machine": "Rocket"})
	for _, key := range []string{
		"images/1/Coffee Setup/2.png", // in use
		"images/1/Coffee Setup/2.jpg", // replaced by the .png
		"images/1/Coffee Setup/3.png", // no profile
		"images/1/Coffee Setup/4.png", // profile has no image
		"images/1/Coffee Setup/5.png", // profile points elsewhere: left alone
		"images/1/Coffee Setup/7.png", // too new to judge
		"images/stray.png",            // not a profile image key
	} {
		put(key, []byte("image"))
	}
	fake.Modified["/test-bucket/images/1/Coffee Setup/7.png"] = now.Add(-time.Hour)

	put("stickies/1.json", map[string]any{"100": map[string]any{
		"file_urls": []string{"sticky_files/1/" + name('A') + ".png"},
		"embeds":    []any{map[string]any{"image": map[string]string{"url": "https://bot.espressoaf.com/files/sticky/1/" + name('B') + ".png"}}},
	}})
	put("shortcuts/1.json", map[string]any{"shortcuts": map[string]any{
		"pic": map[string]any{"responses": []string{"shortcut_files/1/" + name('D') + ".gif"}},
	}})
	for _, key := range []string{
		"sticky_files/1/" + name('A') + ".png",
		"sticky_files/1/" + name('B') + ".png",
		"sticky_files/1/" + name('C') + ".png",
		"shortcut_files/1/" + name('D') + ".gif",
		"shortcut_files/1/" + name('E'),
		"mod_files/1/" + name('F') + ".png", // never touched, referenced or not
	} {
		put(key, []byte("file"))
	}

	return []string{
		"images/1/Coffee Setup/2.jpg",
		"images/1/Coffee Setup/3.png",
		"images/1/Coffee Setup/4.png",
		"sticky_files/1/" + name('C') + ".png",
		"shortcut_files/1/" + name('E'),
	}
}

func TestFindOrphans(t *testing.T) {
	fake := testutil.NewFakeS3()
	fake.PageSize = 3 // make every listing span pages
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)
	now := time.Now()
	want := seedBucket(t, fake, now)

	s, err := findOrphans(context.Background(), c, now)
	if err != nil {
		t.Fatalf("findOrphans: %v", err)
	}
	var got []string
	for _, o := range s.orphans {
		got = append(got, o.Key)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("orphans = %q\nwant %q", got, want)
	}
	if s.tooNew != 1 || s.unclearProfiles != 1 || s.retiredProfiles != 1 || !slices.Equal(s.unknownKeys, []string{"images/stray.png"}) {
		t.Errorf("tooNew %d, unclear %d, retired %d, unknown %q; want 1, 1, 1, [images/stray.png]",
			s.tooNew, s.unclearProfiles, s.retiredProfiles, s.unknownKeys)
	}
}

func TestRunDryRunDeletesNothing(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)
	now := time.Now()
	orphans := seedBucket(t, fake, now)
	before := len(fake.Objects)

	var out bytes.Buffer
	if err := run(context.Background(), c, &out, false, now); err != nil {
		t.Fatalf("run: %v", err)
	}
	fake.Mu.Lock()
	if len(fake.Deleted) != 0 || len(fake.Objects) != before {
		t.Errorf("dry run deleted %q", fake.Deleted)
	}
	fake.Mu.Unlock()
	for _, want := range append(orphans, "orphans:", "Dry run") {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output missing %q:\n%s", want, out.String())
		}
	}
}

func TestRunDeleteRemovesOnlyOrphans(t *testing.T) {
	fake := testutil.NewFakeS3()
	server := httptest.NewServer(fake)
	defer server.Close()
	c := testutil.NewTestS3Client(t, server)
	now := time.Now()
	orphans := seedBucket(t, fake, now)
	before := len(fake.Objects)

	var out bytes.Buffer
	if err := run(context.Background(), c, &out, true, now); err != nil {
		t.Fatalf("run: %v", err)
	}

	fake.Mu.Lock()
	defer fake.Mu.Unlock()
	var deleted []string
	for _, path := range fake.Deleted {
		deleted = append(deleted, strings.TrimPrefix(path, "/test-bucket/"))
	}
	slices.Sort(deleted)
	slices.Sort(orphans)
	if !slices.Equal(deleted, orphans) {
		t.Errorf("deleted %q\nwant %q", deleted, orphans)
	}
	if len(fake.Objects) != before-len(orphans) {
		t.Errorf("%d objects left, want %d", len(fake.Objects), before-len(orphans))
	}
	if _, ok := fake.Objects["/test-bucket/mod_files/1/"+name('F')+".png"]; !ok {
		t.Error("a mod file was deleted")
	}
}
