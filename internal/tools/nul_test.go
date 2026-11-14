package tools

import "testing"

func TestResolveSafePathRejectsNULByte(t *testing.T) {
	r, err := NewRegistry(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	for _, p := range []string{"a\x00b.txt", "sub/\x00", "\x00"} {
		if got, err := r.resolveSafePath(p); err == nil {
			t.Errorf("NUL path %q accepted, resolved to %q", p, got)
		}
	}
}
