package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/llm"
	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

func TestToolBadgesCategoriesASCIIAndNoColor(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	// Verify grep_search and undo remain purely visual badge entries, not registered tools.
	for _, entry := range reg.OpenAITools() {
		fn, _ := entry["function"].(map[string]any)
		name, _ := fn["name"].(string)
		if name == "grep_search" || name == "undo" {
			t.Fatalf("visual-only badge entry %q unexpectedly registered in tools.Registry", name)
		}
	}

	cases := []struct {
		toolName  string
		wantBadge string
		display   string
	}{
		{"read_file", "[READ]", "read_file(main.go) -> 42 bytes"},
		{"list_dir", "[LIST]", "list_dir(.) -> 4 entries"},
		{"repo_map", "[MAP]", "repo_map(.) -> 3 files, 9 symbols"},
		{"grep_search", "[GREP]", "grep_search(TODO) -> 2 matches"},
		{"create_file", "[CREATE]", "create_file(new.go) -> Successfully created new.go"},
		{"edit_file", "[EDIT]", "edit_file(main.go) -> Successfully updated main.go"},
		{"undo", "[UNDO]", "undo(main.go) -> restored"},
		{"web_search", "[WEB]", "web_search -> 3 results"},
		{"web_fetch", "[FETCH]", "web_fetch(example.com) -> 512 bytes"},
		{"go_verify", "[VERIFY]", "go_verify(./...) -> PASS"},
		{"mcp_call", "[MCP]", "mcp_call(srv:op) -> 128 bytes"},
		{"rea:inspect_binary_layout", "[MCP]", "rea:inspect_binary_layout(...) -> 256 bytes"},
		{"custom_analyzer", "[TOOL]", "custom_analyzer -> 19 bytes"},
	}

	modes := []struct {
		name      string
		env       map[string]string
		wantNoESC bool
		wantASCII bool
	}{
		{"unicode_color", map[string]string{"NABD_ASCII": "", "TERM": "xterm-256color", "NO_COLOR": ""}, false, false},
		{"no_color", map[string]string{"NABD_ASCII": "", "TERM": "xterm-256color", "NO_COLOR": "1"}, true, false},
		{"ascii_mode", map[string]string{"NABD_ASCII": "1", "TERM": "xterm-256color", "NO_COLOR": ""}, false, true},
		{"term_dumb", map[string]string{"NABD_ASCII": "", "TERM": "dumb", "NO_COLOR": ""}, true, true},
	}

	for _, mode := range modes {
		t.Run(mode.name, func(t *testing.T) {
			for k, v := range mode.env {
				t.Setenv(k, v)
			}
			for _, tc := range cases {
				msgs := []Message{
					{
						Role:      "assistant",
						ToolCalls: []ToolCall{makeTestToolCall("id_1", tc.toolName, `{}`)},
					},
					{
						Role:       "tool",
						ToolCallID: "id_1",
						Content:    "sample result",
						Display:    tc.display,
					},
				}
				out := renderMessages(msgs, 100, false)
				if mode.wantNoESC && strings.ContainsRune(out, 0x1b) {
					t.Fatalf("%s leaked ANSI escape for %s: %q", mode.name, tc.toolName, out)
				}
				plain := stripTestANSI(out)
				if !strings.Contains(plain, tc.wantBadge) {
					t.Fatalf("%s missing badge %q for %s in:\n%s", mode.name, tc.wantBadge, tc.toolName, plain)
				}
				if !strings.Contains(plain, tc.toolName) {
					t.Fatalf("%s hid tool name %q in:\n%s", mode.name, tc.toolName, plain)
				}
				if mode.wantASCII {
					for _, r := range plain {
						if r > 0x7f {
							t.Fatalf("%s leaked non-ASCII rune %U in %q", mode.name, r, plain)
						}
					}
				}
			}
		})
	}

	// Badges must not alter security warnings, rejections, or approval prompts.
	t.Run("warnings_and_approvals_preserved", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		msgs := []Message{
			{
				Role: "assistant",
				ToolCalls: []ToolCall{
					makeTestToolCall("r1", "edit_file", `{"path":"a.go"}`),
					makeTestToolCall("r2", "read_file", `{"path":"secret.txt"}`),
				},
			},
			{
				Role:       "tool",
				ToolCallID: "r1",
				Content:    "Rejected by user.",
				Display:    "Rejected edit_file",
			},
			{
				Role:       "tool",
				ToolCallID: "r2",
				Content:    "Tool output disclosure rejected by user.",
				Display:    "Tool output disclosure rejected",
			},
		}
		out := stripTestANSI(renderMessages(msgs, 80, false))
		if !strings.Contains(out, "[EDIT]") || !strings.Contains(out, "Rejected edit_file") {
			t.Fatalf("expected [EDIT] and rejection notice intact, got:\n%s", out)
		}
		if !strings.Contains(out, "[READ]") || !strings.Contains(out, "Tool output disclosure rejected") {
			t.Fatalf("expected [READ] and disclosure rejection notice intact, got:\n%s", out)
		}
		if strings.Contains(out, "(Ctrl+O)") {
			t.Fatalf("rejected tools must remain ineligible for Ctrl+O:\n%s", out)
		}
	})
}

