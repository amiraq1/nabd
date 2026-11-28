package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/amiraq1/nabd/internal/memory"
	"github.com/amiraq1/nabd/internal/tools"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type memoryDeletePending struct {
	ID    string
	Kind  string
	Title string
}

var errRememberUsage = errors.New("Usage: /remember [decision:|bugfix:|handoff:|note:] <text>")

func isRememberKindToken(s string) bool {
	if len(s) == 0 || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if !isLetter && !isDigit && c != '_' && c != '-' {
			return false
		}
	}
	return true
}

func parseRememberCommandArgs(rawArgs string) (memory.SaveInput, error) {
	trimmed := strings.TrimSpace(rawArgs)
	if trimmed == "" {
		return memory.SaveInput{}, errRememberUsage
	}

	kind := string(memory.KindNote)
	bodyText := trimmed

	if colon := strings.IndexByte(trimmed, ':'); colon > 0 {
		prefix := trimmed[:colon]
		rest := trimmed[colon+1:]
		// Only treat "<word>: <text>" (or trailing colon) as a kind prefix so
		// URLs ("https://...") and timestamps ("12:30 ...") remain normal notes.
		hasSep := rest == "" || unicode.IsSpace(rune(rest[0]))
		if hasSep && isRememberKindToken(prefix) {
			parsedKind, err := memory.ParseKind(prefix, false)
			if err != nil {
				return memory.SaveInput{}, err
			}
			kind = string(parsedKind)
			bodyText = strings.TrimSpace(rest)
			if bodyText == "" {
				return memory.SaveInput{}, errRememberUsage
			}
		}
	}

	normBody := strings.ReplaceAll(bodyText, "\r\n", "\n")
	title, err := memory.DeriveTitleFromText(normBody)
	if err != nil {
		return memory.SaveInput{}, err
	}
	return memory.SaveInput{
		Kind:  kind,
		Title: title,
		Body:  strings.TrimSpace(normBody),
	}, nil
}

func (m uiModel) commandContext() context.Context {
	if m.lifecycle != nil && m.lifecycle.ctx != nil {
		return m.lifecycle.ctx
	}
	return context.Background()
}

func (m uiModel) handleRememberCommand(rawArgs string) (tea.Model, tea.Cmd) {
	m.clearInput()
	if m.busy || m.awaitingApproval || m.pendingDisclosure != nil {
		m.messages = append(m.messages, Message{
			Role:    "local",
			Content: "Cannot save memory while an operation or approval is in progress.",
		})
		m.refresh()
		return m, nil
	}
	if m.registry == nil {
		m.messages = append(m.messages, Message{
			Role:    "local",
			Content: "Memory error: tool registry is unavailable.",
		})
		m.refresh()
		return m, nil
	}

	input, err := parseRememberCommandArgs(rawArgs)
	if err != nil {
		if errors.Is(err, errRememberUsage) {
			m.messages = append(m.messages, Message{Role: "local", Content: errRememberUsage.Error()})
		} else {
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory rejected: " + sanitizeSingleLineDisplay(err.Error())})
		}
		m.refresh()
		return m, nil
	}

	entry, err := m.registry.RememberMemory(m.commandContext(), input)
	if err != nil {
		if entry.ID != "" {
			// Primary write succeeded, but directory sync or post-write audit raised a warning.
			m.status = fmt.Sprintf("Saved memory %s (with warning)", entry.ID)
			m.messages = append(m.messages, Message{
				Role:    "local",
				Content: fmt.Sprintf("Saved memory %s [%s]; warning: %s", entry.ID, entry.Kind, sanitizeSingleLineDisplay(err.Error())),
			})
		} else {
			m.status = "Memory save rejected"
			m.messages = append(m.messages, Message{
				Role:    "local",
				Content: "Memory save failed: " + sanitizeSingleLineDisplay(err.Error()),
			})
		}
		m.refresh()
		return m, nil
	}

	m.status = fmt.Sprintf("Saved memory %s", entry.ID)
	msg := fmt.Sprintf("Saved memory %s [%s]", entry.ID, entry.Kind)
	if !m.registry.MemoryDirectorySyncSupported() {
		msg += " (note: directory fsync is unsupported on this platform; see docs/THREAT_MODEL.md)"
	}
	m.messages = append(m.messages, Message{
		Role:    "local",
		Content: msg,
	})
	m.refresh()
	return m, nil
}

