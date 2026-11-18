//go:build unix

package tools

import (
	"strings"
	"testing"
)

func TestGoVerifyUsesBoundedCapture(t *testing.T) {
	_, reg := newTestRegistry(t)
	fakeGo(t, `head -c 8388608 /dev/zero | tr '\0' x`)

	out := runToolApproved(t, reg, "go_verify", map[string]any{"pkg": "./..."})
	if !strings.Contains(out, "capture truncated") {
		t.Fatalf("tool summary does not show capture truncation: %.200q", out)
	}
	if len(out) > maxOutputBytes+128 {
		t.Fatalf("tool summary is %d bytes, want at most %d", len(out), maxOutputBytes+128)
	}
}
