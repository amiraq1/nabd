package tools

import (
	"bytes"
	"strings"
	"testing"
)

func TestBoundedCaptureCapsMemory(t *testing.T) {
	capture := newBoundedCapture()
	_, _ = capture.Write([]byte("HEAD-MARKER"))
	_, _ = capture.Write(bytes.Repeat([]byte("x"), 8<<20))
	_, _ = capture.Write([]byte("TAIL-MARKER"))

	got := capture.String()
	if !capture.Truncated() {
		t.Fatal("capture did not report truncation")
	}
	if len(got) > maxGoVerifyCapture+len(captureTruncationMarker)+64 {
		t.Fatalf("capture kept %d bytes, want at most %d", len(got), maxGoVerifyCapture+len(captureTruncationMarker)+64)
	}
	if !strings.HasPrefix(got, "HEAD-MARKER") {
		t.Fatal("capture lost the beginning")
	}
	if !strings.HasSuffix(got, "TAIL-MARKER") {
		t.Fatal("capture lost the end")
	}
}

func TestBoundedCaptureNoTruncation(t *testing.T) {
	capture := newBoundedCapture()
	_, _ = capture.Write([]byte("hello"))
	if got := capture.String(); got != "hello" {
		t.Fatalf("capture = %q, want hello", got)
	}
	if capture.Truncated() {
		t.Fatal("small capture reported truncation")
	}
}

func TestSummarizeGoTestCaptureNotice(t *testing.T) {
	truncated := summarizeGoTestCapture("FAIL", "--- FAIL: TestX\nboom\n", nil, true)
	if !strings.Contains(truncated, "capture truncated") {
		t.Fatalf("truncated summary missing notice: %q", truncated)
	}

	plain := summarizeGoTestCapture("PASS", "ok\n", nil, false)
	if strings.Contains(plain, "capture truncated") {
		t.Fatalf("plain summary has a truncation notice: %q", plain)
	}
}
