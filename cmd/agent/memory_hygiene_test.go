package main

import (
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func TestParseMemoryLimit(t *testing.T) {
	tests := []struct {
		input     string
		wantBytes int64
		wantErr   bool
	}{
		// Valid documented units
		{"64MiB", 64 * 1024 * 1024, false},
		{"128M", 128 * 1000 * 1000, false},
		{"128MB", 128 * 1000 * 1000, false},
		{"1GiB", 1024 * 1024 * 1024, false},
		{"1GB", 1000 * 1000 * 1000, false},
		{"32MiB", 32 * 1024 * 1024, false},        // exact minimum
		{"16GiB", 16 * 1024 * 1024 * 1024, false}, // exact maximum
		{"40000KiB", 40000 * 1024, false},
		{"40000K", 40000 * 1000, false},
		{"100000000B", 100000000, false},
		{"  64MiB  ", 64 * 1024 * 1024, false},

		// Invalid values
		{"", 0, true},
		{"   ", 0, true},
		{"0", 0, true},
		{"-64MiB", 0, true},
		{"10MiB", 0, true},  // below 32 MiB min
		{"31MiB", 0, true},  // below 32 MiB min
		{"17GiB", 0, true},  // above 16 GiB max
		{"32GiB", 0, true},  // above 16 GiB max
		{"1.5GiB", 0, true}, // decimal
		{"64.0MiB", 0, true},
		{"abc", 0, true},
		{"64XYZ", 0, true},
		{"999999999999999999999GiB", 0, true}, // overflow
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseMemoryLimit(tc.input)
			if (err != nil) != tc.wantErr {
				t.Fatalf("parseMemoryLimit(%q) error = %v, wantErr %v", tc.input, err, tc.wantErr)
			}
			if !tc.wantErr && got != tc.wantBytes {
				t.Fatalf("parseMemoryLimit(%q) = %d, want %d", tc.input, got, tc.wantBytes)
			}
		})
	}
}

func TestApplyMemoryLimitFromEnv(t *testing.T) {
	// Must not be parallel as SetMemoryLimit affects the whole process
	origLimit := debug.SetMemoryLimit(-1)
	defer debug.SetMemoryLimit(origLimit)

	// 1. Not set in environment
	_ = os.Unsetenv("NABD_MEM_LIMIT")
	bytes, err := applyMemoryLimitFromEnv()
	if err != nil || bytes != 0 {
		t.Fatalf("expected (0, nil) when unset, got (%d, %v)", bytes, err)
	}

	// 2. Set but empty
	t.Setenv("NABD_MEM_LIMIT", "")
	_, err = applyMemoryLimitFromEnv()
	if err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("expected empty error, got %v", err)
	}

	// 3. Set with valid limit
	t.Setenv("NABD_MEM_LIMIT", "64MiB")
	bytes, err = applyMemoryLimitFromEnv()
	if err != nil || bytes != 64*1024*1024 {
		t.Fatalf("expected 64MiB applied, got %d (err: %v)", bytes, err)
	}
	current := debug.SetMemoryLimit(-1)
	if current != 64*1024*1024 {
		t.Fatalf("expected Go runtime limit 64MiB, got %d", current)
	}

	// 4. Set with invalid limit
	t.Setenv("NABD_MEM_LIMIT", "invalid")
	_, err = applyMemoryLimitFromEnv()
	if err == nil {
		t.Fatal("expected error on invalid NABD_MEM_LIMIT, got nil")
	}
}