func TestToolTreeExpansionAndHonestTruncation(t *testing.T) {
	t.Run("collapsed_vs_expanded_and_grouping_folding", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		var tcs []ToolCall
		var msgs []Message
		for i := 1; i <= 6; i++ {
			id := fmt.Sprintf("c%d", i)
			name := "read_file"
			if i == 6 {
				name = "edit_file"
			}
			tcs = append(tcs, makeTestToolCall(id, name, fmt.Sprintf(`{"path":"f%d.go"}`, i)))
			msgs = append(msgs, Message{
				Role:       "tool",
				ToolCallID: id,
				Content:    fmt.Sprintf("line 1 of f%d\nline 2 of f%d\nline 3 of f%d", i, i, i),
				Display:    fmt.Sprintf("%s(f%d.go) -> 30 bytes", name, i),
			})
		}
		conv := append([]Message{{Role: "assistant", ToolCalls: tcs}}, msgs...)

		collapsed := stripTestANSI(renderMessages(conv, 80, false))
		if !strings.Contains(collapsed, "+3 earlier tool calls") || strings.Contains(collapsed, "Full Output") || strings.Contains(collapsed, "└─ ") {
			t.Fatalf("unexpected collapsed folded rendering:\n%s", collapsed)
		}
		if !strings.Contains(collapsed, "[EDIT] edit_file(f6.go) -> 30 bytes  (Ctrl+O)") {
			t.Fatalf("collapsed latest tool missing badge or (Ctrl+O):\n%s", collapsed)
		}

		expanded := stripTestANSI(renderMessages(conv, 80, true))
		for _, want := range []string{
			"+3 earlier tool calls",
			"[EDIT] edit_file(f6.go) -> 30 bytes  (Ctrl+O to collapse)",
			"─── Full Output ───",
			"│   │ line 1 of f6",
			"│   │ line 2 of f6",
			"│   └─ line 3 of f6",
		} {
			if !strings.Contains(expanded, want) {
				t.Fatalf("expanded output missing %q:\n%s", want, expanded)
			}
		}
	})

	t.Run("empty_single_line_multiline_and_ascii", func(t *testing.T) {
		for _, ascii := range []bool{false, true} {
			if ascii {
				t.Setenv("NABD_ASCII", "1")
			} else {
				setUnicodeDisplayEnv(t)
			}
			emptyOut := stripTestANSI(renderExpandedToolTree("   \n", "read_file(empty.txt) -> 0 bytes", "│   ", 80))
			singleOut := stripTestANSI(renderExpandedToolTree("only one line", "read_file(one.txt) -> 13 bytes", "│   ", 80))
			multiOut := stripTestANSI(renderExpandedToolTree("first\nsecond\nthird\n", "read_file(m.txt) -> 18 bytes", "│   ", 80))

			if ascii {
				if !strings.Contains(emptyOut, "+- (empty output)") {
					t.Fatalf("ASCII empty output mismatch:\n%s", emptyOut)
				}
				if !strings.Contains(singleOut, "+- only one line") || strings.Contains(singleOut, "| only one line") {
					t.Fatalf("ASCII single line mismatch:\n%s", singleOut)
				}
				if !strings.Contains(multiOut, "| first") || !strings.Contains(multiOut, "| second") || !strings.Contains(multiOut, "+- third") {
					t.Fatalf("ASCII multiline mismatch:\n%s", multiOut)
				}
			} else {
				if !strings.Contains(emptyOut, "└─ (empty output)") {
					t.Fatalf("Unicode empty output mismatch:\n%s", emptyOut)
				}
				if !strings.Contains(singleOut, "└─ only one line") || strings.Contains(singleOut, "│ only one line") {
					t.Fatalf("Unicode single line mismatch:\n%s", singleOut)
				}
				if !strings.Contains(multiOut, "│ first") || !strings.Contains(multiOut, "│ second") || !strings.Contains(multiOut, "└─ third") {
					t.Fatalf("Unicode multiline mismatch:\n%s", multiOut)
				}
			}
		}
	})

	t.Run("exact_remaining_line_count_vs_byte_cap_and_upstream_truncation", func(t *testing.T) {
		setUnicodeDisplayEnv(t)

		// 30 short lines (< 4000 bytes, not upstream-truncated): exact +6 more lines.
		var lines30 []string
		for i := 1; i <= 30; i++ {
			lines30 = append(lines30, fmt.Sprintf("row %02d", i))
		}
		exactTree := stripTestANSI(renderExpandedToolTree(strings.Join(lines30, "\n"), "read_file(rows.txt) -> 209 bytes", "│   ", 80))
		wantExact := fmt.Sprintf("└─ +%d more lines", 30-maxExpandedToolLines)
		if !strings.Contains(exactTree, wantExact) {
			t.Fatalf("expected exact %q in:\n%s", wantExact, exactTree)
		}

		// > 4000 bytes: must NOT guess +N lines.
		longContent := strings.Repeat("alpha_beta_gamma_delta_0123456789\n", 140) // ~4760 bytes
		cappedTree := stripTestANSI(renderExpandedToolTree(longContent, "read_file(big.txt) -> 4760 bytes", "│   ", 80))
		if strings.Contains(cappedTree, "more lines") {
			t.Fatalf("4000-char capped preview must not fabricate +N more lines:\n%s", cappedTree)
		}
		if !strings.Contains(cappedTree, "output preview capped at 4000 chars") {
			t.Fatalf("4000-char capped preview missing honest cap notice:\n%s", cappedTree)
		}

		// Upstream truncated output (e.g. read_file JSON page with truncated=true): must NOT fabricate +N.
		upstreamTree := stripTestANSI(renderExpandedToolTree(strings.Join(lines30, "\n"), "read_file(part.go) -> 209 bytes (truncated, next offset: 3584)", "│   ", 80))
		if strings.Contains(upstreamTree, "+6 more lines") {
			t.Fatalf("upstream-truncated output must not claim exact +6 more lines:\n%s", upstreamTree)
		}
		if !strings.Contains(upstreamTree, "output truncated") {
			t.Fatalf("upstream-truncated output missing honest truncation indicator:\n%s", upstreamTree)
		}
	})

	t.Run("wrapped_lines_utf8_ansi_and_control_sanitization", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		// Construct content exceeding 4000 bytes across multibyte Arabic/CJK/emoji boundary + control chars.
		unit := "مرحبا_中文_🔍_\x1b[31mRED\x1b[0m\x1b]0;title\x07\u202E"
		raw := strings.Repeat(unit, 160) + "\nlast_line_" + strings.Repeat("漢字", 20)
		msg := Message{
			Role:       "tool",
			ToolCallID: "t_wrap",
			Content:    raw,
			Display:    "read_file(unicode.txt) -> 8000 bytes",
		}
		for _, w := range []int{36, 40, 45, 80} {
			rendered := renderToolMessageWithName(msg, "read_file", w, true, true)
			if !utf8.ValidString(rendered) {
				t.Fatalf("invalid UTF-8 at width %d", w)
			}
			plain := stripTestANSI(rendered)
			if strings.Contains(plain, "\x1b[") || strings.Contains(plain, "\x1b]") || strings.Contains(plain, "\u202E") {
				t.Fatalf("control or bidi sequence leaked at width %d: %q", w, plain)
			}
			for i, line := range strings.Split(rendered, "\n") {
				if lw := lipgloss.Width(line); lw > w {
					t.Fatalf("expanded tree width %d line %d overflowed (%d): %q", w, i, lw, line)
				}
			}
		}

		// Verify wrapped continuation keeps tree connectors cleanly.
		wrappedTree := stripTestANSI(renderExpandedToolTree("first line wrapping across narrow columns cleanly\nfinal line wrapping across narrow columns cleanly", "read_file(w.txt) -> 90 bytes", "│   ", 36))
		treeLines := strings.Split(wrappedTree, "\n")
		if len(treeLines) < 4 {
			t.Fatalf("expected wrapped tree lines at width 36, got:\n%s", wrappedTree)
		}
		if !strings.HasPrefix(treeLines[0], "│   │ ") || !strings.HasPrefix(treeLines[1], "│   │ ") {
			t.Fatalf("expected middle line continuation to keep '│   │ ', got:\n%s", wrappedTree)
		}
		if !strings.Contains(wrappedTree, "│   └─ ") {
			t.Fatalf("expected final branch '│   └─ ', got:\n%s", wrappedTree)
		}
	})
}

