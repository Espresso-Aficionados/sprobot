package bot

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/disgoorg/disgo/discord"
	"github.com/disgoorg/snowflake/v2"

	"github.com/Espresso-Aficionados/sprobot/pkg/botutil"
	"github.com/Espresso-Aficionados/sprobot/pkg/statever"
)

// State a newer build wrote is refused: the guild stays unloaded, nothing is
// backed up as corrupt or saved over it, and load reports it for startup to
// exit on.
func TestGuildStateStoreRefusesNewerState(t *testing.T) {
	store, h := newTestStore(t)
	const newer = `{"version":2,"value":"from the future","renamed":[1]}`
	h.put(testObject(testGuild), newer)

	err := store.load([]snowflake.ID{testGuild})
	var n *statever.NewerError
	if !errors.As(err, &n) || n.Version != 2 || n.Name != "test/123.json" {
		t.Fatalf("load = %v, want a NewerError for test/123.json", err)
	}
	if st := store.getOrCreate(testGuild); st != nil {
		t.Fatalf("getOrCreate = %+v, want nil for refused state", st)
	}
	if err := store.persist(testGuild); !errors.Is(err, errNotLoaded) {
		t.Fatalf("persist = %v, want errNotLoaded", err)
	}
	store.retryFailedLoads()
	store.save()
	if got, _ := h.object(testObject(testGuild)); got != newer {
		t.Fatalf("stored data = %s, want it untouched", got)
	}
	if backups := corruptBackups(h); len(backups) != 0 {
		t.Fatalf("newer state backed up as corrupt: %v", backups)
	}
}

// Objects from before versions load as version 1, and saving them stamps
// them.
func TestGuildStateStoreLoadsUnversionedAndStamps(t *testing.T) {
	store, h := newTestStore(t)
	h.put(testObject(testGuild), `{"value":"old"}`)
	if err := store.load([]snowflake.ID{testGuild}); err != nil {
		t.Fatal(err)
	}
	if v := value(store.get(testGuild)); v != "old" {
		t.Fatalf("Value = %q", v)
	}
	if err := store.persist(testGuild); err != nil {
		t.Fatal(err)
	}
	if got, _ := h.object(testObject(testGuild)); got != `{"version":1,"value":"old"}` {
		t.Fatalf("stored %s", got)
	}
}

// Every store saves an object that a build from before versions still
// decodes (it ignores "version"), and that this build loads back.
func TestEveryStoreSavesStateOldBuildsRead(t *testing.T) {
	b, h := newStateBot(t)
	if err := b.loadState(); err != nil {
		t.Fatal(err)
	}
	b.saveState()
	for _, s := range b.stateStores() {
		decodeOld := s.(interface{ decodeIgnoringVersion([]byte) error }).decodeIgnoringVersion
		for _, guildID := range botutil.AllowedGuildIDs {
			body, ok := h.object(fmt.Sprintf("/test-bucket/%s/%d.json", s.prefix(), guildID))
			if !ok {
				t.Errorf("%s: nothing saved for guild %d", s.prefix(), guildID)
				continue
			}
			if !strings.HasPrefix(body, `{"version":1`) {
				t.Errorf("%s: saved %.40s..., want version first", s.prefix(), body)
			}
			if err := decodeOld([]byte(body)); err != nil {
				t.Errorf("%s: a decoder from before versions fails on the saved object: %v", s.prefix(), err)
			}
		}
	}
	if err := b.loadState(); err != nil {
		t.Fatalf("loading back: %v", err)
	}
}

// Startup refuses state a newer build wrote in any store.
func TestLoadStateRefusesNewerState(t *testing.T) {
	b, h := newStateBot(t)
	guildID := botutil.AllowedGuildIDs[0]
	h.put(fmt.Sprintf("/test-bucket/tickets/%d.json", guildID), `{"version":9}`)
	err := b.loadState()
	if !statever.IsNewer(err) || !strings.Contains(err.Error(), fmt.Sprintf("tickets/%d.json is state version 9", guildID)) {
		t.Fatalf("loadState = %v, want the tickets refusal", err)
	}
}

