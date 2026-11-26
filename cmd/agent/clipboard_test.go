package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf16"

	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func TestFindCopyContent_Reply(t *testing.T) {
	// 1. Empty messages
	_, err := findCopyContent(nil, targetReply)
	if !errors.Is(err, errNothingToCopy) {
		t.Fatalf("expected errNothingToCopy, got %v", err)
	}

	// 2. Only user and system messages
	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hello"},
	}
	_, err = findCopyContent(msgs, targetReply)
	if !errors.Is(err, errNothingToCopy) {
		t.Fatalf("expected errNothingToCopy, got %v", err)
	}

	// 3. Assistant with tool calls only (no text) followed by older assistant with text
	msgs = []Message{
		{Role: "assistant", Content: "first reply"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "1"}}},
	}
	got, err := findCopyContent(msgs, targetReply)
	if err != nil || got != "first reply" {
		t.Fatalf("expected 'first reply', got %q (err: %v)", got, err)
	}

	// 4. Multiple assistant replies -> returns the last one with text
	msgs = append(msgs, Message{Role: "assistant", Content: "second reply"})
	got, err = findCopyContent(msgs, targetReply)
	if err != nil || got != "second reply" {
		t.Fatalf("expected 'second reply', got %q (err: %v)", got, err)
	}
}

func TestFindCopyContent_Code(t *testing.T) {
	// 1. Assistant has no code
	msgs := []Message{
		{Role: "assistant", Content: "Just plain text here."},
	}
	_, err := findCopyContent(msgs, targetCode)
	if !errors.Is(err, errNoCodeBlock) {
		t.Fatalf("expected errNoCodeBlock, got %v", err)
	}

	// 2. Older assistant had code, but last assistant has no code -> does NOT search older replies
	msgs = []Message{
		{Role: "assistant", Content: "```go\npackage old\n```"},
		{Role: "assistant", Content: "Just plain text in the last reply."},
	}
	_, err = findCopyContent(msgs, targetCode)
	if !errors.Is(err, errNoCodeBlock) {
		t.Fatalf("expected errNoCodeBlock, got %v", err)
	}

	// 3. Last assistant has code
	msgs = []Message{
		{Role: "assistant", Content: "Here is code:\n```go\nfmt.Println(\"hi\")\n```\nEnjoy!"},
	}
	got, err := findCopyContent(msgs, targetCode)
	if err != nil || got != "fmt.Println(\"hi\")" {
		t.Fatalf("expected 'fmt.Println(\"hi\")', got %q (err: %v)", got, err)
	}
}

func TestFindCopyContent_Tool(t *testing.T) {
	// 1. No tool messages
	msgs := []Message{
		{Role: "assistant", Content: "hi"},
	}
	_, err := findCopyContent(msgs, targetTool)
	if !errors.Is(err, errNoToolOutput) {
		t.Fatalf("expected errNoToolOutput, got %v", err)
	}

	// 2. Blocked / rejected tool outputs ignored
	msgs = []Message{
		{Role: "tool", Content: "valid output 1", Display: "read_file -> 14 bytes"},
		{Role: "tool", Content: "Tool output disclosure rejected by user.", Display: "Tool output disclosure rejected"},
		{Role: "tool", Content: "Tool disclosure blocked: unsafe.", Display: "Tool disclosure blocked"},
		{Role: "tool", Content: "Rejected by user.", Display: "Rejected edit_file"},
		{Role: "tool", Content: "Error: file changed since approval preview; retry.", Display: "Blocked: file changed"},
	}
	got, err := findCopyContent(msgs, targetTool)
	if err != nil || got != "valid output 1" {
		t.Fatalf("expected 'valid output 1', got %q (err: %v)", got, err)
	}

	// 3. New valid tool message appended
	msgs = append(msgs, Message{Role: "tool", Content: "valid output 2", Display: "go_verify -> PASS"})
	got, err = findCopyContent(msgs, targetTool)
	if err != nil || got != "valid output 2" {
		t.Fatalf("expected 'valid output 2', got %q (err: %v)", got, err)
	}
}

