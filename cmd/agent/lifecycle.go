package main

import (
	"context"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// lifecycle owns commands that may outlive the UI. Shutdown prohibits new
// commands, cancels active work, and waits before closing the registry.
type lifecycle struct {
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	closing bool
	wg      sync.WaitGroup
}

func newLifecycle() *lifecycle {
	ctx, cancel := context.WithCancel(context.Background())
	return &lifecycle{ctx: ctx, cancel: cancel}
}
func (l *lifecycle) wrap(cmd tea.Cmd) tea.Cmd {
	return func() tea.Msg {
		l.mu.Lock()
		if l.closing {
			l.mu.Unlock()
			return errorMsg{err: context.Canceled}
		}
		l.wg.Add(1)
		l.mu.Unlock()
		defer l.wg.Done()
		return cmd()
	}
}
func (l *lifecycle) stop() {
	l.mu.Lock()
	if !l.closing {
		l.closing = true
		l.cancel()
	}
	l.mu.Unlock()
}
func (l *lifecycle) wait(timeout time.Duration) bool {
	done := make(chan struct{})
	go func() { l.wg.Wait(); close(done) }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
