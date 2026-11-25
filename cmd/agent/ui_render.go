package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderHeader builds a responsive, information-dense status bar.
// On small screens (< 60 columns), it displays a compact layout.
func renderHeader(m *uiModel, width int) string {
	tokens := estimateMessages(m.messages)
	budget := m.historyBudget
	if budget <= 0 {
		budget = defaultHistoryTokens
	}

	modelPill := m.modelName
	if modelPill == "" || modelPill == "unknown" {
		modelPill = "default-model"
	}

	sandboxText := "Sandbox: Disabled"
	sandboxStyle := headerPillWarnStyle
	if m.sandboxProven {
		sandboxText = "Sandbox: Proven"
		sandboxStyle = headerPillSuccessStyle
	}

	ctxMetric := fmt.Sprintf("~%.1fk/%dk", float64(tokens)/1000.0, budget/1000)
	iterMetric := fmt.Sprintf("Iter %d/%d", m.iteration, m.maxIterations)

	// Narrow screen (< 60 columns) compact rendering for Termux
	if width < 60 {
		line1 := headerLogoStyle.Render("NabdCoder") + " " + headerPillStyle.Render(modelPill)
		line2 := fmt.Sprintf("%s | %s | %s", ctxMetric, iterMetric, sandboxStyle.Render(sandboxText))
		return line1 + "\n" + line2
	}

	// Desktop / standard terminal layout
	left := lipgloss.JoinHorizontal(
		lipgloss.Center,
		headerLogoStyle.Render("NabdCoder"),
		" ",
		headerPillStyle.Render(modelPill),
		" ",
		sandboxStyle.Render(sandboxText),
	)

	right := lipgloss.JoinHorizontal(
		lipgloss.Center,
		headerMetricStyle.Render(ctxMetric),
		" ",
		headerMetricStyle.Render(iterMetric),
	)

	gap := max(1, width-lipgloss.Width(left)-lipgloss.Width(right)-2)
	return headerBarStyle.Width(width).Render(left + strings.Repeat(" ", gap) + right)
}

// renderFooter builds an interactive footer with spinner, active status,
// disclosure progress, and contextual key hints.
func renderFooter(m *uiModel, width int) string {
	var statusPart string
	if m.busy {
		statusPart = m.spinner.View() + " " + footerStatusStyle.Render(m.status)
	} else if m.status != "" {
		statusPart = footerStatusStyle.Render(m.status)
	} else {
		statusPart = footerStatusStyle.Render("Ready")
	}

	var hintPart string
	if m.pendingDisclosure != nil {
		if m.viewport.AtBottom() {
			hintPart = scrollBottomReadyStyle.Render("At Bottom ✓ (Press Y to send / N to reject)")
		} else {
			pct := int(m.viewport.ScrollPercent() * 100)
			hintPart = scrollPercentStyle.Render(fmt.Sprintf("Scroll: %d%% (Scroll to end before Y)", pct))
		}
	} else if m.awaitingApproval {
		hintPart = footerKeyPillStyle.Render("Y") + " " + footerKeyHintStyle.Render("approve") + "  " +
			footerKeyPillStyle.Render("N") + " " + footerKeyHintStyle.Render("reject")
	} else {
		if width >= 65 {
			hintPart = footerKeyHintStyle.Render("Enter: send | Alt+Enter: newline | Esc: cancel | Ctrl+O: tool | /help")
		} else {
			hintPart = footerKeyHintStyle.Render("Enter: send | Esc: cancel | /help")
		}
	}

	if width < 50 {
		return statusPart + "\n" + hintPart
	}

	gap := max(1, width-lipgloss.Width(statusPart)-lipgloss.Width(hintPart)-2)
	return footerBarStyle.Width(width).Render(statusPart + strings.Repeat(" ", gap) + hintPart)
}

