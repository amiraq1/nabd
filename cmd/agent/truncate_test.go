package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// بايت غير صالح في بداية النص لا يجب أن يقصّ الناتج كله
func TestTruncateInvalidBytesNotCutEarly(t *testing.T) {
	if got, want := truncate("ab\xffcdefgh", 6), "ab\xffcde..."; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// كل نقطة قطع ممكنة في نص عربي تعطي بادئة سليمة
func TestTruncateEveryBoundary(t *testing.T) {
	s := "مرحبا بالعالم nabd"
	for n := 0; n <= len(s)+1; n++ {
		cut := strings.TrimSuffix(truncate(s, n), "...")
		if !utf8.ValidString(cut) || !strings.HasPrefix(s, cut) {
			t.Fatalf("n=%d produced bad cut %q", n, cut)
		}
	}
}
