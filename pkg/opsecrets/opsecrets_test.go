package opsecrets

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

// stubResolve replaces the SDK with fn, clears the cache, and records the
// reference batches it is called with.
func stubResolve(t *testing.T, fn func(refs []string) (map[string]string, error)) *[][]string {
	t.Helper()
	var calls [][]string
	origResolve, origCache := resolve, cache
	cache = map[string]string{}
	resolve = func(refs []string) (map[string]string, error) {
		calls = append(calls, slices.Clone(refs))
		return fn(refs)
	}
	t.Cleanup(func() { resolve, cache = origResolve, origCache })
	return &calls
}

// fixed resolves every reference to "secret:" + reference.
func fixed(refs []string) (map[string]string, error) {
	out := map[string]string{}
	for _, r := range refs {
		out[r] = "secret:" + r
	}
	return out, nil
}

func TestGetenvPlainAndUnset(t *testing.T) {
	calls := stubResolve(t, fixed)
	t.Setenv("OPSECRETS_PLAIN", "plain-value")

	if got, err := Getenv("OPSECRETS_PLAIN"); err != nil || got != "plain-value" {
		t.Fatalf("Getenv(plain) = %q, %v; want plain-value, nil", got, err)
	}
	if got, err := Getenv("OPSECRETS_UNSET"); err != nil || got != "" {
		t.Fatalf("Getenv(unset) = %q, %v; want empty, nil", got, err)
	}
	if len(*calls) != 0 {
		t.Fatalf("resolver called without op:// values: %v", *calls)
	}
}

func TestGetenvReferenceIsCached(t *testing.T) {
	const ref = "op://dev tokens/discord-sprobot/token"
	calls := stubResolve(t, fixed)
	t.Setenv("OPSECRETS_A", ref)
	t.Setenv("OPSECRETS_B", ref)

	for _, name := range []string{"OPSECRETS_A", "OPSECRETS_A", "OPSECRETS_B"} {
		if got, err := Getenv(name); err != nil || got != "secret:"+ref {
			t.Fatalf("Getenv(%s) = %q, %v", name, got, err)
		}
	}
	if len(*calls) != 1 {
		t.Fatalf("resolver calls = %v; want one", *calls)
	}
}

func TestGetenvAllBatches(t *testing.T) {
	calls := stubResolve(t, fixed)
	t.Setenv("OPSECRETS_A", "op://v/a/f")
	t.Setenv("OPSECRETS_B", "op://v/b/f")
	t.Setenv("OPSECRETS_DUP", "op://v/a/f")
	t.Setenv("OPSECRETS_PLAIN", "plain")

	got, err := GetenvAll("OPSECRETS_A", "OPSECRETS_B", "OPSECRETS_DUP", "OPSECRETS_PLAIN", "OPSECRETS_UNSET")
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OPSECRETS_A":     "secret:op://v/a/f",
		"OPSECRETS_B":     "secret:op://v/b/f",
		"OPSECRETS_DUP":   "secret:op://v/a/f",
		"OPSECRETS_PLAIN": "plain",
		"OPSECRETS_UNSET": "",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %q; want %q", k, got[k], v)
		}
	}
	if len(*calls) != 1 || !slices.Equal((*calls)[0], []string{"op://v/a/f", "op://v/b/f"}) {
		t.Fatalf("resolver calls = %v; want one batch of the two unique refs", *calls)
	}
}

func TestGetenvAllPartialFailure(t *testing.T) {
	calls := stubResolve(t, func(refs []string) (map[string]string, error) {
		return map[string]string{"op://v/ok/f": "ok"}, errors.New("op://v/missing/f: itemNotFound")
	})
	t.Setenv("OPSECRETS_OK", "op://v/ok/f")
	t.Setenv("OPSECRETS_MISSING", "op://v/missing/f")

	_, err := GetenvAll("OPSECRETS_OK", "OPSECRETS_MISSING")
	if err == nil {
		t.Fatal("GetenvAll returned no error for a failed reference")
	}
	for _, want := range []string{"OPSECRETS_MISSING", "op://v/missing/f", "itemNotFound"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "OPSECRETS_OK") {
		t.Errorf("error %q blames the reference that resolved", err)
	}
	// The successful reference was cached, so a retry only asks for the failed one.
	_, _ = GetenvAll("OPSECRETS_OK", "OPSECRETS_MISSING")
	if len(*calls) != 2 || !slices.Equal((*calls)[1], []string{"op://v/missing/f"}) {
		t.Fatalf("resolver calls = %v; want the retry to request only the failed ref", *calls)
	}
}
