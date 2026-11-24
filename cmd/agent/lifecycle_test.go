package main

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestReliabilityShutdownRejectsLateCommands(t *testing.T) {
	l := newLifecycle()
	called := false
	cmd := l.wrap(func() tea.Msg { called = true; return nil })
	l.stop()
	_ = cmd()
	if called {
		t.Fatal("command started after shutdown")
	}
	if !l.wait(time.Second) {
		t.Fatal("shutdown did not finish")
	}
}
func TestReliabilityShutdownWaitIsBounded(t *testing.T) {
	l := newLifecycle()
	started := make(chan struct{})
	release := make(chan struct{})
	cmd := l.wrap(func() tea.Msg { close(started); <-release; return nil })
	done := make(chan struct{})
	go func() { _ = cmd(); close(done) }()
	<-started
	l.stop()
	if l.wait(20 * time.Millisecond) {
		t.Fatal("shutdown reported completion while command was active")
	}
	close(release)
	<-done
	if !l.wait(time.Second) {
		t.Fatal("shutdown did not observe completion")
	}
}