func TestCopyExtractLastCodeBlock_Variations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantCode string
		wantOk   bool
	}{
		{
			name:     "single block with lang",
			input:    "Intro\n```go\nfunc main() {}\n```\nOutro",
			wantCode: "func main() {}",
			wantOk:   true,
		},
		{
			name:     "single block without lang",
			input:    "Intro\n```\nplain code line\n```",
			wantCode: "plain code line",
			wantOk:   true,
		},
		{
			name:     "multiple blocks returns last",
			input:    "```python\nprint(1)\n```\nmiddle\n```go\nprintln(2)\n```",
			wantCode: "println(2)",
			wantOk:   true,
		},
		{
			name:     "longer fence with inner backticks",
			input:    "````markdown\nHere is a block:\n```go\ncode\n```\n````",
			wantCode: "Here is a block:\n```go\ncode\n```",
			wantOk:   true,
		},
		{
			name:     "unclosed block at EOF is ignored",
			input:    "```go\nclosed\n```\n```python\nunclosed block",
			wantCode: "closed",
			wantOk:   true,
		},
		{
			name:     "only unclosed block",
			input:    "```go\nunclosed block only",
			wantCode: "",
			wantOk:   false,
		},
		{
			name:     "empty code block",
			input:    "```\n```",
			wantCode: "",
			wantOk:   true,
		},
		{
			name:     "arabic text in code block",
			input:    "```text\nمرحباً بالعالم\nسطر ثانٍ مع رموز !@#$\n```",
			wantCode: "مرحباً بالعالم\nسطر ثانٍ مع رموز !@#$",
			wantOk:   true,
		},
		{
			name:     "CRLF normalized lines",
			input:    "```go\r\nline1\r\nline2\r\n```",
			wantCode: "line1\nline2",
			wantOk:   true,
		},
		{
			name:     "no code blocks",
			input:    "just text without backticks",
			wantCode: "",
			wantOk:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := extractLastCodeBlock(tc.input)
			if ok != tc.wantOk {
				t.Fatalf("extractLastCodeBlock() ok = %v, want %v", ok, tc.wantOk)
			}
			if got != tc.wantCode {
				t.Fatalf("extractLastCodeBlock() =\n%q\nwant:\n%q", got, tc.wantCode)
			}
		})
	}
}

func TestClipboardSizeLimits(t *testing.T) {
	// 1. Text exactly 256 KiB
	exactText := strings.Repeat("A", maxClipboardSize)
	env := systemEnv{goos: "linux", isWayland: true}
	var executedText string
	mockRunner := func(ctx context.Context, prog string, args []string, text string) error {
		executedText = text
		return nil
	}

	res, err := copyToClipboard(context.Background(), exactText, env, mockRunner)
	if err != nil {
		t.Fatalf("expected success for exact 256 KiB, got %v", err)
	}
	if res.Bytes != maxClipboardSize {
		t.Fatalf("expected %d bytes, got %d", maxClipboardSize, res.Bytes)
	}
	if len(executedText) != maxClipboardSize {
		t.Fatalf("expected runner to receive exact text, got %d bytes", len(executedText))
	}

	// 2. Text exceeding 256 KiB by 1 byte
	oversizedText := strings.Repeat("A", maxClipboardSize+1)
	_, err = copyToClipboard(context.Background(), oversizedText, env, mockRunner)
	if err == nil || !strings.Contains(err.Error(), "exceeds clipboard limit") {
		t.Fatalf("expected size limit rejection, got %v", err)
	}

	// 3. OSC 52 size limits
	var ttyBuf bytes.Buffer
	oscEnv := systemEnv{
		isSSH:     true,
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}

	res, err = copyToClipboard(context.Background(), "small text", oscEnv, mockRunner)
	if err != nil {
		t.Fatalf("expected OSC 52 success, got %v", err)
	}
	if res.Method != "OSC 52" || !res.Unconfirmed {
		t.Fatalf("expected unconfirmed OSC 52 result, got %+v", res)
	}

	// Exact maxOSC52RawBytes succeeds
	ttyBuf.Reset()
	exactOSC := strings.Repeat("B", maxOSC52RawBytes)
	res, err = copyToClipboard(context.Background(), exactOSC, systemEnv{
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}, func(ctx context.Context, prog string, args []string, text string) error {
		return errors.New("tool unavailable")
	})
	if err != nil || res.Method != "OSC 52" {
		t.Fatalf("expected OSC 52 success at exact maxOSC52RawBytes, got %+v (err: %v)", res, err)
	}

	// Over 75 KiB base64 limit fails explicitly without truncation
	ttyBuf.Reset()
	largeOSCText := strings.Repeat("B", maxOSC52RawBytes+1)
	_, err = copyToClipboard(context.Background(), largeOSCText, systemEnv{
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}, func(ctx context.Context, prog string, args []string, text string) error {
		return errors.New("tool unavailable")
	})
	if !errors.Is(err, errOSC52Limit) {
		t.Fatalf("expected errOSC52Limit, got %v", err)
	}
	if ttyBuf.Len() != 0 {
		t.Fatalf("expected no partial OSC 52 output when limit exceeded, wrote %d bytes", ttyBuf.Len())
	}
}

