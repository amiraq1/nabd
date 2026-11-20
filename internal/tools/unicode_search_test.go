package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnicodeSearch_AllCases(t *testing.T) {
	dir := t.TempDir()
	r, err := NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// 1. Arabic file
	arabicContent := `package main

// مرحبا بالعالم - دالة اختبار
func دالة_رئيسية() string {
	رسالة := "أهلاً وسهلاً بك في نبض"
	return رسالة
}
`
	if err := os.WriteFile(filepath.Join(dir, "arabic.go"), []byte(arabicContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 2. German / Latin Unicode file
	germanContent := `package main

const Address = "Große Straße 42, Berlin"
const Warning = "ÜBERPRÜFUNG ERFORDERLICH"
const Coffee = "Café au lait"
`
	if err := os.WriteFile(filepath.Join(dir, "german.go"), []byte(germanContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 3. Mixed ASCII and Unicode
	mixedContent := `package main

// Identifier: test_دالة_123
func test_دالة_123() int {
	return 42
}
`
	if err := os.WriteFile(filepath.Join(dir, "mixed.go"), []byte(mixedContent), 0644); err != nil {
		t.Fatal(err)
	}

	// 4. Invalid UTF-8 binary-like file with printable prefix
	invalidUTF8 := []byte("ValidPrefix\n\xff\xfe\xfdInvalidBytesHere\nEndLine\n")
	if err := os.WriteFile(filepath.Join(dir, "invalid.txt"), invalidUTF8, 0644); err != nil {
		t.Fatal(err)
	}

	// 5. Standard ASCII file for fast-path regression
	asciiContent := `package main

// TARGET_NEEDLE in pure ascii
func TargetFunction() string {
	return "target_needle_result"
}
`
	if err := os.WriteFile(filepath.Join(dir, "ascii.go"), []byte(asciiContent), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		pattern    string
		expectFile string
		expectText string
		expectErr  bool
	}{
		{
			name:       "Arabic exact match",
			pattern:    "مرحبا بالعالم",
			expectFile: "arabic.go",
			expectText: "مرحبا بالعالم",
		},
		{
			name:       "Arabic identifier match",
			pattern:    "دالة_رئيسية",
			expectFile: "arabic.go",
			expectText: "دالة_رئيسية",
		},
		{
			name:       "German lowercase ueber matches uppercase ÜBERPRÜFUNG",
			pattern:    "überprüfung",
			expectFile: "german.go",
			expectText: "ÜBERPRÜFUNG",
		},
		{
			name:       "German uppercase matches lowercase",
			pattern:    "CAFÉ",
			expectFile: "german.go",
			expectText: "Café",
		},
		{
			name:       "German Straße matches STRASSE",
			pattern:    "STRASSE",
			expectFile: "german.go",
			expectText: "Straße",
		},
		{
			name:       "German STRASSE query matches Straße in file",
			pattern:    "straße",
			expectFile: "german.go",
			expectText: "Straße",
		},
		{
			name:       "Mixed ASCII and Unicode pattern",
			pattern:    "test_دالة_123",
			expectFile: "mixed.go",
			expectText: "test_دالة_123",
		},
		{
			name:       "ASCII fast path regression - lowercase query matches uppercase",
			pattern:    "target_needle",
			expectFile: "ascii.go",
			expectText: "TARGET_NEEDLE",
		},
		{
			name:       "ASCII fast path regression - uppercase query matches lowercase",
			pattern:    "TARGET_NEEDLE_RESULT",
			expectFile: "ascii.go",
			expectText: "target_needle_result",
		},
		{
			name:       "Invalid UTF-8 does not panic and matches valid prefix",
			pattern:    "ValidPrefix",
			expectFile: "invalid.txt",
			expectText: "ValidPrefix",
		},
		{
			name:      "Empty pattern rejected with error",
			pattern:   "",
			expectErr: true,
		},
		{
			name:      "Whitespace pattern rejected with error",
			pattern:   "   ",
			expectErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"pattern": tc.pattern})
			res := r.Run(context.Background(), "search_files", args)

			if tc.expectErr {
				if !strings.Contains(res, "Error:") && !strings.Contains(res, "cannot be empty") {
					t.Fatalf("expected error for empty pattern, got: %s", res)
				}
				return
			}

			if strings.HasPrefix(res, "Error:") {
				t.Fatalf("unexpected error for %q: %s", tc.pattern, res)
			}
			if !strings.Contains(res, tc.expectFile) {
				t.Errorf("expected result to contain file %q for query %q, got:\n%s", tc.expectFile, tc.pattern, res)
			}
			if !strings.Contains(res, tc.expectText) {
				t.Errorf("expected result to contain text %q for query %q, got:\n%s", tc.expectText, tc.pattern, res)
			}
		})
	}
}

func BenchmarkSearch_ASCIIFastPath(b *testing.B) {
	dir := b.TempDir()
	r, err := NewRegistry(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()

	for i := 0; i < 50; i++ {
		content := fmt.Sprintf("package pkg%d\nfunc Example%d() string { return \"TARGET_NEEDLE\" }\n", i, i)
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(content), 0644)
	}
	args := json.RawMessage(`{"pattern":"TARGET_NEEDLE"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := r.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkSearch_UnicodeArabic(b *testing.B) {
	dir := b.TempDir()
	r, err := NewRegistry(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()

	for i := 0; i < 50; i++ {
		content := fmt.Sprintf("package pkg%d\n// مرحبا بالعالم في نبض\nfunc دالة_%d() string { return \"نتيجة\" }\n", i, i)
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(content), 0644)
	}
	args := json.RawMessage(`{"pattern":"مرحبا بالعالم"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := r.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}

func BenchmarkSearch_UnicodeGerman(b *testing.B) {
	dir := b.TempDir()
	r, err := NewRegistry(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer r.Close()

	for i := 0; i < 50; i++ {
		content := fmt.Sprintf("package pkg%d\n// Große Straße Berlin\nfunc Test_%d() string { return \"ÜBERPRÜFUNG\" }\n", i, i)
		_ = os.WriteFile(filepath.Join(dir, fmt.Sprintf("file%d.go", i)), []byte(content), 0644)
	}
	args := json.RawMessage(`{"pattern":"straße"}`)

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := r.Run(context.Background(), "search_files", args)
		if out == "" {
			b.Fatal("expected match")
		}
	}
}