// formatToolSummary produces a clear, structured one-line summary of a tool execution.
func formatToolSummary(name string, rawArgs string, result string) string {
	switch name {
	case "read_file":
		var p struct {
			Path   string `json:"path"`
			Offset int64  `json:"offset"`
			Format string `json:"format"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		displayPath := p.Path
		if displayPath == "" {
			displayPath = "file"
		}

		// Check if result is JSON structured page
		var page struct {
			Content    string `json:"content"`
			Truncated  bool   `json:"truncated"`
			NextOffset *int64 `json:"next_offset"`
		}
		if err := json.Unmarshal([]byte(result), &page); err == nil && page.Content != "" {
			if page.Truncated && page.NextOffset != nil {
				return fmt.Sprintf("read_file(%s) -> %d bytes (truncated, next offset: %d)",
					displayPath, len(page.Content), *page.NextOffset)
			}
			return fmt.Sprintf("read_file(%s) -> %d bytes", displayPath, len(page.Content))
		}

		// Text mode
		if strings.Contains(result, "[truncated;") {
			return fmt.Sprintf("read_file(%s) -> %d bytes (truncated)", displayPath, len(result))
		}
		return fmt.Sprintf("read_file(%s) -> %d bytes", displayPath, len(result))

	case "edit_file":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		displayPath := p.Path
		if displayPath == "" {
			displayPath = "file"
		}
		if strings.HasPrefix(result, "Successfully updated") {
			return fmt.Sprintf("edit_file(%s) -> %s", displayPath, result)
		}
		return fmt.Sprintf("edit_file(%s) -> %s", displayPath, truncate(result, 80))

	case "list_dir":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		dirPath := p.Path
		if dirPath == "" {
			dirPath = "."
		}
		lines := strings.Split(strings.TrimSpace(result), "\n")
		entries := 0
		for _, l := range lines {
			l = strings.TrimSpace(l)
			if strings.HasPrefix(l, "F ") || strings.HasPrefix(l, "D ") {
				entries++
			}
		}
		return fmt.Sprintf("list_dir(%s) -> %d entries", dirPath, entries)

	case "go_verify":
		var p struct {
			Pkg string `json:"pkg"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		pkg := p.Pkg
		if pkg == "" {
			pkg = "./..."
		}
		if strings.HasPrefix(result, "PASS") {
			firstLine := strings.SplitN(result, "\n", 2)[0]
			return fmt.Sprintf("go_verify(%s) -> %s", pkg, firstLine)
		}
		return fmt.Sprintf("go_verify(%s) -> FAIL (tests failed)", pkg)

	case "mcp_call":
		var p struct {
			Command string `json:"command"`
			Tool    string `json:"tool"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		return fmt.Sprintf("mcp_call(%s:%s) -> %d bytes", p.Command, p.Tool, len(result))

	default:
		return name + " -> " + truncate(result, maxUIDisplayChars)
	}
}

// renderApprovalSummary enhances git-like diffs visually with red/green/gray styles
// while preserving the exact text content (including prefixes -, +).
func renderApprovalSummary(summary string, width int) string {
	if summary == "" {
		return ""
	}
	lines := strings.Split(summary, "\n")
	var b strings.Builder
	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "-"):
			b.WriteString(diffDelStyle.Render(line))
		case strings.HasPrefix(line, "+ ") || strings.HasPrefix(line, "+"):
			b.WriteString(diffAddStyle.Render(line))
		case strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++"):
			b.WriteString(diffHeaderStyle.Render(line))
		case strings.HasPrefix(line, "path:") || strings.HasPrefix(line, "base sha256:") ||
			strings.HasPrefix(line, "old sha256:") || strings.HasPrefix(line, "new sha256:") ||
			strings.HasPrefix(line, "arguments sha256:") || strings.HasPrefix(line, "pkg:") ||
			strings.HasPrefix(line, "command:") || strings.HasPrefix(line, "tool:"):
			b.WriteString(diffInfoStyle.Render(line))
		default:
			b.WriteString(line)
		}
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

var (
	inlineCodeRegex = regexp.MustCompile("`([^`]+)`")
	boldRegex       = regexp.MustCompile(`\*\*([^*]+)\*\*`)
)

// renderMarkdownSafe renders lightweight markdown (headings, code blocks, bullet points,
// inline code) strictly AFTER the text has been sanitized via sanitizeForDisplay.
func renderMarkdownSafe(rawText string, width int) string {
	// Security invariant: sanitize first
	clean := sanitizeForDisplay(rawText)
	if width <= 0 {
		width = 80
	}

	lines := strings.Split(clean, "\n")
	var b strings.Builder
	inCodeBlock := false
	var codeLang string
	var codeBuffer strings.Builder

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		// Check fenced code block boundary
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inCodeBlock {
				// Close code block
				inCodeBlock = false
				codeContent := strings.TrimRight(codeBuffer.String(), "\n")
				blockWidth := max(20, width-4)
				renderedBlock := mdCodeBlockStyle.Width(blockWidth).Render(codeContent)
				if codeLang != "" {
					b.WriteString(mdCodeLangStyle.Render(" "+codeLang) + "\n")
				}
				b.WriteString(renderedBlock + "\n")
				codeBuffer.Reset()
				codeLang = ""
			} else {
				// Open code block
				inCodeBlock = true
				codeLang = strings.TrimPrefix(trimmed, "```")
				codeBuffer.Reset()
			}
			continue
		}

		if inCodeBlock {
			codeBuffer.WriteString(line + "\n")
			continue
		}

		// Heading 1
		if strings.HasPrefix(line, "# ") {
			title := strings.TrimPrefix(line, "# ")
			b.WriteString(mdHeading1Style.Render("# "+title) + "\n")
			continue
		}
		// Heading 2
		if strings.HasPrefix(line, "## ") {
			title := strings.TrimPrefix(line, "## ")
			b.WriteString(mdHeading2Style.Render("## "+title) + "\n")
			continue
		}
		// Heading 3
		if strings.HasPrefix(line, "### ") {
			title := strings.TrimPrefix(line, "### ")
			b.WriteString(mdHeading3Style.Render("### "+title) + "\n")
			continue
		}

		// Bullet lists
		if strings.HasPrefix(trimmed, "* ") || strings.HasPrefix(trimmed, "- ") {
			item := trimmed[2:]
			itemFormatted := formatInlineMarkdown(item)
			b.WriteString(mdBulletStyle.Render("  • ") + itemFormatted + "\n")
			continue
		}

		// Numbered list (e.g. "1. ")
		if len(trimmed) > 3 && trimmed[0] >= '0' && trimmed[0] <= '9' && trimmed[1] == '.' && trimmed[2] == ' ' {
			prefix := trimmed[:3]
			item := trimmed[3:]
			itemFormatted := formatInlineMarkdown(item)
			b.WriteString(lipgloss.NewStyle().Foreground(colorInfo).Render("  "+prefix) + itemFormatted + "\n")
			continue
		}

		// Normal paragraph with inline formatting
		b.WriteString(formatInlineMarkdown(line) + "\n")
	}

	// Unclosed code block fallback
	if inCodeBlock && codeBuffer.Len() > 0 {
		blockWidth := max(20, width-4)
		b.WriteString(mdCodeBlockStyle.Width(blockWidth).Render(strings.TrimRight(codeBuffer.String(), "\n")) + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// formatInlineMarkdown handles `inline code` and **bold** text.
func formatInlineMarkdown(s string) string {
	// Format inline code
	s = inlineCodeRegex.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		return mdInlineCodeStyle.Render(inner)
	})
	// Format bold
	s = boldRegex.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return lipgloss.NewStyle().Bold(true).Render(inner)
	})
	return s
}

// renderMessageCard renders a single message inside a structured, rounded card.
func renderMessageCard(msg Message, width int, isLastTool bool, expanded bool) string {
	cardWidth := max(20, width-4)

	switch msg.Role {
	case "user":
		content := sanitizeForDisplay(msg.Content)
		body := userStyle.Render("You: ") + content
		return userCardStyle.Width(cardWidth).Render(body)

	case "assistant":
		var b strings.Builder
		if strings.TrimSpace(msg.Content) != "" {
			rendered := renderMarkdownSafe(msg.Content, cardWidth-2)
			b.WriteString(assistantStyle.Render("NabdCoder: ") + rendered)
		}
		for _, tc := range msg.ToolCalls {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(toolStyle.Render(fmt.Sprintf("Call: %s", sanitizeForDisplay(tc.Function.Name))))
		}
		return asstCardStyle.Width(cardWidth).Render(b.String())

	case "tool":
		disp := msg.Display
		if disp == "" {
			disp = truncate(msg.Content, maxUIDisplayChars)
		}
		var content string
		if isLastTool && expanded {
			// Expanded view showing full / larger output
			full := sanitizeForDisplay(msg.Content)
			if len(full) > 4000 {
				full = full[:4000] + "\n...[output preview capped at 4000 chars]"
			}
			content = toolStyle.Render("Tool: "+sanitizeForDisplay(disp)) + "\n" +
				lipgloss.NewStyle().Foreground(colorMuted).Italic(true).Render("─── Full Output (Ctrl+O to collapse) ───") + "\n" +
				full
		} else {
			content = toolStyle.Render("Tool: "+sanitizeForDisplay(disp)) + "  " +
				lipgloss.NewStyle().Foreground(colorMuted).Render("[Ctrl+O to expand]")
		}
		return toolCardStyle.Width(cardWidth).Render(content)

	case "error":
		body := errorStyle.Render("Error: " + sanitizeForDisplay(msg.Content))
		return errorCardStyle.Width(cardWidth).Render(body)

	case "local":
		body := statusStyle.Render("• ") + sanitizeForDisplay(msg.Content)
		return localCardStyle.Width(cardWidth).Render(body)

	default:
		return sanitizeForDisplay(msg.Content)
	}
}
