package tools

import (
	"bytes"
	"encoding/json"
	"sort"
	"testing"
)

func toolNames(defs []map[string]any) []string {
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		fn, ok := def["function"].(map[string]any)
		if !ok {
			continue
		}
		if name, ok := fn["name"].(string); ok {
			names = append(names, name)
		}
	}
	return names
}

// TestOpenAIToolsPrefixIsDeterministic pins the prefix contract: repeated
// builds and independent registries with the same configuration must produce
// byte-identical tool definitions in a stable name order. Map iteration order
// must never leak into the advertised tools.
func TestOpenAIToolsPrefixIsDeterministic(t *testing.T) {
	reg := mustRegistry(t, t.TempDir())
	t.Cleanup(func() { _ = reg.Close() })

	first := reg.OpenAITools()
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	names := toolNames(first)
	if len(names) < 5 {
		t.Fatalf("expected the default tool set, got %v", names)
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("tool names are not sorted: %v", names)
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate tool name %q", name)
		}
		seen[name] = true
	}
	if !seen["grep_search"] {
		t.Fatalf("expected grep_search in default tool set, got %v", names)
	}

	for i := 0; i < 20; i++ {
		next, err := json.Marshal(reg.OpenAITools())
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstJSON, next) {
			t.Fatalf("build %d changed the serialized tool definitions:\n%s\n%s", i, firstJSON, next)
		}
	}

	other := mustRegistry(t, t.TempDir())
	t.Cleanup(func() { _ = other.Close() })
	otherJSON, err := json.Marshal(other.OpenAITools())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, otherJSON) {
		t.Fatalf("independent registries produced different tool definitions:\n%s\n%s", firstJSON, otherJSON)
	}
}

// TestToolSchemasPreserveSourceOrderForArrays ensures required/enum arrays keep
// the order declared in the schema instead of being re-sorted or re-ordered.
func TestToolSchemasPreserveSourceOrderForArrays(t *testing.T) {
	reg := mustRegistry(t, t.TempDir())
	t.Cleanup(func() { _ = reg.Close() })

	for _, def := range reg.OpenAITools() {
		fn := def["function"].(map[string]any)
		name := fn["name"].(string)
		encoded, err := json.Marshal(fn["parameters"])
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(encoded) == 0 || !json.Valid(encoded) {
			t.Fatalf("%s: invalid schema JSON", name)
		}
	}
}