func (m uiModel) handleMemoryCommand(rawArgs string) (tea.Model, tea.Cmd) {
	m.clearInput()
	if m.busy || m.awaitingApproval || m.pendingDisclosure != nil {
		m.messages = append(m.messages, Message{
			Role:    "local",
			Content: "Cannot access memory while an operation or approval is in progress.",
		})
		m.refresh()
		return m, nil
	}
	if m.registry == nil {
		m.messages = append(m.messages, Message{
			Role:    "local",
			Content: "Memory error: tool registry is unavailable.",
		})
		m.refresh()
		return m, nil
	}

	trimmed := strings.TrimSpace(rawArgs)
	ctx := m.commandContext()
	if trimmed == "" {
		entries, total, err := m.registry.ListMemories(ctx, 10)
		if err != nil {
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory error: " + sanitizeSingleLineDisplay(err.Error())})
		} else {
			m.messages = append(m.messages, Message{Role: "local", Content: formatMemoryListDisplay(entries, total, m.viewport.Width)})
		}
		m.refresh()
		return m, nil
	}

	sub := trimmed
	subArgs := ""
	if idx := strings.IndexAny(trimmed, " \t\r\n"); idx >= 0 {
		sub = trimmed[:idx]
		subArgs = strings.TrimSpace(trimmed[idx+1:])
	}

	switch strings.ToLower(sub) {
	case "search":
		if subArgs == "" {
			m.messages = append(m.messages, Message{Role: "local", Content: "Usage: /memory search <query>"})
			m.refresh()
			return m, nil
		}
		resp, err := m.registry.SearchMemories(ctx, subArgs, "", 5)
		if err != nil {
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory search failed: " + sanitizeSingleLineDisplay(err.Error())})
		} else {
			m.messages = append(m.messages, Message{Role: "local", Content: formatMemorySearchDisplay(resp, m.viewport.Width)})
		}
		m.refresh()
		return m, nil

	case "delete":
		if subArgs == "" || strings.ContainsAny(subArgs, " \t\r\n") {
			m.messages = append(m.messages, Message{Role: "local", Content: "Usage: /memory delete <id>"})
			m.refresh()
			return m, nil
		}
		if err := memory.ValidateID(subArgs); err != nil {
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory delete error: " + sanitizeSingleLineDisplay(err.Error())})
			m.refresh()
			return m, nil
		}
		entry, err := m.registry.GetMemory(ctx, subArgs)
		if err != nil {
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory delete error: " + sanitizeSingleLineDisplay(err.Error())})
			m.refresh()
			return m, nil
		}
		safeTitle := sanitizeSingleLineDisplay(entry.Title)
		m.memoryDeleteConfirm = &memoryDeletePending{
			ID:    entry.ID,
			Kind:  string(entry.Kind),
			Title: safeTitle,
		}
		m.status = "Confirm memory delete [y/N]"
		m.messages = append(m.messages, Message{
			Role:    "local",
			Content: fmt.Sprintf("Delete memory %s [%s]: %s?\nPress y to delete; Esc, Enter or any other key cancels.", entry.ID, entry.Kind, safeTitle),
		})
		m.refresh()
		return m, nil

	default:
		m.messages = append(m.messages, Message{
			Role:    "local",
			Content: "Usage: /memory | /memory search <query> | /memory delete <id>",
		})
		m.refresh()
		return m, nil
	}
}

