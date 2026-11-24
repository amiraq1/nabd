package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func splitPage(t *testing.T, page string) (string, int64, bool) {
	t.Helper()
	marker := "\n...[truncated; continue at byte offset "
	i := strings.LastIndex(page, marker)
	if i < 0 {
		return page, 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSuffix(page[i+len(marker):], "]"), 10, 64)
	if err != nil {
		t.Fatal("invalid continuation")
	}
	return page[:i], n, true
}
func TestIntegrationReadFilePaging(t *testing.T) {
	root, r := newTestRegistry(t)
	content := strings.Repeat("0123456789", 1000)
	writeTestFile(t, filepath.Join(root, "range.txt"), content)
	out := runTool(t, r, "read_file", map[string]any{"path": "range.txt", "offset": 4321, "limit": 64})
	text, next, more := splitPage(t, out)
	if text != content[4321:4385] || !more || next != 4385 {
		t.Error("incorrect page or continuation")
	}
	for _, args := range []map[string]any{{"path": "range.txt", "offset": -1}, {"path": "range.txt", "limit": -1}, {"path": "range.txt", "limit": 0}, {"path": "range.txt", "limit": 3585}} {
		if out := runTool(t, r, "read_file", args); !strings.HasPrefix(out, "Error:") {
			t.Error("invalid range accepted")
		}
	}
	out = runTool(t, r, "read_file", map[string]any{"path": "range.txt"})
	_, next, more = splitPage(t, out)
	if !more || next != 3584 || len(out) > maxOutputBytes {
		t.Error("default page loses continuation or exceeds output cap")
	}
}
func TestIntegrationPagingKeepsByteLimitAtRuneBoundaries(t *testing.T) {
	root, r := newTestRegistry(t)
	writeTestFile(t, filepath.Join(root, "boundary"), "A€Z")
	page := runTool(t, r, "read_file", map[string]any{"path": "boundary", "limit": 2})
	text, next, more := splitPage(t, page)
	if text != "A" || !more || next != 1 {
		t.Error("page exceeded its byte limit across a later UTF-8 rune")
	}
	page = runTool(t, r, "read_file", map[string]any{"path": "boundary", "offset": 1, "limit": 1})
	text, next, more = splitPage(t, page)
	if text != "€" || !more || next != 4 {
		t.Error("first UTF-8 rune did not make complete forward progress")
	}
}

func TestIntegrationReadFilePagingUTF8AndInvalidBytes(t *testing.T) {
	root, r := newTestRegistry(t)
	for name, data := range map[string][]byte{"utf8": []byte("مرحبا 👩‍💻 بالعالم € — 絵文字"), "invalid": append([]byte(strings.Repeat("\xff", 4000)), []byte("END")...)} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		var offset int64
		limit := 2
		if name == "invalid" {
			limit = 3584
		}
		for i := 0; i < 100; i++ {
			page := runTool(t, r, "read_file", map[string]any{"path": name, "offset": offset, "limit": limit})
			if len(page) > maxOutputBytes || !utf8.ValidString(page) {
				t.Fatalf("%s: page is oversized or invalid UTF-8", name)
			}
			text, next, more := splitPage(t, page)
			out.WriteString(text)
			if !more {
				break
			}
			if next <= offset {
				t.Fatal("pagination failed to advance")
			}
			offset = next
			if i == 99 {
				t.Fatal("pagination did not terminate")
			}
		}
		expected := string(data)
		if name == "invalid" {
			expected = strings.Repeat("\uFFFD", 4000) + "END"
		}
		if out.String() != expected {
			t.Errorf("%s: paged output differs from expected", name)
		}
	}
}
func TestIntegrationReadFilePagingMCP(t *testing.T) {
	root, r := newTestRegistry(t)
	writeTestFile(t, filepath.Join(root, "file"), strings.Repeat("abc", 2000))
	var found bool
	for _, entry := range r.OpenAITools() {
		fn := entry["function"].(map[string]any)
		if fn["name"] == "read_file" {
			p := fn["parameters"].(map[string]any)["properties"].(map[string]any)
			_, a := p["offset"]
			_, b := p["limit"]
			found = a && b
		}
	}
	if !found {
		t.Fatal("read_file schema omits pagination")
	}
	out := runTool(t, r, "read_file", map[string]any{"path": "file", "offset": 5, "limit": 3})
	text, next, more := splitPage(t, out)
	if text != "cab" || !more || next != 8 {
		t.Fatalf("unexpected range result: %q", out)
	}
}

func TestIntegrationStructuredReadPage(t *testing.T) {
	root, r := newTestRegistry(t)
	data := strings.Repeat("\x00", 4000)
	writeTestFile(t, filepath.Join(root, "binary"), data)
	var assembled strings.Builder
	var offset int64
	for i := 0; i < 20; i++ {
		out := runTool(t, r, "read_file", map[string]any{"path": "binary", "offset": offset, "limit": 3584, "format": "json"})
		if len(out) > maxOutputBytes {
			t.Fatal("structured page exceeds output budget")
		}
		var page struct {
			Content    string `json:"content"`
			Truncated  bool   `json:"truncated"`
			NextOffset *int64 `json:"next_offset"`
		}
		if err := json.Unmarshal([]byte(out), &page); err != nil {
			t.Fatal("structured page is not JSON")
		}
		assembled.WriteString(page.Content)
		if !page.Truncated {
			if page.NextOffset != nil {
				t.Error("final page has continuation")
			}
			break
		}
		if page.NextOffset == nil || *page.NextOffset <= offset {
			t.Fatal("invalid structured continuation")
		}
		offset = *page.NextOffset
	}
	if assembled.String() != data {
		t.Error("structured pages did not reconstruct content")
	}
	out := runTool(t, r, "read_file", map[string]any{"path": "binary", "format": "bogus"})
	if !strings.HasPrefix(out, "Error:") {
		t.Error("unknown output format accepted")
	}
}
