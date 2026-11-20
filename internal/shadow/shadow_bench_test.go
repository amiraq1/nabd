package shadow

import (
	"fmt"
	"os"
	"testing"
)

func BenchmarkShadow_CachedLatestVsReload(b *testing.B) {
	dir := b.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })

	store, err := Open(root)
	if err != nil {
		b.Fatal(err)
	}

	// Create 10 backup entries
	for i := 0; i < 10; i++ {
		p := fmt.Sprintf("file_%d.txt", i)
		_, err := store.Backup(p, []byte("before content"), 0644, []byte("after content"))
		if err != nil {
			b.Fatal(err)
		}
	}

	b.Run("CachedLookup", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_, _, err := store.Latest("file_5.txt")
			if err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("ColdDiskReload", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			// Invalidate cache by clearing index to simulate cold restart
			store.mu.Lock()
			store.index = nil
			store.mu.Unlock()

			_, _, err := store.Latest("file_5.txt")
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkShadow_BackupAndPrune(b *testing.B) {
	dir := b.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })

	store, err := Open(root)
	if err != nil {
		b.Fatal(err)
	}

	before := []byte("original content before modification")
	after := []byte("updated content after modification")

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		p := fmt.Sprintf("bench_file_%d.txt", i%15)
		_, err := store.Backup(p, before, 0644, after)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkShadow_MarkUndone(b *testing.B) {
	dir := b.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })

	store, err := Open(root)
	if err != nil {
		b.Fatal(err)
	}

	entries := make([]Entry, b.N)
	for i := 0; i < b.N; i++ {
		entry, err := store.Backup(fmt.Sprintf("f%d.txt", i), []byte("a"), 0644, []byte("b"))
		if err != nil {
			b.Fatal(err)
		}
		entries[i] = entry
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = store.MarkUndone(entries[i].ID)
	}
}
