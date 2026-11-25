package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

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
	// Should skip all the rejected ones and find "valid output 1"
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

func TestExtractLastCodeBlock_Variations(t *testing.T) {
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

func TestSizeLimits(t *testing.T) {
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

	// Valid size for OSC 52
	res, err = copyToClipboard(context.Background(), "small text", oscEnv, mockRunner)
	if err != nil {
		t.Fatalf("expected OSC 52 success, got %v", err)
	}
	if res.Method != "OSC 52" || !res.Unconfirmed {
		t.Fatalf("expected unconfirmed OSC 52 result, got %+v", res)
	}

	// Over 75 KiB base64 limit
	largeOSCText := strings.Repeat("B", maxOSC52RawBytes+10)
	_, err = copyToClipboard(context.Background(), largeOSCText, systemEnv{
		isTTY:     true,
		ttyWriter: &ttyBuf,
	}, func(ctx context.Context, prog string, args []string, text string) error {
		return errors.New("tool unavailable")
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds terminal size limit") {
		t.Fatalf("expected OSC 52 payload limit rejection, got %v", err)
	}
}

func TestOSC52Formatting(t *testing.T) {
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
}

func TestPlatformPriorityRouting(t *testing.T) {
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

	// 4. macOS
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "darwin"}, mockRunner)
	if err != nil || res.Method != "pbcopy" {
		t.Fatalf("expected pbcopy, got %+v (err: %v)", res, err)
	}

	// 5. Windows
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{goos: "windows"}, mockRunner)
	if err != nil || res.Method != "clip.exe" {
		t.Fatalf("expected clip.exe, got %+v (err: %v)", res, err)
	}

	// 6. SSH remote indicator when OSC 52 not available
	attempted = nil
	res, err = copyToClipboard(context.Background(), "test", systemEnv{
		goos:      "linux",
		isWayland: true,
		isSSH:     true,
		isTTY:     false, // no TTY -> cannot OSC 52
	}, mockRunner)
	if err != nil || !res.RemoteSSH {
		t.Fatalf("expected RemoteSSH to be true, got %+v (err: %v)", res, err)
	}
}

func TestTimeoutAndCancellation(t *testing.T) {
	// Test context timeout / cancellation stops execution
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled

	_, err := copyToClipboard(ctx, "test", systemEnv{goos: "linux", isWayland: true}, func(ctx context.Context, prog string, args []string, text string) error {
		return ctx.Err()
	})
	if err == nil {
		t.Fatal("expected cancellation error, got nil")
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