// The cache dir records its version before a cache file is written, and a
// newer build's dir is never written to.
func TestCacheDirVersion(t *testing.T) {
	b := newCacheTestBot(t)
	if v, exists, err := cacheDirVersion(b.cacheDir); v != 1 || exists || err != nil {
		t.Fatalf("empty dir: %d, %v, %v; want version 1, no file", v, exists, err)
	}
	b.saveCaches()
	data, err := os.ReadFile(filepath.Join(b.cacheDir, cacheVersionFile))
	if err != nil || string(data) != "{\"version\":1}\n" {
		t.Fatalf("version file = %q, %v", data, err)
	}

	// A newer build's dir: its files are left as they are.
	vpath := filepath.Join(b.cacheDir, cacheVersionFile)
	os.WriteFile(vpath, []byte(`{"version":2}`), 0o600)
	msgs := filepath.Join(b.cacheDir, messageCacheFile)
	os.WriteFile(msgs, []byte(`{"version":2,"items":[]}`), 0o600)
	b.msgCache.Put(1, 2, discord.Message{ID: 2, ChannelID: 1})
	b.saveCaches()
	if data, _ := os.ReadFile(msgs); string(data) != `{"version":2,"items":[]}` {
		t.Fatalf("message cache rewritten over a newer build's: %s", data)
	}
	if _, _, err := cacheDirVersion(b.cacheDir); !statever.IsNewer(err) {
		t.Fatalf("cacheDirVersion = %v, want a NewerError", err)
	}

	// One that can't be read is this build's to replace.
	os.WriteFile(vpath, []byte(`{"vers`), 0o600)
	b.saveCaches()
	if data, _ := os.ReadFile(vpath); string(data) != "{\"version\":1}\n" {
		t.Fatalf("corrupt version file not replaced: %q", data)
	}
}

// -check-state reads every store's object for every allowed guild and the
// cache dir, sends nothing but GETs, and writes nothing locally.
func TestCheckState(t *testing.T) {
	b, h := newStateBot(t)
	if err := b.loadState(); err != nil {
		t.Fatal(err)
	}
	b.saveState()
	dir := t.TempDir()
	cb := newCacheTestBot(t)
	cb.cacheDir = dir
	cb.saveCaches()
	before := dirListing(t, dir)

	h.fake.Mu.Lock()
	h.fake.Requests = nil
	h.fake.Mu.Unlock()
	r := checkState(t.Context(), b.S3, dir)
	if code := r.Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 0 {
		t.Fatalf("exit %d on this build's own state: %+v", code, r.Docs)
	}
	want := len(b.stateStores())*len(botutil.AllowedGuildIDs) + 1 + len(cacheFiles)
	if len(r.Docs) != want {
		t.Errorf("%d documents, want %d", len(r.Docs), want)
	}
	h.fake.Mu.Lock()
	for _, req := range h.fake.Requests {
		if !strings.HasPrefix(req, http.MethodGet+" ") {
			t.Errorf("-check-state sent %s", req)
		}
	}
	h.fake.Mu.Unlock()
	if after := dirListing(t, dir); !reflect.DeepEqual(before, after) {
		t.Errorf("cache dir changed: %v -> %v", before, after)
	}

	guildID := botutil.AllowedGuildIDs[1]
	h.put(fmt.Sprintf("/test-bucket/welcome/%d.json", guildID), `{"version":4}`)
	h.setHook(failGets(fmt.Sprintf("/test-bucket/starboard/%d.json", guildID)))
	if code := checkState(t.Context(), b.S3, dir).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 3 {
		t.Errorf("newer and unreadable: exit %d, want 3", code)
	}
	h.put(fmt.Sprintf("/test-bucket/welcome/%d.json", guildID), `{}`)
	if code := checkState(t.Context(), b.S3, dir).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 1 {
		t.Errorf("unreadable: exit %d, want 1", code)
	}
	h.setHook(nil)

	os.WriteFile(filepath.Join(dir, cacheVersionFile), []byte(`{"version":3}`), 0o600)
	r = checkState(t.Context(), b.S3, dir)
	if code := r.Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 3 {
		t.Errorf("newer cache dir: exit %d, want 3", code)
	}
	if n := len(r.Docs); n != len(b.stateStores())*len(botutil.AllowedGuildIDs)+1 {
		t.Errorf("newer cache dir: %d documents; its files shouldn't be read", n)
	}

	// No state anywhere yet is fine.
	empty, _ := newStateBot(t)
	if code := checkState(t.Context(), empty.S3, filepath.Join(t.TempDir(), "none")).Print(new(bytes.Buffer), new(bytes.Buffer), false); code != 0 {
		t.Errorf("no state: exit %d, want 0", code)
	}
}

// decodeIgnoringVersion decodes data as a build from before versions does.
func (s *guildStateStore[T]) decodeIgnoringVersion(data []byte) error {
	return json.Unmarshal(data, s.newState())
}

func dirListing(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, e := range entries {
		info, _ := e.Info()
		out[e.Name()] = fmt.Sprint(info.Size(), info.ModTime().UnixNano())
	}
	return out
}