func TestClipboardOSC52FormattingAndInjection(t *testing.T) {
	var buf bytes.Buffer
	text := "hello world"

	// Non-tmux format
	err := writeOSC52(&buf, text, false)
	if err != nil {
		t.Fatalf("writeOSC52 failed: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "\x1b]52;c;") || !strings.HasSuffix(out, "\x07") {
		t.Fatalf("unexpected OSC 52 format: %q", out)
	}

	// Tmux format
	buf.Reset()
	err = writeOSC52(&buf, text, true)
	if err != nil {
		t.Fatalf("writeOSC52 in tmux failed: %v", err)
	}
	out = buf.String()
	if !strings.HasPrefix(out, "\x1bPtmux;\x1b\x1b]52;c;") || !strings.HasSuffix(out, "\x07\x1b\\") {
		t.Fatalf("unexpected tmux OSC 52 format: %q", out)
	}

	// Injection resistance: ESC, BEL, DCS, newlines, NUL inside content must remain base64-encoded
	buf.Reset()
	malicious := "مرحبا\x1b]52;c;INJECT\x07\x1bPtmux;\x1b\\\x1b[2J\r\n\x00"
	if err := writeOSC52(&buf, malicious, false); err != nil {
		t.Fatalf("writeOSC52 malicious failed: %v", err)
	}
	raw := buf.String()
	payload := strings.TrimSuffix(strings.TrimPrefix(raw, "\x1b]52;c;"), "\x07")
	for i := 0; i < len(payload); i++ {
		c := payload[i]
		if c < 0x20 || c == 0x7f || c == 0x1b || c == 0x07 {
			t.Fatalf("control byte %#x leaked into OSC 52 payload", c)
		}
	}
	decoded, err := base64.StdEncoding.DecodeString(payload)
	if err != nil || string(decoded) != malicious {
		t.Fatalf("decoded payload mismatch: err=%v got=%q want=%q", err, string(decoded), malicious)
	}

	// detectSystemEnv: inTmux must ONLY be true when TMUX != ""
	t.Setenv("TMUX", "")
	t.Setenv("TERM", "screen-256color")
	if env := detectSystemEnv(); env.inTmux {
		t.Fatal("expected inTmux=false for TERM=screen-256color without TMUX")
	}
	t.Setenv("TERM", "tmux-256color")
	if env := detectSystemEnv(); env.inTmux {
		t.Fatal("expected inTmux=false for TERM=tmux-256color without TMUX")
	}
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	if env := detectSystemEnv(); !env.inTmux {
		t.Fatal("expected inTmux=true when TMUX is set")
	}
}

