package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type searchBenchFixture struct {
	dir string
	reg *Registry
}

var (
	fixture25Once   sync.Once
	fixture25       *searchBenchFixture
	fixture150Once  sync.Once
	fixture150      *searchBenchFixture
	fixture500Once  sync.Once
	fixture500      *searchBenchFixture
	fixture1000Once sync.Once
	fixture1000     *searchBenchFixture
)

func createSearchBenchWorkspace(numDirs, filesPerDir int) *searchBenchFixture {
	dir, err := os.MkdirTemp("", fmt.Sprintf("nabd-bench-%d-", numDirs*filesPerDir))
	if err != nil {
		panic(fmt.Sprintf("create bench temp dir: %v", err))
	}
	r, err := NewRegistry(dir)
	if err != nil {
		panic(fmt.Sprintf("open bench registry: %v", err))
	}

	for d := 0; d < numDirs; d++ {
		dirName := fmt.Sprintf("pkg%d", d)
		sub := filepath.Join(dir, dirName)
		if err := os.MkdirAll(sub, 0755); err != nil {
			panic(err)
		}
		for f := 0; f < filesPerDir; f++ {
			fName := fmt.Sprintf("file%d.go", f)
			var content string
			if f%10 == 0 {
				// Large source file
				content = fmt.Sprintf("package %s\n\n// Large file with functions\n", dirName)
				for k := 0; k < 60; k++ {
					content += fmt.Sprintf("func Routine_%d_%d_%d() string { return \"val\" }\n", d, f, k)
				}
			} else {
				// Small source file
				content = fmt.Sprintf("package %s\n\nfunc Function_%d_%d() string {\n\treturn \"result\"\n}\n", dirName, d, f)
			}
			if d == numDirs/2 && f == filesPerDir/2 {
				content += "\n// TARGET_NEEDLE: rare keyword to search\n"
			}
			if err := os.WriteFile(filepath.Join(sub, fName), []byte(content), 0644); err != nil {
				panic(err)
			}
		}
	}

	// Add vendor dir that should be ignored
	vendorDir := filepath.Join(dir, "vendor", "lib")
	_ = os.MkdirAll(vendorDir, 0755)
	_ = os.WriteFile(filepath.Join(vendorDir, "v.go"), []byte("// TARGET_NEEDLE in vendor"), 0644)

	// Add sensitive file that should be ignored
	_ = os.WriteFile(filepath.Join(dir, ".env"), []byte("SECRET=TARGET_NEEDLE"), 0600)

	// Add mixed file extensions (.txt, .md, .json)
	_ = os.WriteFile(filepath.Join(dir, "README.md"), []byte("# Benchmark Repo\nDocumentation content\n"), 0644)
	_ = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"name":"benchmark","enabled":true}`), 0644)

	return &searchBenchFixture{dir: dir, reg: r}
}

func getSearchFixture(numDirs, filesPerDir int) *searchBenchFixture {
	total := numDirs * filesPerDir
	switch total {
	case 25:
		fixture25Once.Do(func() { fixture25 = createSearchBenchWorkspace(numDirs, filesPerDir) })
		return fixture25
	case 150:
		fixture150Once.Do(func() { fixture150 = createSearchBenchWorkspace(numDirs, filesPerDir) })
		return fixture150
	case 500:
		fixture500Once.Do(func() { fixture500 = createSearchBenchWorkspace(numDirs, filesPerDir) })
		return fixture500
	case 1000:
		fixture1000Once.Do(func() { fixture1000 = createSearchBenchWorkspace(numDirs, filesPerDir) })
		return fixture1000
	default:
		return createSearchBenchWorkspace(numDirs, filesPerDir)
	}
}

func BenchmarkSearchFiles_25Files(b *testing.B) {
	ws := getSearchFixture(5, 5) // 25 files
	args := json.RawMessage(`{"pattern":"TARGET_NEEDLE"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := ws.reg.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkSearchFiles_150Files(b *testing.B) {
	ws := getSearchFixture(15, 10) // 150 files
	args := json.RawMessage(`{"pattern":"TARGET_NEEDLE"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := ws.reg.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkSearchFiles_500Files(b *testing.B) {
	ws := getSearchFixture(25, 20) // 500 files
	args := json.RawMessage(`{"pattern":"TARGET_NEEDLE"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := ws.reg.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkSearchFiles_1000Files(b *testing.B) {
	ws := getSearchFixture(50, 20) // 1000 files
	args := json.RawMessage(`{"pattern":"TARGET_NEEDLE"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := ws.reg.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkReadFile_FullVsRange(b *testing.B) {
	dir := b.TempDir()
	r, err := NewRegistry(dir)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = r.Close() })

	// Create a 1000-line file
	var content string
	for i := 1; i <= 1000; i++ {
		content += fmt.Sprintf("Line %d: data content goes here for benchmarking\n", i)
	}
	if err := os.WriteFile(filepath.Join(dir, "large.txt"), []byte(content), 0644); err != nil {
		b.Fatal(err)
	}

	fullArgs := json.RawMessage(`{"path":"large.txt"}`)
	rangeArgs := json.RawMessage(`{"path":"large.txt","start_line":500,"end_line":510}`)

	b.Run("FullFileRead_1000Lines", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = r.Run(context.Background(), "read_file", fullArgs)
		}
	})

	b.Run("LineRangeRead_10Lines", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = r.Run(context.Background(), "read_file", rangeArgs)
		}
	})
}

func TestMain(m *testing.M) {
	code := m.Run()
	if fixture25 != nil {
		_ = fixture25.reg.Close()
		_ = os.RemoveAll(fixture25.dir)
	}
	if fixture150 != nil {
		_ = fixture150.reg.Close()
		_ = os.RemoveAll(fixture150.dir)
	}
	if fixture500 != nil {
		_ = fixture500.reg.Close()
		_ = os.RemoveAll(fixture500.dir)
	}
	if fixture1000 != nil {
		_ = fixture1000.reg.Close()
		_ = os.RemoveAll(fixture1000.dir)
	}
	os.Exit(code)
}
