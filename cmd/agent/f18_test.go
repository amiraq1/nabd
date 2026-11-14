package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
)

func f18Model(t *testing.T, dir string) uiModel {
	t.Helper()
	reg, err := tools.NewRegistry(dir, nil)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	return initialModel(reg)
}

func f18Call(args string) ToolCall {
	tc := ToolCall{ID: "call_f18", Type: "function"}
	tc.Function.Name = "edit_file"
	tc.Function.Arguments = args
	return tc
}

func f18Submit(t *testing.T, m uiModel, tc ToolCall) uiModel {
	t.Helper()
	m.busy = true
	updated, _ := m.Update(llmResponseMsg{assistantMsg: Message{Role: "assistant", ToolCalls: []ToolCall{tc}}})
	um, ok := updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	return um
}

func f18Approve(t *testing.T, m uiModel) uiModel {
	t.Helper()
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	um, ok := updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	if cmd == nil {
		t.Fatal("approval returned no execution command")
	}
	updated, _ = um.Update(cmd())
	um, ok = updated.(uiModel)
	if !ok {
		t.Fatalf("unexpected model type %T", updated)
	}
	return um
}

func f18Reply(m uiModel, id string) string {
	for _, msg := range m.messages {
		if msg.Role == "tool" && msg.ToolCallID == id {
			return msg.Content
		}
	}
	return ""
}

func TestF18ApprovalShowsArabicDiff(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "ar.txt")
	if err := os.WriteFile(target, []byte("مرحبا بالعالم\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := f18Submit(t, f18Model(t, dir), f18Call(`{"path":"ar.txt","old_text":"مرحبا بالعالم","new_text":"مرحبا يا عالم"}`))
	if !m.awaitingApproval {
		t.Fatal("no approval prompt")
	}
	view := m.View()
	if strings.Contains(view, `"old_text"`) {
		t.Fatalf("approval still shows raw JSON: %q", view)
	}
	if !strings.Contains(view, "-مرحبا بالعالم") {
		t.Fatalf("approval diff lacks the removed Arabic line: %q", view)
	}
	if !strings.Contains(view, "+مرحبا يا عالم") {
		t.Fatalf("approval diff lacks the added Arabic line: %q", view)
	}
	if !utf8.ValidString(view) {
		t.Fatal("approval view is not valid UTF-8")
	}
}

func TestF18ChangedFileRejected(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := f18Submit(t, f18Model(t, dir), f18Call(`{"path":"a.txt","old_text":"old","new_text":"new"}`))
	if !m.awaitingApproval {
		t.Fatal("no approval prompt")
	}
	if err := os.WriteFile(target, []byte("changed old\n"), 0600); err != nil {
		t.Fatal(err)
	}

	m = f18Approve(t, m)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "changed old\n" {
		t.Fatalf("file was overwritten after a post-approval change: %q", got)
	}
	if reply := f18Reply(m, "call_f18"); !strings.Contains(reply, "file changed since approval") {
		t.Fatalf("missing hash mismatch error, got %q", reply)
	}
}

func TestF18UnchangedFileSucceeds(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(target, []byte("old\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := f18Submit(t, f18Model(t, dir), f18Call(`{"path":"a.txt","old_text":"old","new_text":"new"}`))
	if !m.awaitingApproval {
		t.Fatal("no approval prompt")
	}
	view := m.View()
	if !strings.Contains(view, "-old") || !strings.Contains(view, "+new") {
		t.Fatalf("approval view lacks the diff: %q", view)
	}

	m = f18Approve(t, m)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new\n" {
		t.Fatalf("edit was not applied: %q", got)
	}
	if reply := f18Reply(m, "call_f18"); !strings.Contains(reply, "Successfully updated") {
		t.Fatalf("missing success reply, got %q", reply)
	}
}

func TestF18CRLFPreserved(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "x.go")
	if err := os.WriteFile(target, []byte("package x\r\n\r\nfunc A() {}\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	m := f18Submit(t, f18Model(t, dir), f18Call(`{"path":"x.go","old_text":"func A() {}","new_text":"func A() { _ = 1 }"}`))
	if !m.awaitingApproval {
		t.Fatal("no approval prompt")
	}
	view := m.View()
	if !strings.Contains(view, "-func A() {}") || !strings.Contains(view, "+func A() { _ = 1 }") {
		t.Fatalf("approval view lacks the diff: %q", view)
	}

	m = f18Approve(t, m)
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("\r\n")) {
		t.Fatalf("CRLF was lost: %q", got)
	}
	if bytes.Contains(bytes.ReplaceAll(got, []byte("\r\n"), nil), []byte("\n")) {
		t.Fatalf("mixed line endings after the edit: %q", got)
	}
}

func TestF18MissingFileExplicit(t *testing.T) {
	dir := t.TempDir()
	m := f18Submit(t, f18Model(t, dir), f18Call(`{"path":"missing.txt","old_text":"a","new_text":"b"}`))
	if !m.awaitingApproval {
		t.Fatal("missing file was not surfaced as an explicit approval state")
	}
	view := m.View()
	if !strings.Contains(view, "does not exist") {
		t.Fatalf("approval view lacks the missing-file notice: %q", view)
	}

	m = f18Approve(t, m)
	if _, err := os.Stat(filepath.Join(dir, "missing.txt")); err == nil {
		t.Fatal("edit_file created a missing file")
	}
	if reply := f18Reply(m, "call_f18"); !strings.Contains(reply, "Error") {
		t.Fatalf("expected an error reply for the missing file, got %q", reply)
	}
}