func TestTurnTimerLifecycleWithFakeClock(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	newClockModel := func(start time.Time) (uiModel, *time.Time) {
		now := start
		m := initialModel(reg, &mockClient{})
		m.nowFn = func() time.Time { return now }
		return m, &now
	}

	t.Run("subsecond_hidden_and_4s_and_1m12s_unicode_and_ascii", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		base := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

		// < 1s hidden
		m, now := newClockModel(base)
		upd, _ := m.executeLocalCommand("quick question")
		m = upd.(uiModel)
		*now = base.Add(900 * time.Millisecond)
		upd, _ = m.Update(llmResponseMsg{gen: m.taskGen, assistantMsg: Message{Role: "assistant", Content: "ok"}})
		m = upd.(uiModel)
		if strings.Contains(stripTestANSI(renderFooter(&m, 80)), "Worked for") {
			t.Fatalf("sub-second turn should hide Worked for:\n%s", renderFooter(&m, 80))
		}

		// 4s turn
		m, now = newClockModel(base)
		upd, _ = m.executeLocalCommand("explain main.go")
		m = upd.(uiModel)
		*now = base.Add(4 * time.Second)
		upd, _ = m.Update(llmResponseMsg{gen: m.taskGen, assistantMsg: Message{Role: "assistant", Content: "done"}})
		m = upd.(uiModel)
		footer4s := stripTestANSI(renderFooter(&m, 80))
		if !strings.Contains(footer4s, "· Worked for 4s") {
			t.Fatalf("expected '· Worked for 4s' in footer, got:\n%s", footer4s)
		}

		// View() must not recompute elapsed time after completion.
		*now = base.Add(99 * time.Second)
		viewLater := stripTestANSI(m.View())
		if !strings.Contains(viewLater, "· Worked for 4s") || strings.Contains(viewLater, "99s") {
			t.Fatalf("View() recomputed completed turn timer:\n%s", viewLater)
		}

		// 1m 12s turn + ASCII separator
		m, now = newClockModel(base)
		upd, _ = m.executeLocalCommand("long task")
		m = upd.(uiModel)
		*now = base.Add(72 * time.Second)
		upd, _ = m.Update(llmResponseMsg{gen: m.taskGen, assistantMsg: Message{Role: "assistant", Content: "done"}})
		m = upd.(uiModel)
		if f := stripTestANSI(renderFooter(&m, 80)); !strings.Contains(f, "· Worked for 1m 12s") {
			t.Fatalf("expected '· Worked for 1m 12s', got:\n%s", f)
		}
		t.Setenv("NABD_ASCII", "1")
		if fASCII := stripTestANSI(renderFooter(&m, 80)); !strings.Contains(fASCII, "| Worked for 1m 12s") {
			t.Fatalf("expected ASCII '| Worked for 1m 12s', got:\n%s", fASCII)
		}
	})

	t.Run("multi_tool_and_approval_wait_included_in_elapsed_turn_time", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		if err := os.WriteFile(filepath.Join(dir, "target.txt"), []byte("before\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
		m := initialModel(reg, &mockClient{})
		m.nowFn = func() time.Time { return now }

		// t=0s: user sends prompt
		upd, _ := m.executeLocalCommand("edit target.txt")
		m = upd.(uiModel)

		// t=1s: intermediate LLM response requesting edit_file (NOT final!)
		now = now.Add(1 * time.Second)
		editCall := makeTestToolCall("c_edit", "edit_file", `{"path":"target.txt","old_text":"before","new_text":"after"}`)
		upd, _ = m.Update(llmResponseMsg{
			gen:          m.taskGen,
			assistantMsg: Message{Role: "assistant", ToolCalls: []ToolCall{editCall}},
		})
		m = upd.(uiModel)
		if !m.awaitingApproval || !m.turnActive || m.hasLastTurnDuration {
			t.Fatalf("intermediate tool call must keep turnActive=true awaiting approval; got awaiting=%v active=%v hasLast=%v",
				m.awaitingApproval, m.turnActive, m.hasLastTurnDuration)
		}
		if strings.Contains(stripTestANSI(renderFooter(&m, 80)), "Worked for") {
			t.Fatal("Worked for must not appear before final response")
		}

		// t=6s: user approves execution (5s waiting for human approval)
		now = now.Add(5 * time.Second)
		upd, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		m = upd.(uiModel)
		if cmd == nil {
			t.Fatal("expected tool launch cmd after approval")
		}
		toolMsg := cmd()

		// t=7s: tool finishes, disclosure prompt appears
		now = now.Add(1 * time.Second)
		upd, _ = m.Update(toolMsg)
		m = upd.(uiModel)
		if m.pendingDisclosure == nil || !m.turnActive {
			t.Fatal("expected pending disclosure with turnActive=true")
		}

		// t=10s: user scrolls to bottom and approves disclosure (3s waiting for disclosure)
		now = now.Add(3 * time.Second)
		m.viewport.GotoBottom()
		upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
		m = upd.(uiModel)

		// t=12s: final assistant response arrives (no tool calls)
		now = now.Add(2 * time.Second)
		upd, _ = m.Update(llmResponseMsg{
			gen:          m.taskGen,
			assistantMsg: Message{Role: "assistant", Content: "Updated target.txt."},
		})
		m = upd.(uiModel)
		if m.turnActive || !m.hasLastTurnDuration || m.lastTurnDuration != 12*time.Second {
			t.Fatalf("expected 12s elapsed turn duration, got active=%v has=%v dur=%v", m.turnActive, m.hasLastTurnDuration, m.lastTurnDuration)
		}
		if f := stripTestANSI(renderFooter(&m, 80)); !strings.Contains(f, "· Worked for 12s") {
			t.Fatalf("expected '· Worked for 12s' in footer, got:\n%s", f)
		}
	})

	t.Run("error_esc_clear_and_late_completion_isolation", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)

		// 1. Esc cancel clears timer and blocks late completion from old generation.
		m, clk := newClockModel(now)
		upd, _ := m.executeLocalCommand("turn 1")
		m = upd.(uiModel)
		gen1 := m.taskGen
		*clk = now.Add(5 * time.Second)
		upd, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		m = upd.(uiModel)
		if m.turnActive || m.hasLastTurnDuration {
			t.Fatalf("Esc cancel left active/completed timer: active=%v has=%v", m.turnActive, m.hasLastTurnDuration)
		}
		// Late response from cancelled turn 1 must be dropped.
		upd, _ = m.Update(llmResponseMsg{gen: gen1, assistantMsg: Message{Role: "assistant", Content: "late"}})
		m = upd.(uiModel)
		if m.hasLastTurnDuration || strings.Contains(stripTestANSI(renderFooter(&m, 80)), "Worked for") {
			t.Fatalf("late response revived timer after Esc:\n%s", renderFooter(&m, 80))
		}

		// 2. Error response clears timer.
		upd, _ = m.executeLocalCommand("turn error")
		m = upd.(uiModel)
		*clk = clk.Add(3 * time.Second)
		upd, _ = m.Update(errorMsg{gen: m.taskGen, err: fmt.Errorf("upstream failure")})
		m = upd.(uiModel)
		if m.turnActive || m.hasLastTurnDuration || strings.Contains(stripTestANSI(renderFooter(&m, 80)), "Worked for") {
			t.Fatalf("error turn displayed Worked for:\n%s", renderFooter(&m, 80))
		}

		// 3. Completed turn cleared by /clear; late msg from old turn cannot restore it to new session.
		upd, _ = m.executeLocalCommand("turn ok")
		m = upd.(uiModel)
		genOK := m.taskGen
		*clk = clk.Add(4 * time.Second)
		upd, _ = m.Update(llmResponseMsg{gen: genOK, assistantMsg: Message{Role: "assistant", Content: "ok"}})
		m = upd.(uiModel)
		if !m.hasLastTurnDuration {
			t.Fatal("expected completed duration before /clear")
		}
		upd, _ = m.executeLocalCommand("/clear")
		m = upd.(uiModel)
		if m.hasLastTurnDuration || strings.Contains(stripTestANSI(renderFooter(&m, 80)), "Worked for") {
			t.Fatalf("/clear did not reset turn timer:\n%s", renderFooter(&m, 80))
		}
		// New turn starts; stale message from genOK must not attribute old time to new turn.
		upd, _ = m.executeLocalCommand("turn 2")
		m = upd.(uiModel)
		*clk = clk.Add(20 * time.Second)
		upd, _ = m.Update(llmResponseMsg{gen: genOK, assistantMsg: Message{Role: "assistant", Content: "stale"}})
		m = upd.(uiModel)
		if m.hasLastTurnDuration || !m.turnActive {
			t.Fatalf("stale generation message leaked into new turn: active=%v has=%v", m.turnActive, m.hasLastTurnDuration)
		}
	})
}