func TestCompactToolOutputs(t *testing.T) {
	// Build a scenario with:
	// - System message
	// - Old tool call unit (assistant + tool result of 2000 bytes) -> SHOULD be compacted
	// - Another old tool call unit (assistant + tool result of 500 bytes) -> NOT compacted (below 1 KiB)
	// - Protected tool call unit 1 (assistant + tool result of 3000 bytes) -> PROTECTED (last 2 units)
	// - Protected tool call unit 2 (assistant + tool result of 4000 bytes) -> PROTECTED (last 2 units and last tool in session)

	largeContent := strings.Repeat("ABCDEFGH12345678", 125) // 2000 bytes
	smallContent := "small result of 50 bytes"
	protectedContent1 := strings.Repeat("P1-Content-Bytes", 150) // 2400 bytes
	lastToolContent := strings.Repeat("LAST-TOOL-OUTPUT", 200)   // 3200 bytes

	msgs := []Message{
		{Role: "system", Content: "sys"},
		// Unit 1 (old, eligible for compaction)
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc1"}}},
		{Role: "tool", ToolCallID: "tc1", Content: largeContent},
		// Unit 2 (old, small, not eligible)
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc2"}}},
		{Role: "tool", ToolCallID: "tc2", Content: smallContent},
		// Unit 3 (completed tool unit, protected as 1 of last 2)
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc3"}}},
		{Role: "tool", ToolCallID: "tc3", Content: protectedContent1},
		// Unit 4 (completed tool unit, protected as last tool output & 1 of last 2)
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc4"}}},
		{Role: "tool", ToolCallID: "tc4", Content: lastToolContent},
	}

	compacted, reduced := compactToolOutputs(msgs)
	if compacted != 1 {
		t.Fatalf("expected 1 compacted output, got %d", compacted)
	}
	if reduced <= 0 {
		t.Fatalf("expected positive reduced bytes, got %d", reduced)
	}

	// Verify Unit 1 tool output was compacted
	if !strings.Contains(msgs[2].Content, "... [compacted from 2000 bytes to save memory]") {
		t.Fatalf("Unit 1 was not properly compacted: %q", msgs[2].Content)
	}
	if len(msgs[2].Content) > maxSnippetBytes+100 {
		t.Fatalf("Unit 1 content length unexpectedly large: %d", len(msgs[2].Content))
	}

	// Verify Unit 2 tool output was untouched (small)
	if msgs[4].Content != smallContent {
		t.Fatalf("Unit 2 was modified: %q", msgs[4].Content)
	}

	// Verify Unit 3 was protected (part of last 2 tool units)
	if msgs[6].Content != protectedContent1 {
		t.Fatalf("Unit 3 was modified: %q", msgs[6].Content)
	}

	// Verify Unit 4 was protected (last tool output in session)
	if msgs[8].Content != lastToolContent {
		t.Fatalf("Unit 4 (last tool) was modified: %q", msgs[8].Content)
	}

	// Idempotency: run again on the same messages
	compacted2, reduced2 := compactToolOutputs(msgs)
	if compacted2 != 0 || reduced2 != 0 {
		t.Fatalf("expected 0 compacted on second run, got count=%d, reduced=%d", compacted2, reduced2)
	}
}

func TestCompactToolOutputs_ArabicUTF8Boundary(t *testing.T) {
	// Ensure multi-byte Arabic runes across the 256-byte boundary are not broken
	arabicSnippet := "هذا النص العربي مكرر لفحص سلامة التشفير ومحارف اليونيكود عند قطع السلسلة. "
	var b strings.Builder
	for b.Len() < 2000 {
		b.WriteString(arabicSnippet)
	}
	rawArabic := b.String()

	msgs := []Message{
		{Role: "system", Content: "sys"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc1"}}},
		{Role: "tool", ToolCallID: "tc1", Content: rawArabic},
		// Add two dummy completed tool units after it so tc1 is eligible for compaction
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc2"}}},
		{Role: "tool", ToolCallID: "tc2", Content: "t2"},
		{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "tc3"}}},
		{Role: "tool", ToolCallID: "tc3", Content: "t3"},
	}

	compacted, _ := compactToolOutputs(msgs)
	if compacted != 1 {
		t.Fatalf("expected 1 compacted, got %d", compacted)
	}

	res := msgs[2].Content
	if !utf8.ValidString(res) {
		t.Fatalf("compacted Arabic content is invalid UTF-8!")
	}
	if !strings.Contains(res, fmt.Sprintf("... [compacted from %d bytes to save memory]", len(rawArabic))) {
		t.Fatalf("missing correct Arabic compaction marker: %s", res)
	}
}

func TestPrunePromptHistory(t *testing.T) {
	history := make([]string, 250)
	for i := 0; i < 250; i++ {
		history[i] = fmt.Sprintf("command-%d", i)
	}

	pruned := prunePromptHistory(history, 200)
	if len(pruned) != 200 {
		t.Fatalf("expected 200 entries, got %d", len(pruned))
	}
	if pruned[0] != "command-50" {
		t.Fatalf("expected oldest kept to be command-50, got %s", pruned[0])
	}
	if pruned[199] != "command-249" {
		t.Fatalf("expected newest to be command-249, got %s", pruned[199])
	}

	// Under limit
	short := []string{"cmd1", "cmd2"}
	notPruned := prunePromptHistory(short, 200)
	if len(notPruned) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(notPruned))
	}
}

