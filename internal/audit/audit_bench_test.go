package audit

import (
	"os"
	"testing"
	"time"
)

func BenchmarkAudit_Durable(b *testing.B) {
	dir := b.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })

	log, err := Open(root)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })

	event := Event{
		Time:       time.Now().UTC(),
		Event:      "applied",
		Tool:       "edit_file",
		ArgsSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Detail:     "main.go",
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := log.Write(event); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkAudit_BestEffort(b *testing.B) {
	dir := b.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = root.Close() })

	log, err := Open(root)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })

	event := Event{
		Time:       time.Now().UTC(),
		Event:      "applied",
		Tool:       "read_file",
		ArgsSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Detail:     "main.go",
	}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := log.WriteBestEffort(event); err != nil {
			b.Fatal(err)
		}
	}
}
