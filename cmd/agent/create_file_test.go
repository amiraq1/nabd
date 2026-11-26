package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func createToolCall(id, path, content string) ToolCall {
	raw, _ := json.Marshal(map[string]any{"path": path, "content": content})
	tc := ToolCall{ID: id, Type: "function"}
	tc.Function.Name = "create_file"
	tc.Function.Arguments = string(raw)
	return tc
}

func TestTUICreateFilesInEmptyWorkspace(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		id      string
		path    string
		content string
	}{
		{"c1", "go.mod", "module example.com/demo\n\ngo 1.26\n"},
		{"c2", "main.go", "package main\n\nfunc main() {}\n"},
		{"c3", "pkg/sub/a.go", "package sub\n\nfunc Answer() int { return 42 }\n"},
	}

	for _, tcCase := range cases {
		tc := createToolCall(tcCase.id, tcCase.path, tcCase.content)
		m := f18Submit(t, f18Model(t, dir), tc)
		if !m.awaitingApproval {
			t.Fatalf("%s: expected approval prompt", tcCase.path)
		}
		view := m.View()
		if !strings.Contains(view, "Approve create_file? [Y/N]") {
			t.Fatalf("%s: approval view missing create_file prompt:\n%s", tcCase.path, view)
		}
		if !strings.Contains(view, "content sha256:") || !strings.Contains(view, tcCase.path) {
			t.Fatalf("%s: approval view missing header:\n%s", tcCase.path, view)
		}
		m = f18Approve(t, m)
		reply := f18Reply(m, tcCase.id)
		if !strings.HasPrefix(reply, "Successfully created") {
			t.Fatalf("%s: unexpected reply %q", tcCase.path, reply)
		}
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(tcCase.path)))
		if err != nil || string(got) != tcCase.content {
			t.Fatalf("%s: file content mismatch: %q %v", tcCase.path, got, err)
		}
	}
}

func TestTUICreateFileEmptyContentAndPreviewLimits(t *testing.T) {
	dir := t.TempDir()
	m := f18Model(t, dir)

	// Empty content explicitly provided is allowed and surfaced clearly.
	tcEmpty := createToolCall("c_empty", ".gitkeep", "")
	m = f18Submit(t, m, tcEmpty)
	if !m.awaitingApproval {
		t.Fatal("empty content create_file should prompt for approval")
	}
	if !strings.Contains(m.View(), "empty file, 0 bytes") {
		t.Fatalf("empty file preview missing explicit notice:\n%s", m.View())
	}
	m = f18Approve(t, m)
	if reply := f18Reply(m, "c_empty"); !strings.HasPrefix(reply, "Successfully created") {
		t.Fatalf("empty file create failed: %q", reply)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".gitkeep"))
	if err != nil || len(data) != 0 {
		t.Fatalf("expected 0-byte .gitkeep, got %q %v", data, err)
	}

	// Reject oversized payload cleanly before prompting user.
	mBig := f18Submit(t, f18Model(t, dir), createToolCall("c_big", "big.txt", strings.Repeat("x", maxApprovalArgs+100)))
	if mBig.awaitingApproval {
		t.Fatal("oversized create_file must not prompt for approval")
	}
	if reply := f18Reply(mBig, "c_big"); !strings.Contains(reply, "approval limit") || !strings.Contains(reply, "edit_file") {
		t.Fatalf("oversized create_file reply = %q", reply)
	}

	// Reject hidden/control/bidi characters before prompting user.
	for _, bad := range []struct{ name, content string }{
		{"ansi", "ok\x1b[31mspoof"},
		{"bidi", "ok\u202espoof"},
		{"zwsp", "ok\u200bspoof"},
		{"bare_cr", "ok\rspoof"},
	} {
		t.Run(bad.name, func(t *testing.T) {
			mBad := f18Submit(t, f18Model(t, dir), createToolCall("c_"+bad.name, bad.name+".txt", bad.content))
			if mBad.awaitingApproval {
				t.Fatalf("%s: hidden control char reached approval prompt", bad.name)
			}
			if reply := f18Reply(mBad, "c_"+bad.name); !strings.Contains(reply, "undisplayable control characters") {
				t.Fatalf("%s: reply = %q", bad.name, reply)
			}
		})
	}

	// Valid CRLF newlines are explicitly displayed as [EOL=CRLF] and preserved verbatim.
	mCRLF := f18Submit(t, f18Model(t, dir), createToolCall("c_crlf", "crlf.txt", "a\r\nb\r\n"))
	if !mCRLF.awaitingApproval {
		t.Fatal("valid CRLF create_file should prompt for approval")
	}
	if !strings.Contains(mCRLF.View(), "[EOL=CRLF]") {
		t.Fatalf("CRLF preview missing [EOL=CRLF] indicator:\n%s", mCRLF.View())
	}
	mCRLF = f18Approve(t, mCRLF)
	if crlfData, err := os.ReadFile(filepath.Join(dir, "crlf.txt")); err != nil || string(crlfData) != "a\r\nb\r\n" {
		t.Fatalf("CRLF content mismatch: %q %v", crlfData, err)
	}
}

func TestTUICreateFileRejectsTargetAppearingBeforeYes(t *testing.T) {
	dir := t.TempDir()
	m := f18Submit(t, f18Model(t, dir), createToolCall("c_race", "race.txt", "new\n"))
	if !m.awaitingApproval {
		t.Fatal("expected approval prompt")
	}
	if err := os.WriteFile(filepath.Join(dir, "race.txt"), []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	um := updated.(uiModel)
	reply := f18Reply(um, "c_race")
	if !strings.Contains(reply, "already exists") {
		t.Fatalf("expected target appeared error, got %q", reply)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "race.txt"))
	if string(got) != "existing\n" {
		t.Fatalf("existing file overwritten: %q", got)
	}
}

func TestTUICreateFileAutoApproveAllFollowsEditPolicy(t *testing.T) {
	dir := t.TempDir()
	m := newTestAgentModel(t, dir, AutoApproveAll)
	m.toolQueueMode = AutoApproveAll
	tc := createToolCall("c_auto", "auto.go", "package main\n\nfunc main() {}\n")
	m.pendingTools = []ToolCall{tc}

	next, cmd := m.advance()
	um := next.(uiModel)
	if um.awaitingApproval || cmd == nil {
		t.Fatalf("create_file should follow edit_file under /auto all (awaiting=%v cmd=%v)", um.awaitingApproval, cmd)
	}
	updated, _ := um.Update(cmd())
	um = updated.(uiModel)
	if reply := f18Reply(um, "c_auto"); !strings.HasPrefix(reply, "Successfully created") {
		t.Fatalf("auto-approved create_file failed: %q", reply)
	}
}

func TestSystemPromptDocumentsCreateAndEditTools(t *testing.T) {
	_, m := testModel(t)
	prompt := m.messages[0].Content
	for _, want := range []string{"create_file", "edit_file"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("system prompt missing %q:\n%s", want, prompt)
		}
	}
	sum := formatToolSummary("create_file", `{"path":"go.mod"}`, "Successfully created go.mod")
	if sum != "create_file(go.mod) -> Successfully created go.mod" {
		t.Fatalf("formatToolSummary(create_file) = %q", sum)
	}
}