func TestClipboardPlatformPriorityRouting(t *testing.T) {
	var attempted []string
	mockRunner := func(ctx context.Context, prog string, args []string, text string) error {
		attempted = append(attempted, prog)
		if prog == "termux-clipboard-set" || prog == "wl-copy" || prog == "clip.exe" || prog == "pbcopy" {
			return nil
		}
		return errors.New("tool not found")
	}

	// 1. Termux
	attempted = nil
	res, err := copyToClipboard(context.Background(), "test", systemEnv{isTermux: true}, mockRunner)
	if err != nil || res.Method != "termux-clipboard-set" {
		t.Fatalf("expected termux-clipboard-set, got %+v (err: %v)", res, err)
	}

	// 2. Linux Wayland
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "linux", isWayland: true}, mockRunner)
	if err != nil || res.Method != "wl-copy" {
		t.Fatalf("expected wl-copy, got %+v (err: %v)", res, err)
	}

	// 3. Linux X11 fallback
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "linux", isX11: true}, func(ctx context.Context, prog string, args []string, text string) error {
		attempted = append(attempted, prog)
		if prog == "xclip" {
			return errors.New("xclip failed")
		}
		if prog == "xsel" {
			return nil
		}
		return errors.New("not found")
	})
	if err != nil || res.Method != "xsel" {
		t.Fatalf("expected fallback to xsel, got %+v (err: %v)", res, err)
	}
	if len(attempted) != 2 || attempted[0] != "xclip" || attempted[1] != "xsel" {
		t.Fatalf("expected [xclip, xsel], got %v", attempted)
	}

	// 4. Headless Linux (neither Wayland nor X11): must NOT try xclip/xsel
	attempted = nil
	var ttyBuf bytes.Buffer
	res, err = copyToClipboard(context.Background(), "test", systemEnv{
		goos:      "linux",
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}, mockRunner)
	if err != nil || res.Method != "OSC 52" {
		t.Fatalf("expected OSC 52 on headless linux, got %+v (err: %v)", res, err)
	}
	if len(attempted) != 0 {
		t.Fatalf("expected headless linux not to invoke X11/Wayland tools, attempted %v", attempted)
	}

	// 5. macOS
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "darwin"}, mockRunner)
	if err != nil || res.Method != "pbcopy" {
		t.Fatalf("expected pbcopy, got %+v (err: %v)", res, err)
	}

	// 6. Windows: only clip.exe (no duplicate "clip")
	attempted = nil
	_, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "windows"}, func(ctx context.Context, prog string, args []string, text string) error {
		attempted = append(attempted, prog)
		return errors.New("fail")
	})
	if err == nil || len(attempted) != 1 || attempted[0] != "clip.exe" {
		t.Fatalf("expected single clip.exe attempt on windows, got attempted=%v err=%v", attempted, err)
	}

	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "windows"}, mockRunner)
	if err != nil || res.Method != "clip.exe" {
		t.Fatalf("expected clip.exe, got %+v (err: %v)", res, err)
	}

	// 7. SSH remote indicator when OSC 52 not available
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{
		goos:      "linux",
		isWayland: true,
		isSSH:     true,
		isTTY:     false,
	}, mockRunner)
	if err != nil || !res.RemoteSSH {
		t.Fatalf("expected RemoteSSH to be true, got %+v (err: %v)", res, err)
	}
}