func TestLLMRequestUnchangedByTimerAndBadges(t *testing.T) {
	runScriptedSession := func(t *testing.T, clockStep time.Duration) llm.Request {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "sample.go"), []byte("package sample\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		reg, err := tools.NewRegistry(dir)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = reg.Close() })

		now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
		client := &mockClient{
			responses: []llm.Response{
				{
					Message: llm.Message{
						Role:      "assistant",
						ToolCalls: []ToolCall{makeTestToolCall("c1", "read_file", `{"path":"sample.go","format":"json"}`)},
					},
				},
				{
					Message: llm.Message{Role: "assistant", Content: "All checked."},
				},
			},
		}

		m := initialModel(reg, client, AutoApproveReadOnly)
		m.nowFn = func() time.Time { return now }

		upd, cmd := m.executeLocalCommand("Inspect sample.go")
		m = upd.(uiModel)
		now = now.Add(clockStep)
		upd, cmd = m.Update(cmd())
		m = upd.(uiModel)
		now = now.Add(clockStep)
		upd, cmd = m.Update(cmd())
		m = upd.(uiModel)
		now = now.Add(clockStep)
		upd, _ = m.Update(cmd())
		m = upd.(uiModel)

		if client.callCount() != 2 {
			t.Fatalf("expected 2 LLM calls, got %d", client.callCount())
		}
		return client.requestAt(1)
	}

	reqShort := runScriptedSession(t, 200*time.Millisecond)
	reqLong := runScriptedSession(t, 7*time.Second)

	if !reflect.DeepEqual(reqShort, reqLong) {
		t.Fatalf("LLM request differed across different UI timer durations:\nshort=%+v\nlong=%+v", reqShort, reqLong)
	}

	wireShort, err := json.Marshal(reqShort)
	if err != nil {
		t.Fatal(err)
	}
	wireLong, err := json.Marshal(reqLong)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(wireShort, wireLong) {
		t.Fatalf("wire JSON payload changed across timer durations:\nshort=%s\nlong=%s", wireShort, wireLong)
	}

	for i, msg := range reqLong.Messages {
		for _, forbidden := range []string{"Worked for", "[READ]", "[EDIT]", "[TOOL]", "[VERIFY]", "[MCP]"} {
			if strings.Contains(msg.Content, forbidden) {
				t.Fatalf("LLM message[%d] leaked UI display string %q: %q", i, forbidden, msg.Content)
			}
		}
	}
	for _, forbidden := range []string{"Worked for", "[READ]", "[EDIT]", "[TOOL]", "[VERIFY]", "[MCP]"} {
		if bytes.Contains(wireLong, []byte(forbidden)) {
			t.Fatalf("serialized LLM wire payload leaked UI string %q: %s", forbidden, wireLong)
		}
	}
	for _, tool := range reqLong.Tools {
		serialized := fmt.Sprint(tool)
		if strings.Contains(serialized, "Worked for") || strings.Contains(serialized, "[READ]") {
			t.Fatalf("tool schema polluted with UI strings: %s", serialized)
		}
	}
}