func TestPruneSessionMessages_AtomicUnits(t *testing.T) {
	// Build 25 atomic units:
	// Unit 0: System message
	// Units 1..24: Assistant + Tool call (multi-message units)
	msgs := []Message{
		{Role: "system", Content: "sys prompt"},
	}

	for i := 1; i <= 24; i++ {
		id := fmt.Sprintf("tc-%d", i)
		msgs = append(msgs,
			Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: id}}},
			Message{Role: "tool", ToolCallID: id, Content: fmt.Sprintf("result-%d", i)},
		)
	}

	// Limit to 10 units
	pruned := pruneSessionMessages(msgs, 10, false)
	units := groupMessageUnits(pruned)
	if len(units) != 10 {
		t.Fatalf("expected 10 units, got %d", len(units))
	}

	// First unit must still be the system message
	if units[0][0].Role != "system" {
		t.Fatalf("expected system message first, got %v", units[0][0])
	}

	// Each subsequent unit must remain atomic (assistant + tool)
	for idx, u := range units[1:] {
		if len(u) != 2 || u[0].Role != "assistant" || u[1].Role != "tool" {
			t.Fatalf("unit %d broken: %+v", idx+1, u)
		}
	}

	// Do not prune when isPending is true
	notPruned := pruneSessionMessages(msgs, 10, true)
	if len(notPruned) != len(msgs) {
		t.Fatalf("expected no pruning when isPending is true, got %d vs %d", len(notPruned), len(msgs))
	}
}

func TestFormatMemoryDiagnostics(t *testing.T) {
	status := formatMemoryStatus()
	if !strings.Contains(status, "Memory: HeapAlloc:") {
		t.Fatalf("expected 'Memory: HeapAlloc:' in status, got %q", status)
	}
	if !strings.Contains(status, "Go soft limit:") {
		t.Fatalf("expected 'Go soft limit:' in status, got %q", status)
	}
}

func TestBubbleTeaCompactCommand(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	m := initialModel(reg, &mockClient{})

	// Add old large tool message and a newer tool message
	largeData := strings.Repeat("DATA-BLOCK-", 200) // 2200 bytes
	m.messages = append(m.messages,
		Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "old"}}},
		Message{Role: "tool", ToolCallID: "old", Content: largeData},
		// Protected recent units
		Message{Role: "assistant", Content: "", ToolCalls: []ToolCall{{ID: "mid"}}},
		Message{Role: "tool", ToolCallID: "mid", Content: "mid"},
		Message{Role: "assistant", Content: "Last assistant reply", ToolCalls: []ToolCall{{ID: "latest"}}},
		Message{Role: "tool", ToolCallID: "latest", Content: "latest tool output"},
	)

	// 1. Test /compact command execution
	m.textArea.SetValue("/compact")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	um := updated.(uiModel)
	if cmd != nil {
		t.Fatal("expected nil tea.Cmd for local /compact")
	}

	lastMsg := um.messages[len(um.messages)-1]
	if !strings.Contains(lastMsg.Content, "Compacted 1 tool outputs") {
		t.Fatalf("expected compacted status message, got: %v", lastMsg)
	}
	if !strings.Contains(lastMsg.Content, "HeapAlloc:") {
		t.Fatalf("expected HeapAlloc in message, got: %v", lastMsg)
	}

	// 2. Verify /copy still accesses the full content of the last tool output and last reply
	text, err := findCopyContent(um.messages, targetTool)
	if err != nil || text != "latest tool output" {
		t.Fatalf("expected 'latest tool output', got %q (err: %v)", text, err)
	}
	replyText, err := findCopyContent(um.messages, targetReply)
	if err != nil || replyText != "Last assistant reply" {
		t.Fatalf("expected 'Last assistant reply', got %q (err: %v)", replyText, err)
	}

	// 3. Test /compact while operation/approval in progress
	um.awaitingApproval = true
	updated, _ = um.executeLocalCommand("/compact")
	um = updated.(uiModel)
	lastMsg = um.messages[len(um.messages)-1]
	if !strings.Contains(lastMsg.Content, "Cannot compact while an operation or approval is in progress") {
		t.Fatalf("expected busy rejection message, got: %v", lastMsg)
	}
}

func BenchmarkMemoryHygieneCompact(b *testing.B) {
	// Build a simulated long session with 50 iterations of tool outputs (each 4 KiB)
	baseMessages := []Message{
		{Role: "system", Content: "sys prompt"},
	}

	chunk := strings.Repeat("x", 4096)
	for i := 0; i < 50; i++ {
		id := fmt.Sprintf("call-%d", i)
		baseMessages = append(baseMessages,
			Message{Role: "assistant", Content: fmt.Sprintf("Iteration %d response", i), ToolCalls: []ToolCall{{ID: id}}},
			Message{Role: "tool", ToolCallID: id, Content: chunk},
		)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Clone slice of messages
		msgs := make([]Message, len(baseMessages))
		copy(msgs, baseMessages)

		compacted, reduced := compactToolOutputs(msgs)
		if compacted == 0 || reduced == 0 {
			b.Fatalf("expected positive compaction, got compacted=%d, reduced=%d", compacted, reduced)
		}
	}
}