func TestClipboardTimeoutAndCancellation(t *testing.T) {
	// 1. Pre-cancelled context must NOT emit OSC 52 even when TTY is available
	var ttyBuf bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := copyToClipboard(ctx, "test", systemEnv{
		goos:      "linux",
		isWayland: true,
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}, func(ctx context.Context, prog string, args []string, text string) error {
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if ttyBuf.Len() != 0 {
		t.Fatalf("pre-cancelled copy must not write OSC 52, wrote %d bytes", ttyBuf.Len())
	}

	// 2. Local tool timeout must NOT fall back to OSC 52 or be masked by errOSC52Limit
	ttyBuf.Reset()
	timeoutCtx, timeoutCancel := context.WithCancel(context.Background())
	largeText := strings.Repeat("A", maxOSC52RawBytes+100)
	_, err = copyToClipboard(timeoutCtx, largeText, systemEnv{
		isTermux:  true,
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}, func(ctx context.Context, prog string, args []string, text string) error {
		timeoutCancel()
		return context.DeadlineExceeded
	})
	if errors.Is(err, errOSC52Limit) {
		t.Fatalf("timeout error must not be masked behind errOSC52Limit, got %v", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected wrapped cancellation/timeout error, got %v", err)
	}
	if ttyBuf.Len() != 0 {
		t.Fatalf("timed-out copy must not write OSC 52 fallback, wrote %d bytes", ttyBuf.Len())
	}
}

func TestClipboardEnvAllowlistAcrossPlatforms(t *testing.T) {
	sampleEnviron := []string{
		"PATH=/usr/bin:/bin",
		"HOME=/home/user",
		"PREFIX=/data/data/com.termux/files/usr",
		"LD_LIBRARY_PATH=/data/data/com.termux/files/usr/lib",
		"TMPDIR=/tmp",
		"LANG=en_US.UTF-8",
		"LC_ALL=en_US.UTF-8",
		"DISPLAY=:0",
		"WAYLAND_DISPLAY=wayland-0",
		"XDG_RUNTIME_DIR=/run/user/1000",
		"XAUTHORITY=/home/user/.Xauthority",
		"ANDROID_DATA=/data",
		"TERMUX_VERSION=0.119.0",
		"SystemRoot=C:\\Windows",
		"ComSpec=C:\\Windows\\System32\\cmd.exe",
		"TEMP=C:\\Temp",
		"TMP=C:\\Temp",
		"NABD_API_KEY=dummy-secret-1",
		"NABD_AUDIT_ANCHOR=dummy-secret-2",
		"NABD_FIRECRAWL_API_KEY=dummy-secret-3",
		"AWS_SECRET_ACCESS_KEY=dummy-secret-4",
		"GITHUB_TOKEN=dummy-secret-5",
		"CUSTOM_KEY=dummy-secret-6",
		"TERMUX_API_TOKEN=dummy-secret-7",
		"ANDROID_SECRET=dummy-secret-8",
	}

	platforms := []struct {
		name string
		env  systemEnv
		want []string
	}{
		{
			name: "termux",
			env:  systemEnv{isTermux: true, goos: "android"},
			want: []string{"PATH", "HOME", "PREFIX", "LD_LIBRARY_PATH", "TMPDIR", "LANG", "LC_ALL", "ANDROID_DATA", "TERMUX_VERSION"},
		},
		{
			name: "linux",
			env:  systemEnv{goos: "linux"},
			want: []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "DISPLAY", "WAYLAND_DISPLAY", "XDG_RUNTIME_DIR", "XAUTHORITY"},
		},
		{
			name: "windows",
			env:  systemEnv{goos: "windows"},
			want: []string{"PATH", "SystemRoot", "ComSpec", "TEMP", "TMP"},
		},
		{
			name: "darwin",
			env:  systemEnv{goos: "darwin"},
			want: []string{"PATH", "HOME", "TMPDIR", "LANG", "LC_ALL"},
		},
	}

	for _, tc := range platforms {
		t.Run(tc.name, func(t *testing.T) {
			got := buildClipboardEnvFrom(tc.env, sampleEnviron)
			gotKeys := make(map[string]bool)
			for _, kv := range got {
				k, _, _ := strings.Cut(kv, "=")
				gotKeys[k] = true
				upper := strings.ToUpper(k)
				if strings.HasPrefix(upper, "NABD_") || strings.HasSuffix(upper, "_KEY") || strings.HasSuffix(upper, "_SECRET") || strings.HasSuffix(upper, "_TOKEN") {
					t.Fatalf("forbidden secret variable leaked into %s child env: %s", tc.name, k)
				}
			}
			for _, w := range tc.want {
				if !gotKeys[w] {
					t.Fatalf("expected allowed key %s missing for %s (got: %v)", w, tc.name, got)
				}
			}
		})
	}
}

func TestClipboardWindowsUTF16LEBytes(t *testing.T) {
	input := "مرحباً\nHello\r\n🚀"
	got := encodeWindowsClipboardBytes(input)

	// Must begin with UTF-16LE BOM: FF FE
	if len(got) < 2 || got[0] != 0xFF || got[1] != 0xFE {
		t.Fatalf("missing UTF-16LE BOM: got %x", got[:min(len(got), 4)])
	}
	if (len(got)-2)%2 != 0 {
		t.Fatalf("UTF-16LE payload must be even byte count, got %d", len(got)-2)
	}

	// Decode little-endian uint16 code units after BOM
	units := make([]uint16, (len(got)-2)/2)
	for i := range units {
		units[i] = uint16(got[2+i*2]) | uint16(got[2+i*2+1])<<8
	}
	decoded := string(utf16.Decode(units))
	want := "مرحباً\r\nHello\r\n🚀"
	if decoded != want {
		t.Fatalf("decoded UTF-16LE mismatch:\ngot:  %q\nwant: %q", decoded, want)
	}

	// Verify CRLF bytes explicitly (0D 00 0A 00) and no doubled CR (0D 00 0D 00 0A 00)
	if !bytes.Contains(got, []byte{0x0D, 0x00, 0x0A, 0x00}) {
		t.Fatalf("expected UTF-16LE CRLF sequence in output")
	}
	if bytes.Contains(got, []byte{0x0D, 0x00, 0x0D, 0x00, 0x0A, 0x00}) {
		t.Fatalf("unexpected doubled CR in UTF-16LE output")
	}
}

func TestCopyCommandParsingAndRouting(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	client := &mockClient{}
	m := initialModel(reg, client)
	m.messages = append(m.messages,
		Message{Role: "assistant", Content: "Hi\n```go\nprintln(1)\n```"},
		Message{Role: "tool", Content: "tool-res", Display: "read_file -> 8 bytes"},
	)

	var gotTarget copyTarget
	m.clipboardFn = func(ctx context.Context, text string, target copyTarget) (clipboardResult, error) {
		gotTarget = target
		return clipboardResult{Method: "mock-clip", Bytes: len(text)}, nil
	}

	localCases := []struct {
		input      string
		wantCmd    bool
		wantTarget copyTarget
	}{
		{"/COPY", true, targetReply},
		{"/Copy   Code", true, targetCode},
		{"/copy\tcode", true, targetCode},
		{"/copy\ncode", true, targetCode},
		{"/copy\tTOOL", true, targetTool},
		{"copy", true, targetReply},
		{"COPY code", true, targetCode},
		{"/copy invalid", false, targetReply},
		{"/copy\tinvalid", false, targetReply},
		{"/copy code extra", false, targetReply},
	}

	for _, tc := range localCases {
		updated, cmd := m.executeLocalCommand(tc.input)
		um := updated.(uiModel)
		if tc.wantCmd {
			if cmd == nil {
				t.Fatalf("input %q: expected non-nil tea.Cmd", tc.input)
			}
			msg := cmd()
			resMsg, ok := msg.(clipboardResultMsg)
			if !ok || resMsg.err != nil {
				t.Fatalf("input %q: expected clipboardResultMsg, got %v", tc.input, msg)
			}
			if gotTarget != tc.wantTarget {
				t.Fatalf("input %q: got target %v, want %v", tc.input, gotTarget, tc.wantTarget)
			}
			updated, _ = um.Update(resMsg)
			m = updated.(uiModel)
		} else {
			if cmd != nil {
				t.Fatalf("input %q: expected nil tea.Cmd (usage error, not LLM call)", tc.input)
			}
			lastMsg := um.messages[len(um.messages)-1]
			if !strings.Contains(lastMsg.Content, "Usage: /copy") {
				t.Fatalf("input %q: expected usage error, got %q", tc.input, lastMsg.Content)
			}
			m = um
		}
	}

	if client.calls != 0 {
		t.Fatalf("expected 0 LLM calls for local /copy variants, got %d", client.calls)
	}

	// Natural sentence starting with "copy " should still go to the LLM
	updated, cmd := m.executeLocalCommand("copy this helper into utils.go")
	um := updated.(uiModel)
	if !um.busy || cmd == nil {
		t.Fatalf("expected natural prompt starting with 'copy ' to invoke LLM")
	}
}

func TestCopyGenerationConcurrencyAndAdvisory(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	m := initialModel(reg, &mockClient{})
	m.messages = append(m.messages,
		Message{Role: "assistant", Content: "Full reply text\n```go\nfmt.Println(42)\n```"},
	)

	var firstCtxCancelled bool
	var mu sync.Mutex
	m.clipboardFn = func(ctx context.Context, text string, target copyTarget) (clipboardResult, error) {
		if target == targetReply {
			select {
			case <-ctx.Done():
				mu.Lock()
				firstCtxCancelled = true
				mu.Unlock()
				return clipboardResult{}, ctx.Err()
			case <-time.After(500 * time.Millisecond):
				return clipboardResult{Method: "slow-stale", Bytes: len(text)}, nil
			}
		}
		return clipboardResult{
			Method:      "OSC 52",
			Bytes:       len(text),
			Unconfirmed: true,
		}, nil
	}

	// Launch first /copy (blocks until cancelled)
	updated1, cmd1 := m.executeLocalCommand("/copy")
	um1 := updated1.(uiModel)
	if cmd1 == nil {
		t.Fatal("expected cmd1 != nil")
	}

	// Launch second /copy code before first finishes -> cancels first & increments copyGen
	updated2, cmd2 := um1.executeLocalCommand("/copy code")
	um2 := updated2.(uiModel)
	if cmd2 == nil {
		t.Fatal("expected cmd2 != nil")
	}

	// Deliver stale first result after executing cmd1 (which saw cancellation)
	msg1 := cmd1()
	mu.Lock()
	cancelled := firstCtxCancelled
	mu.Unlock()
	if !cancelled {
		t.Fatal("expected first copy context to be cancelled by superseded copy")
	}

	msg2 := cmd2()

	// Deliver winning gen 2 first, then stale gen 1: stale must be ignored
	updated3, _ := um2.Update(msg2)
	um3 := updated3.(uiModel)
	if !strings.Contains(um3.status, "code block") || !strings.Contains(um3.status, "OSC 52") {
		t.Fatalf("expected OSC 52 message to identify target 'code block', got %q", um3.status)
	}
	msgCountAfterWin := len(um3.messages)

	updated4, _ := um3.Update(msg1)
	um4 := updated4.(uiModel)
	if um4.status != um3.status || len(um4.messages) != msgCountAfterWin {
		t.Fatalf("stale clipboardResultMsg mutated model state: status=%q len=%d vs %d",
			um4.status, len(um4.messages), msgCountAfterWin)
	}

	// Verify advisory warning is emitted without leaking copied secret when copying credential-like text
	secret := "sk-" + strings.Repeat("Z", 32)
	um4.messages = append(um4.messages, Message{Role: "assistant", Content: "key=" + secret})
	um4.clipboardFn = func(ctx context.Context, text string, target copyTarget) (clipboardResult, error) {
		return clipboardResult{Method: "mock-clip", Bytes: len(text)}, nil
	}
	updated5, cmd5 := um4.executeLocalCommand("/copy")
	um5 := updated5.(uiModel)
	res5 := cmd5()
	updated6, _ := um5.Update(res5)
	um6 := updated6.(uiModel)
	last := um6.messages[len(um6.messages)-1]
	if !strings.Contains(last.Content, "Advisory:") {
		t.Fatalf("expected advisory secret warning message, got %q", last.Content)
	}
	if strings.Contains(last.Content, secret) {
		t.Fatalf("secret leaked into advisory warning: %q", last.Content)
	}
}

func TestClipboardSyncTTYWriterConcurrency(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "tty-sync-*")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	w := newSyncTTYWriter(f)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				if id%2 == 0 {
					_ = writeOSC52(w, "مرحبا test payload", false)
				} else {
					_, _ = w.Write([]byte("\x1b[1;1Hframe-update\n"))
				}
			}
		}(i)
	}
	wg.Wait()

	if _, err := f.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	expectedOSC := "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte("مرحبا test payload")) + "\x07"
	if count := strings.Count(string(data), expectedOSC); count != 8*20 {
		t.Fatalf("OSC 52 writes interleaved or lost: expected %d intact sequences, got %d", 8*20, count)
	}
}