func TestNarrowWidthsResizeAndSecurityPriority(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	widths := []int{36, 40, 45, 60, 80}
	heights := []int{12, 16, 24}

	t.Run("responsive_widths_heights_and_single_line_footer", func(t *testing.T) {
		setUnicodeDisplayEnv(t)
		for _, w := range widths {
			for _, h := range heights {
				m := initialModel(reg, &mockClient{})
				upd, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
				m = upd.(uiModel)
				m.lastTurnDuration = 4 * time.Second
				m.hasLastTurnDuration = true
				m.messages = append(m.messages,
					Message{Role: "user", Content: "check files"},
					Message{Role: "assistant", Content: "Reading files", ToolCalls: []ToolCall{makeTestToolCall("t1", "read_file", `{"path":"internal/tools/registry.go"}`)}},
					Message{Role: "tool", ToolCallID: "t1", Content: "package tools\nline 2\nline 3", Display: "read_file(internal/tools/registry.go) -> 1200 bytes"},
				)
				m.expandLastTool = false
				m.refresh()
				if w <= 40 {
					plainCompact := stripTestANSI(m.viewport.View())
					if !strings.Contains(plainCompact, "…") {
						t.Fatalf("expected narrow compacted summary at width %d to indicate truncation:\n%s", w, plainCompact)
					}
				}

				m.expandLastTool = true
				m.refresh()

				footer := renderFooter(&m, w)
				if fLines := strings.Split(footer, "\n"); len(fLines) != 1 {
					t.Fatalf("expected 1-line footer at width %d with 4s timer, got %d lines:\n%s", w, len(fLines), footer)
				}
				if !strings.Contains(stripTestANSI(footer), "Worked for 4s") {
					t.Fatalf("footer at width %d missing Worked for 4s:\n%s", w, footer)
				}

				view := m.View()
				for i, line := range strings.Split(view, "\n") {
					if lw := lipgloss.Width(line); lw > w {
						t.Fatalf("View() line %d at %dx%d exceeds width (got %d): %q", i, w, h, lw, line)
					}
				}
				if h >= 16 {
					if gotH := len(strings.Split(view, "\n")); gotH > h {
						t.Fatalf("View() height at %dx%d exceeded terminal height (got %d)", w, h, gotH)
					}
				}
			}
		}

		// Resize from 80 -> 36 preserves width bounds and updates expanded tree cleanly.
		m := initialModel(reg, &mockClient{})
		m.messages = append(m.messages, Message{
			Role:       "tool",
			ToolCallID: "r1",
			Content:    "long output line with enough words to force wrapping across narrow Termux screens",
			Display:    "read_file(cmd/agent/ui_render.go) -> 90 bytes",
		})
		m.expandLastTool = true
		upd, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
		m = upd.(uiModel)
		upd, _ = m.Update(tea.WindowSizeMsg{Width: 36, Height: 16})
		m = upd.(uiModel)
		for i, line := range strings.Split(m.View(), "\n") {
			if lw := lipgloss.Width(line); lw > 36 {
				t.Fatalf("resized View() line %d exceeded 36 (got %d): %q", i, lw, line)
			}
		}
	})

	t.Run("security_warnings_approval_and_disclosure_never_truncated", func(t *testing.T) {
		setUnicodeDisplayEnv(t)

		// 1. Approval at width 36 keeps full approval prompt and Y/N choices even with timer present.
		mApp := initialModel(reg, &mockClient{})
		upd, _ := mApp.Update(tea.WindowSizeMsg{Width: 36, Height: 16})
		mApp = upd.(uiModel)
		mApp.lastTurnDuration = 4 * time.Second
		mApp.hasLastTurnDuration = true
		tc := makeTestToolCall("a1", "edit_file", `{"path":"secret.go","old_text":"a","new_text":"b"}`)
		mApp.pendingTools = []ToolCall{tc}
		mApp.awaitingApproval = true
		mApp.approval = approvalPreview{Summary: "path: secret.go\n-a\n+b"}
		mApp.status = "Approval required [y/n]"
		mApp.refresh()

		appView := stripTestANSI(mApp.View())
		for _, mustHave := range []string{"Approve edit_file? [Y/N]", "Y approve", "N reject", "path: secret.go"} {
			if !strings.Contains(appView, mustHave) {
				t.Fatalf("approval view at width 36 lost %q:\n%s", mustHave, appView)
			}
		}

		// 2. Disclosure + advisory secret warning at width 36 keeps full warning & scroll/confirm prompt.
		mDisc := initialModel(reg, &mockClient{})
		upd, _ = mDisc.Update(tea.WindowSizeMsg{Width: 36, Height: 14})
		mDisc = upd.(uiModel)
		mDisc.pendingTools = []ToolCall{makeTestToolCall("d1", "read_file", `{"path":"env.txt"}`)}
		mDisc.pendingDisclosure = &Message{
			Role:       "tool",
			ToolCallID: "d1",
			Content:    "API_KEY=sk-1234567890abcdef1234567890abcdef",
		}
		mDisc.refreshDisclosure()
		mDisc.viewport.GotoBottom()
		discView := stripTestANSI(mDisc.View())
		flatDisc := strings.ReplaceAll(discView, "\n", " ")
		for _, mustHave := range []string{"ADVISORY WARNING", "Send the displayed tool output to the model? [Y/N]", "At Bottom ✓ (Y send / N reject)"} {
			if !strings.Contains(flatDisc, mustHave) {
				t.Fatalf("disclosure view at width 36 lost %q:\n%s", mustHave, discView)
			}
		}

		// 3. Tool secret text must never leak into timer, badges, or footer status.
		mLeak := initialModel(reg, &mockClient{})
		mLeak.lastTurnDuration = 5 * time.Second
		mLeak.hasLastTurnDuration = true
		mLeak.messages = append(mLeak.messages, Message{
			Role:       "tool",
			ToolCallID: "l1",
			Content:    "TOKEN=sk-super-secret-value-9876543210",
			Display:    "read_file(config.env) -> 40 bytes",
		})
		footer := stripTestANSI(renderFooter(&mLeak, 80))
		if strings.Contains(footer, "sk-super-secret") {
			t.Fatalf("tool secret leaked into footer: %q", footer)
		}
	})
}