func (m *uiModel) resolveMemoryDeleteConfirm(key string) {
	pending := m.memoryDeleteConfirm
	m.memoryDeleteConfirm = nil
	if pending == nil {
		return
	}
	if key == "y" || key == "yes" {
		if m.registry == nil {
			m.status = "Memory error"
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory delete failed: registry unavailable."})
			return
		}
		removed, err := m.registry.DeleteMemory(m.commandContext(), pending.ID)
		if err != nil {
			if removed.ID != "" {
				m.status = fmt.Sprintf("Deleted %s (with warning)", removed.ID)
				m.messages = append(m.messages, Message{
					Role:    "local",
					Content: fmt.Sprintf("Deleted memory %s [%s]; warning: %s", removed.ID, removed.Kind, sanitizeSingleLineDisplay(err.Error())),
				})
				return
			}
			m.status = "Memory delete failed"
			m.messages = append(m.messages, Message{Role: "local", Content: "Memory delete failed: " + sanitizeSingleLineDisplay(err.Error())})
			return
		}
		m.status = fmt.Sprintf("Deleted memory %s", removed.ID)
		msg := fmt.Sprintf("Deleted memory %s [%s]", removed.ID, removed.Kind)
		if !m.registry.MemoryDirectorySyncSupported() {
			msg += " (note: directory fsync is unsupported on this platform; see docs/THREAT_MODEL.md)"
		}
		m.messages = append(m.messages, Message{Role: "local", Content: msg})
		return
	}

	if m.registry != nil {
		_ = m.registry.RecordMemoryDeleteDenied(pending.ID)
	}
	m.status = "Memory delete cancelled"
	m.messages = append(m.messages, Message{
		Role:    "local",
		Content: fmt.Sprintf("Memory delete cancelled for %s.", pending.ID),
	})
}

func formatMemoryListDisplay(entries []memory.Entry, total int, width int) string {
	if len(entries) == 0 {
		return "No workspace memories saved yet. Use /remember [kind:] <text>."
	}
	avail := memoryLocalWidth(width)
	var b strings.Builder
	fmt.Fprintf(&b, "Workspace Memories (showing %d of %d):", len(entries), total)
	for _, e := range entries {
		title := sanitizeSingleLineDisplay(e.Title)
		date := sanitizeSingleLineDisplay(e.CreatedAt)
		if len(date) >= 10 {
			date = date[:10]
		}
		header := fmt.Sprintf("  %s [%s] %s", e.ID, e.Kind, date)
		oneLine := header + " - " + title
		if avail >= 52 && lipgloss.Width(oneLine) <= avail {
			b.WriteByte('\n')
			b.WriteString(oneLine)
		} else {
			b.WriteByte('\n')
			b.WriteString(truncateEndWithEllipsis(header, avail))
			b.WriteByte('\n')
			b.WriteString("    " + truncateEndWithEllipsis(title, max(12, avail-4)))
		}
		if len(e.Tags) > 0 {
			tagsLine := "    tags: " + sanitizeSingleLineDisplay(strings.Join(e.Tags, ", "))
			b.WriteByte('\n')
			b.WriteString(truncateEndWithEllipsis(tagsLine, avail))
		}
	}
	b.WriteString("\nCommands: /memory search <q> | /memory delete <id>")
	return b.String()
}

func formatMemorySearchDisplay(resp memory.SearchResponse, width int) string {
	if resp.Returned == 0 {
		return "No matching workspace memories found."
	}
	avail := memoryLocalWidth(width)
	var b strings.Builder
	if resp.Truncated {
		fmt.Fprintf(&b, "Memory Search (%d of %d matches, truncated):", resp.Returned, resp.TotalMatches)
	} else {
		fmt.Fprintf(&b, "Memory Search (%d of %d matches):", resp.Returned, resp.TotalMatches)
	}
	for _, m := range resp.Results {
		title := sanitizeSingleLineDisplay(m.Title)
		snippet := sanitizeSingleLineDisplay(m.Body)
		header := fmt.Sprintf("  %s [%s] %.2f", m.ID, m.Kind, m.Score)
		b.WriteByte('\n')
		b.WriteString(truncateEndWithEllipsis(header, avail))
		b.WriteByte('\n')
		b.WriteString("    " + truncateEndWithEllipsis(title, max(12, avail-4)))
		if snippet != "" && snippet != title {
			b.WriteByte('\n')
			b.WriteString("    " + truncateEndWithEllipsis(snippet, max(12, avail-4)))
		}
	}
	return b.String()
}

func memoryLocalWidth(width int) int {
	if width <= 0 {
		return 77
	}
	avail := width - 3 // Role "local" prepends "│  " (3 columns)
	if avail < 20 {
		return 20
	}
	return avail
}

// Keep unused tools import check happy if refactored; verify AuditWarningError type reference.
var _ = (*tools.AuditWarningError)(nil)
