package tools

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidatePkgPattern(t *testing.T) {
	root, reg := newTestRegistry(t)
	if err := os.Mkdir(filepath.Join(root, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "./...", false},
		{".", ".", false},
		{"./", ".", false},
		{"./...", "./...", false},
		{"./pkg", "./pkg", false},
		{"./pkg/...", "./pkg/...", false},
		{"std", "", true},
		{"github.com/a/b", "", true},
		{"../x", "", true},
		{"./../x", "", true},
		{"./..", "", true},
		{"-run", "", true},
		{"/abs", "", true},
	}
	for _, tc := range cases {
		got, err := reg.validatePkgPattern(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("validatePkgPattern(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("validatePkgPattern(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("validatePkgPattern(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSummarizeGoTest(t *testing.T) {
	pass := summarizeGoTest("PASS", "ok  example.com/a  0.1s\nok  example.com/b  0.2s", 0, "", nil)
	if !strings.HasPrefix(pass, "PASS") || !strings.Contains(pass, "ok  example.com/a") {
		t.Fatalf("pass summary = %q", pass)
	}
	if strings.Contains(pass, "bytes omitted") {
		t.Fatalf("complete output must not report omitted bytes: %q", pass)
	}

	fail := summarizeGoTest("FAIL",
		"--- FAIL: TestA (a_test.go:12)\n    a_test.go:13: boom\nFAIL\nexit status 1", 0, "",
		errors.New("exit status 1"))
	for _, want := range []string{"FAIL", "--- FAIL: TestA", "a_test.go:13", "exit status 1"} {
		if !strings.Contains(fail, want) {
			t.Errorf("fail summary missing %q:\n%s", want, fail)
		}
	}

	build := summarizeGoTest("FAIL", "# example\n./x.go:3: undefined: y\nFAIL", 0, "", errors.New("exit status 1"))
	if !strings.Contains(build, "./x.go:3") {
		t.Errorf("build summary missing file:line:\n%s", build)
	}

	timeout := summarizeGoTest("FAIL (timeout)", "", 0, "", context.DeadlineExceeded)
	if !strings.Contains(timeout, "timeout") || !strings.Contains(timeout, "no output") {
		t.Errorf("timeout summary = %q", timeout)
	}

	long := summarizeGoTest("FAIL", strings.Repeat("x", 10000), 0, "", nil)
	if len(long) > maxOutputBytes {
		t.Errorf("long summary = %d bytes", len(long))
	}
	if !strings.Contains(long, "...") {
		t.Errorf("long line was not clipped")
	}
}

// TestIsGoTestSignalIgnoresRoutineNoise pins the signal classifier: a routine
// "go: downloading ..." line must not consume one of the few signal slots.
func TestIsGoTestSignalIgnoresRoutineNoise(t *testing.T) {
	signals := []string{
		"FAIL",
		"FAIL\texample.com/a\t0.1s",
		"--- FAIL: TestA (a_test.go:1)",
		"panic: boom",
		"./x.go:3: undefined: y",
		"cannot find package",
		"FAIL\texample.com/a [build failed]",
		"go: build error",
	}
	for _, line := range signals {
		if !isGoTestSignal(line) {
			t.Errorf("expected %q to be a signal", line)
		}
	}
	for _, line := range []string{
		"go: downloading example.com/dep v1.0.0",
		"go: downloading github.com/x/y",
		"=== RUN   TestA",
		"--- PASS: TestA (0.00s)",
		"ok  \texample.com/a\t0.1s",
		"noise from a very chatty test",
	} {
		if isGoTestSignal(line) {
			t.Errorf("expected %q not to be a signal", line)
		}
	}
}

// TestSummarizeGoTestKeepsHeadAndSignalsWhenOutputIsTruncated is the regression
// guard for the truncation bug: the summary was built from the flattened
// bounded buffer, where the omission marker sits between the two retained
// halves and can itself be clipped away, so both the first line of the output
// and the failure markers disappeared.
func TestSummarizeGoTestKeepsHeadAndSignalsWhenOutputIsTruncated(t *testing.T) {
	out := newBoundedOutput(64 << 10)
	if _, err := out.Write([]byte("go: downloading example.com/dep\n--- FAIL: TestFirst (first_test.go:1)\n")); err != nil {
		t.Fatal(err)
	}
	// A large middle section that pushes the opening lines out of the buffer.
	if _, err := out.Write([]byte(strings.Repeat("noise from a very chatty test\n", 40000))); err != nil {
		t.Fatal(err)
	}
	if _, err := out.Write([]byte("--- FAIL: TestLast (last_test.go:2)\nFAIL\nbuild failed\nexit status 1\n")); err != nil {
		t.Fatal(err)
	}

	head, omitted, tail := out.segments()
	if omitted <= 0 {
		t.Fatalf("expected dropped bytes, got %d", omitted)
	}
	summary := summarizeGoTest("FAIL", string(head), omitted, string(tail), errors.New("exit status 1"))

	for _, want := range []string{
		"go: downloading example.com/dep", // very first line of the stream
		"--- FAIL: TestFirst",             // failure from the retained head
		"--- FAIL: TestLast",              // failure from the retained tail
		"FAIL",
		"build failed",
		"... [", "bytes omitted] ...", // the omission marker must survive
		"--- head ---", "--- tail ---",
	} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary lost %q:\n%s", want, summary)
		}
	}
	if len(summary) > maxOutputBytes {
		t.Errorf("summary = %d bytes, exceeds %d", len(summary), maxOutputBytes)
	}
	noise := strings.Count(summary, "noise from a very chatty test")
	if noise == 0 || noise >= 4000 {
		t.Errorf("summary should keep a bounded slice of the noise, kept %d of 4000 lines", noise)
	}
}

func TestGoVerifyTimeout(t *testing.T) {
	root, reg := newTestRegistry(t)
	reg.allowUnsandboxedForTest = true
	writeTestFile(t, filepath.Join(root, "go.mod"), "module audittimeout\n\ngo 1.22\n")
	writeTestFile(t, filepath.Join(root, "slow_test.go"), `package audittimeout

import (
	"testing"
	"time"
)

func TestSlow(t *testing.T) {
	time.Sleep(10 * time.Second)
}
`)

	start := time.Now()
	out, err := reg.runGoVerify(context.Background(), "./...", 1*time.Second)
	if err != nil {
		t.Fatalf("runGoVerify: %v", err)
	}
	if !strings.Contains(out, "timeout") {
		t.Fatalf("timeout summary = %q", out)
	}
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Fatalf("timeout took %v, process tree may still be running", elapsed)
	}
}

func TestGoVerifyTestFailureIsNotToolError(t *testing.T) {
	root, reg := newTestRegistry(t)
	reg.allowUnsandboxedForTest = true
	writeTestFile(t, filepath.Join(root, "go.mod"), "module auditfail\n\ngo 1.22\n")
	writeTestFile(t, filepath.Join(root, "fail_test.go"), `package auditfail

import "testing"

func TestFail(t *testing.T) {
	t.Fatal("expected failure")
}
`)

	out := runToolApproved(t, reg, "go_verify", map[string]any{"pkg": "./..."})
	if strings.HasPrefix(out, "Error:") {
		t.Fatalf("test failure was reported as a tool error:\n%s", out)
	}
	if !strings.HasPrefix(out, "FAIL") || !strings.Contains(out, "--- FAIL: TestFail") {
		t.Fatalf("structured failure missing:\n%s", out)
	}
}

func TestGoVerifyFailsClosedWithoutSandbox(t *testing.T) {
	root, reg := newTestRegistry(t)
	writeTestFile(t, filepath.Join(root, "go.mod"), "module auditfailclosed\n\ngo 1.22\n")
	writeTestFile(t, filepath.Join(root, "canary_test.go"), `package auditfailclosed

import (
	"os"
	"testing"
)

func init() {
	_ = os.WriteFile("executed.txt", []byte("ran"), 0o644)
}

func TestNothing(t *testing.T) {}
`)

	if _, err := reg.goVerifyCommand(context.Background(), "./..."); err == nil || !strings.Contains(err.Error(), "no proven execution sandbox") {
		t.Fatalf("goVerifyCommand without sandbox = %v, want fail-closed sandbox error", err)
	}

	out := runToolApproved(t, reg, "go_verify", map[string]any{"pkg": "./..."})
	if !strings.HasPrefix(out, "Error:") || !strings.Contains(out, "no proven execution sandbox") {
		t.Fatalf("go_verify without sandbox = %q, want fail-closed error", out)
	}
	if _, err := os.Stat(filepath.Join(root, "executed.txt")); err == nil {
		t.Fatal("go_verify executed code without an active sandbox")
	}
}