func TestTUIPolishTextPreviews(t *testing.T) {
	dir := t.TempDir()
	reg, err := tools.NewRegistry(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reg.Close() })

	sampleMsgs := []Message{
		{Role: "user", Content: "Inspect repo and run tests"},
		{
			Role:    "assistant",
			Content: "Checking key files and running verification.",
			ToolCalls: []ToolCall{
				makeTestToolCall("p1", "read_file", `{"path":"internal/tools/registry.go"}`),
				makeTestToolCall("p2", "edit_file", `{"path":"cmd/agent/ui_render.go"}`),
				makeTestToolCall("p3", "go_verify", `{"pkg":"./cmd/agent"}`),
			},
		},
		{Role: "tool", ToolCallID: "p1", Content: "package tools\n", Display: "read_file(internal/tools/registry.go) -> 1820 bytes"},
		{Role: "tool", ToolCallID: "p2", Content: "updated ui_render.go", Display: "edit_file(cmd/agent/ui_render.go) -> Successfully updated"},
		{Role: "tool", ToolCallID: "p3", Content: "PASS\nok  github.com/amiraq1/nabd/cmd/agent 1.2s\nok  github.com/amiraq1/nabd/internal/llm 0.5s", Display: "go_verify(./cmd/agent) -> PASS"},
	}

	previewFooter := func(w int, ascii bool) string {
		m := initialModel(reg, &mockClient{})
		m.lastTurnDuration = 4 * time.Second
		m.hasLastTurnDuration = true
		if ascii {
			t.Setenv("NABD_ASCII", "1")
		} else {
			setUnicodeDisplayEnv(t)
		}
		return stripTestANSI(renderFooter(&m, w))
	}

	for _, w := range []int{40, 80} {
		setUnicodeDisplayEnv(t)
		t.Logf("=== COMPACT %d COLS (Unicode) ===\n%s\n%s", w, stripTestANSI(renderMessages(sampleMsgs, w, false)), previewFooter(w, false))
		t.Logf("=== EXPANDED %d COLS (Unicode) ===\n%s\n%s", w, stripTestANSI(renderMessages(sampleMsgs, w, true)), previewFooter(w, false))
		t.Setenv("NABD_ASCII", "1")
		t.Logf("=== COMPACT %d COLS (ASCII) ===\n%s\n%s", w, stripTestANSI(renderMessages(sampleMsgs, w, false)), previewFooter(w, true))
		t.Logf("=== EXPANDED %d COLS (ASCII) ===\n%s\n%s", w, stripTestANSI(renderMessages(sampleMsgs, w, true)), previewFooter(w, true))
	}
}
