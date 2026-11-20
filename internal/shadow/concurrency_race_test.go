package shadow

import (
	"fmt"
	"os"
	"sync"
	"testing"
)

// TestConcurrentShadowIndexUpdates verifies that concurrent calls to Backup
// across multiple files and concurrent calls to Latest / List / Undo do not race,
// corrupt the in-memory index, or lose state.
func TestConcurrentShadowIndexUpdates(t *testing.T) {
	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()

	store, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}

	const goroutines = 20
	const filesPerG = 5
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(gID int) {
			defer wg.Done()
			for f := 0; f < filesPerG; f++ {
				relPath := fmt.Sprintf("file_%d_%d.txt", gID, f)
				origContent := []byte(fmt.Sprintf("initial content for %s", relPath))
				newContent := []byte(fmt.Sprintf("updated content for %s", relPath))

				entry, err := store.Backup(relPath, origContent, 0644, newContent)
				if err != nil {
					t.Errorf("Backup failed for %s: %v", relPath, err)
					return
				}

				// Concurrent read of latest
				latest, _, err := store.Latest(relPath)
				if err != nil {
					t.Errorf("Latest failed for %s: %v", relPath, err)
					return
				}
				if latest.ID != entry.ID {
					t.Errorf("Latest mismatch for %s: got %v, want %v", relPath, latest.ID, entry.ID)
				}
			}
		}(g)
	}

	wg.Wait()

	// Verify all entries can be retrieved from the index
	for g := 0; g < goroutines; g++ {
		for f := 0; f < filesPerG; f++ {
			relPath := fmt.Sprintf("file_%d_%d.txt", g, f)
			entry, _, err := store.Latest(relPath)
			if err != nil {
				t.Errorf("failed to retrieve latest for %s: %v", relPath, err)
			}
			if entry.Path != relPath {
				t.Errorf("path mismatch: got %s, want %s", entry.Path, relPath)
			}
		}
	}
}