func TestBubbleTeaCopyExecution(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	m := initialModel(reg, &mockClient{})

	// Add an assistant reply and a tool reply
	m.messages = append(m.messages,
		Message{Role: "assistant", Content: "Hello world\n```python\nprint(42)\n```"},
		Message{Role: "tool", Content: "tool output data", Display: "read_file -> 16 bytes"},
	)

	// Mock clipboardFn
	var copiedText string
	var copiedTarget copyTarget
	m.clipboardFn = func(ctx context.Context, text string, target copyTarget) (clipboardResult, error) {
		copiedText = text
		copiedTarget = target
		return clipboardResult{
			Method:      "mock-clip",
			Bytes:       len(text),
			Unconfirmed: false,
		}, nil
	}

	// Test /copy
	m.textArea.SetValue("/copy")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	um := updated.(uiModel)
	if cmd == nil {
		t.Fatal("expected non-nil tea.Cmd for /copy")
	}
	msg := cmd()
	resMsg, ok := msg.(clipboardResultMsg)
	if !ok || resMsg.err != nil {
		t.Fatalf("expected successful clipboardResultMsg, got %v", msg)
	}
	if copiedTarget != targetReply || !strings.Contains(copiedText, "Hello world") {
		t.Fatalf("expected reply content, got %q (target: %v)", copiedText, copiedTarget)
	}

	// Deliver result message
	updated, _ = um.Update(resMsg)
	um = updated.(uiModel)
	lastMsg := um.messages[len(um.messages)-1]
	if !strings.Contains(lastMsg.Content, "Copied last reply") || !strings.Contains(lastMsg.Content, "mock-clip") {
		t.Fatalf("expected status message, got %v", lastMsg)
	}
	// Verify content was NOT leaked into the status message
	if strings.Contains(lastMsg.Content, "Hello world") {
		t.Fatalf("content leaked into status message: %s", lastMsg.Content)
	}

	// Test /copy code
	um.textArea.SetValue("/copy code")
	updated, cmd = um.Update(tea.KeyMsg{Type: tea.KeyEnter})
	um = updated.(uiModel)
	if cmd == nil {
		t.Fatal("expected non-nil tea.Cmd for /copy code")
	}
	msg = cmd()
	resMsg = msg.(clipboardResultMsg)
	if resMsg.err != nil || copiedText != "print(42)" {
		t.Fatalf("expected 'print(42)', got %q (err: %v)", copiedText, resMsg.err)
	}
	updated, _ = um.Update(resMsg)
	um = updated.(uiModel)

	// Test /copy tool
	um.textArea.SetValue("/copy tool")
	updated, cmd = um.Update(tea.KeyMsg{Type: tea.KeyEnter})
	um = updated.(uiModel)
	if cmd == nil {
		t.Fatal("expected non-nil tea.Cmd for /copy tool")
	}
	msg = cmd()
	resMsg = msg.(clipboardResultMsg)
	if resMsg.err != nil || copiedText != "tool output data" {
		t.Fatalf("expected 'tool output data', got %q (err: %v)", copiedText, resMsg.err)
	}
	updated, _ = um.Update(resMsg)
	um = updated.(uiModel)

	// Test invalid /copy argument
	um.textArea.SetValue("/copy invalid")
	updated, cmd = um.Update(tea.KeyMsg{Type: tea.KeyEnter})
	um = updated.(uiModel)
	if cmd != nil {
		t.Fatal("expected nil tea.Cmd for invalid /copy argument")
	}
	lastMsg = um.messages[len(um.messages)-1]
	if !strings.Contains(lastMsg.Content, "Usage: /copy") {
		t.Fatalf("expected usage message, got %v", lastMsg)
	}
}
