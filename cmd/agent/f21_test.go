package main

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestF21SmallWindowDoesNotPanic(t *testing.T) {
	m := testModel(t)
	widths := []int{0, 10}
	heights := []int{0, 3, 5}
	for _, w := range widths {
		for _, h := range heights {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("WindowSizeMsg width=%d height=%d panicked: %v", w, h, r)
					}
				}()
				updated, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
				um, ok := updated.(uiModel)
				if !ok {
					t.Fatalf("unexpected model type %T", updated)
				}
				if um.viewport.Height < 1 {
					t.Fatalf("viewport height = %d for terminal height %d", um.viewport.Height, h)
				}
				if um.viewport.Width < 1 {
					t.Fatalf("viewport width = %d for terminal width %d", um.viewport.Width, w)
				}
				um.refresh()
			}()
		}
	}
}
