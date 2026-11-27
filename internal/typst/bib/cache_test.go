package bib

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestCacheHitInvalidationDeleteAndConcurrency(t *testing.T) {
	ClearCache()
	t.Cleanup(ClearCache)

	dir := t.TempDir()
	bibPath := filepath.Join(dir, "refs.bib")

	v1 := "@article{first2020, author={Alice}, title={First Title}, year={2020}}\n"
	if err := os.WriteFile(bibPath, []byte(v1), 0o644); err != nil {
		t.Fatal(err)
	}

	// First load -> populates cache.
	got1 := EntriesForFile(bibPath)
	if len(got1) != 1 || got1[0].Key != "first2020" || got1[0].Source != filepath.Clean(bibPath) {
		t.Fatalf("unexpected initial entries: %#v", got1)
	}

	// Second load without change -> hits cache.
	got2 := EntriesForFile(bibPath)
	if len(got2) != 1 || got2[0].Key != "first2020" {
		t.Fatalf("unexpected cached entries: %#v", got2)
	}

	// Update file content + mtime -> cache invalidates and reloads.
	v2 := "@article{second2024, author={Bob}, title={Updated Title}, year={2024}}\n"
	if err := os.WriteFile(bibPath, []byte(v2), 0o644); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(2 * time.Second)
	_ = os.Chtimes(bibPath, future, future)

	got3 := EntriesForFile(bibPath)
	if len(got3) != 1 || got3[0].Key != "second2024" {
		t.Fatalf("expected cache invalidation on mtime/size update, got %#v", got3)
	}

	// Concurrent reads under -race.
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			entries := EntriesForFile(bibPath)
			if len(entries) != 1 || entries[0].Key != "second2024" {
				t.Errorf("concurrent read mismatch: %#v", entries)
			}
		}()
	}
	wg.Wait()

	// Delete file -> cache purges cleanly and returns nil.
	if err := os.Remove(bibPath); err != nil {
		t.Fatal(err)
	}
	if got4 := EntriesForFile(bibPath); len(got4) != 0 {
		t.Fatalf("expected nil after deleting file, got %#v", got4)
	}
}
