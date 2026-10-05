package classify

import (
	"os"
	"testing"
)

func TestSQLiteCacheRoundTrip(t *testing.T) {
	t.Parallel()
	path := t.TempDir() + "/mailsorter.db"
	cache, err := OpenCache(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	decision := Decision{Category: "work", Confidence: 0.91, Provider: "jev", Model: "jev-1.13.0", Reason: "ok", Folder: "Work", Action: "move"}
	if err := cache.Put(t.Context(), "abc", decision); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode=%o", info.Mode().Perm())
	}
	reopened, err := OpenCache(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	got, ok, err := reopened.Get(t.Context(), "abc")
	if err != nil || !ok {
		t.Fatalf("got=%+v ok=%v err=%v", got, ok, err)
	}
	if got.Category != "work" || got.Folder != "" || got.Confidence != 0.91 {
		t.Fatalf("got=%+v", got)
	}
}
