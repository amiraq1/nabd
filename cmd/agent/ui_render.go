package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// renderHeader builds a quiet, responsive hairline status bar.
// Width >= 60: single line followed by subtle hairline divider.
// Width < 60: two compact lines followed by subtle hairline divider.
func renderHeader(m *uiModel, width int) string {
	if width <= 0 {
		width = 80
	}

	tokens := estimateMessages(m.messages)
	budget := m.historyBudget
	if budget <= 0 {
		budget = defaultHistoryTokens
	}

	modelPill := m.modelName
	if modelPill == "" || modelPill == "unknown" {
		modelPill = "default-model"
	}

	sandboxText := "Sandbox: Off"
	if m.sandboxProven {
		sandboxText = "Sandbox: Proven"
	}

	ctxMetric := fmt.Sprintf("%.1fk/%dk", float64(tokens)/1000.0, budget/1000)
	iterMetric := fmt.Sprintf("Iter %d/%d", m.iteration, m.maxIterations)

	// Workspace path
	ws := m.workspace
	if ws == "" {
		ws = "."
	}

	hairline := headerDividerStyle.Render(strings.Repeat(symDivider, width))

	// Narrow layout (< 60 columns)
	if width < 60 {
		prefix := headerIdentityStyle.Render(symIdentity) + " " + headerTitleStyle.Render("NabdCoder") + "   "
		availModel := width - lipgloss.Width(prefix)
		displayModel := modelPill
		if availModel > 0 && lipgloss.Width(displayModel) > availModel {
			displayModel = truncateEnd(displayModel, availModel)
		}
		line1 := prefix + headerMutedStyle.Render(displayModel)

		// Line 2: Sandbox: Off|Proven   0.4k/24k   Iter 0/10
		// Spacing degrades from 3 spaces to 2 or 1 if needed to fit width without truncating Sandbox or Iter
		spacing := "   "
		line2Text := fmt.Sprintf("%s%s%s%s%s", sandboxText, spacing, ctxMetric, spacing, iterMetric)
		if lipgloss.Width(line2Text) > width {
			spacing = "  "
			line2Text = fmt.Sprintf("%s%s%s%s%s", sandboxText, spacing, ctxMetric, spacing, iterMetric)
		}
		if lipgloss.Width(line2Text) > width {
			spacing = " "
			line2Text = fmt.Sprintf("%s%s%s%s%s", sandboxText, spacing, ctxMetric, spacing, iterMetric)
		}
		line2 := headerMutedStyle.Render(line2Text)

		return line1 + "\n" + line2 + "\n" + hairline
	}

	// Desktop / standard terminal layout (>= 60 columns): single line
	// Format: ● NabdCoder   <model>   <workspace>   Sandbox: Off|Proven   0.4k/24k   Iter 0/10
	prefix := headerIdentityStyle.Render(symIdentity) + " " + headerTitleStyle.Render("NabdCoder")
	fixedSuffixText := fmt.Sprintf("   %s   %s   %s", sandboxText, ctxMetric, iterMetric)
	fixedSuffix := headerMutedStyle.Render(fixedSuffixText)

	// Available width for: "   <model>   <workspace>"
	fixedWidth := lipgloss.Width(prefix) + lipgloss.Width(fixedSuffix)
	availMiddle := width - fixedWidth

	modelDisplay := modelPill
	wsDisplay := ws

	if availMiddle >= 6 {
		// Try full first: "   " + modelDisplay + "   " + wsDisplay
		needed := 3 + lipgloss.Width(modelDisplay) + 3 + lipgloss.Width(wsDisplay)
		if needed > availMiddle {
			// Truncate workspace first (shorten from beginning keeping last folder)
			wsDisplay = truncateWorkspace(ws, max(3, availMiddle-6-lipgloss.Width(modelDisplay)))
			needed = 3 + lipgloss.Width(modelDisplay) + 3 + lipgloss.Width(wsDisplay)
			if needed > availMiddle {
				// Workspace already minimal; truncate model next
				availForModel := max(3, availMiddle-6-lipgloss.Width(wsDisplay))
				modelDisplay = truncateEnd(modelDisplay, availForModel)
			}
		}
	} else {
		// Extremely narrow space between fixed parts
		wsDisplay = ""
		modelDisplay = truncateEnd(modelDisplay, max(3, availMiddle-3))
	}

	var middle string
	if wsDisplay != "" {
		middle = fmt.Sprintf("   %s   %s", modelDisplay, wsDisplay)
	} else if modelDisplay != "" {
		middle = fmt.Sprintf("   %s", modelDisplay)
	}

	headerLine := prefix + headerMutedStyle.Render(middle) + fixedSuffix
	// Ensure headerLine strictly does not exceed width
	if lipgloss.Width(headerLine) > width {
		headerLine = truncateEnd(headerLine, width)
	}

	return headerLine + "\n" + hairline
}

// truncateWorkspace shortens a directory path from the start with "…",
// keeping the last folder name intact.
func truncateWorkspace(path string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(path) <= maxLen {
		return path
	}

	base := filepath.Base(path)
	if base == "" || base == "." || base == "/" || base == "\\" {
		base = path
	}

	candidate := "…/" + base
	if lipgloss.Width(candidate) <= maxLen {
		return candidate
	}

	if lipgloss.Width(base) <= maxLen {
		return base
	}

	return truncateEnd(base, maxLen)
}

// truncateEnd truncates a string from the end with "…".
func truncateEnd(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	if maxLen == 1 {
		return "…"
	}

	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)+"…") > maxLen {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + "…"
}

// renderFooter builds a quiet, minimal footer with status and key hints.
func renderFooter(m *uiModel, width int) string {
	if width <= 0 {
		width = 80
	}

	var statusPart string
	if m.busy {
		statusPart = m.spinner.View() + " " + footerStatusStyle.Render(m.status)
	} else if m.status != "" {
		statusPart = footerStatusStyle.Render(m.status)
	} else {
		statusPart = footerStatusStyle.Render("Ready")
	}

	fullHint := "Enter send · Alt+Enter newline · Esc cancel · Ctrl+O tool · /help"
	compactHint := "Enter send · Esc cancel · /help"

	var hintPart string
	if m.pendingDisclosure != nil {
		if m.viewport.AtBottom() {
			if width >= 44 {
				hintPart = scrollBottomReadyStyle.Render("At Bottom ✓ (Press Y to send / N to reject)")
			} else {
				hintPart = scrollBottomReadyStyle.Render("At Bottom ✓ (Y send / N reject)")
			}
		} else {
			pct := int(m.viewport.ScrollPercent() * 100)
			if width >= 44 {
				hintPart = scrollPercentStyle.Render(fmt.Sprintf("Scroll: %d%% (Scroll to end before Y)", pct))
			} else {
				hintPart = scrollPercentStyle.Render(fmt.Sprintf("Scroll: %d%% (Scroll to end)", pct))
			}
		}
	} else if m.awaitingApproval {
		hintPart = footerKeyHintStyle.Render("Y approve · N reject")
	} else {
		if width >= lipgloss.Width(fullHint) {
			hintPart = footerKeyHintStyle.Render(fullHint)
		} else {
			hintPart = footerKeyHintStyle.Render(compactHint)
		}
	}

	wStatus := lipgloss.Width(statusPart)
	wHint := lipgloss.Width(hintPart)

	// Single line if both fit comfortably with at least 2 spaces gap
	if wStatus+wHint+2 <= width {
		gap := width - wStatus - wHint
		return statusPart + strings.Repeat(" ", gap) + hintPart
	}

	// Two lines if narrow
	return statusPart + "\n" + hintPart
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
		if p.Command == "rea" {
			toolName := p.Tool
			if toolName == "" {
				toolName = "tool"
			}
			return fmt.Sprintf("rea:%s(...) -> %d bytes", toolName, len(result))
		}
		return fmt.Sprintf("mcp_call(%s:%s) -> %d bytes", p.Command, p.Tool, len(result))

	default:
		return name + " -> " + truncate(result, maxUIDisplayChars)
	}
}

// renderApprovalSummary enhances git-like diffs visually with quiet red/green/gray styles
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
				renderedBlock := mdCodeBlockStyle.Render(codeContent)
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
			b.WriteString(headerMutedStyle.Render("  "+prefix) + itemFormatted + "\n")
			continue
		}

		// Normal paragraph with inline formatting
		b.WriteString(formatInlineMarkdown(line) + "\n")
	}

	// Unclosed code block fallback
	if inCodeBlock && codeBuffer.Len() > 0 {
		b.WriteString(mdCodeBlockStyle.Render(strings.TrimRight(codeBuffer.String(), "\n")) + "\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// formatInlineMarkdown handles `inline code` and **bold** text.
func formatInlineMarkdown(s string) string {
	s = inlineCodeRegex.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[1 : len(m)-1]
		return mdInlineCodeStyle.Render(inner)
	})
	s = boldRegex.ReplaceAllStringFunc(s, func(m string) string {
		inner := m[2 : len(m)-2]
		return lipgloss.NewStyle().Bold(true).Render(inner)
	})
	return s
}

// wrapIndented splits and wraps text such that the first line is prefixed with prefix
// and all subsequent lines are prefixed with indent, ensuring no line exceeds totalWidth.
func wrapIndented(prefix, indent, text string, totalWidth int) string {
	if totalWidth <= 0 {
		totalWidth = 80
	}
	pw := lipgloss.Width(prefix)
	avail := max(10, totalWidth-pw)

	rawLines := strings.Split(text, "\n")
	var wrappedLines []string

	for _, rl := range rawLines {
		if lipgloss.Width(rl) <= avail {
			wrappedLines = append(wrappedLines, rl)
			continue
		}

		// Word wrap line to avail columns
		words := strings.Fields(rl)
		if len(words) == 0 {
			wrappedLines = append(wrappedLines, "")
			continue
		}

		var current strings.Builder
		for _, w := range words {
			if current.Len() == 0 {
				if lipgloss.Width(w) > avail {
					// Word itself is wider than available width: break into chunks
					runes := []rune(w)
					for len(runes) > 0 {
						chunkLen := min(avail, len(runes))
						wrappedLines = append(wrappedLines, string(runes[:chunkLen]))
						runes = runes[chunkLen:]
					}
				} else {
					current.WriteString(w)
				}
				continue
			}

			if lipgloss.Width(current.String())+1+lipgloss.Width(w) <= avail {
				current.WriteString(" " + w)
			} else {
				wrappedLines = append(wrappedLines, current.String())
				current.Reset()
				if lipgloss.Width(w) > avail {
					runes := []rune(w)
					for len(runes) > 0 {
						chunkLen := min(avail, len(runes))
						wrappedLines = append(wrappedLines, string(runes[:chunkLen]))
						runes = runes[chunkLen:]
					}
				} else {
					current.WriteString(w)
				}
			}
		}
		if current.Len() > 0 {
			wrappedLines = append(wrappedLines, current.String())
		}
	}

	if len(wrappedLines) == 0 {
		return prefix
	}

	var b strings.Builder
	b.WriteString(prefix + wrappedLines[0])
	for i := 1; i < len(wrappedLines); i++ {
		b.WriteString("\n" + indent + wrappedLines[i])
	}
	return b.String()
}

// renderMessageCard renders a single message in a quiet, borderless stream.
func renderMessageCard(msg Message, width int, isLastTool bool, expanded bool) string {
	if width <= 0 {
		width = 80
	}

	switch msg.Role {
	case "user":
		content := sanitizeForDisplay(msg.Content)
		prefix := userPrefixStyle.Render(symPrompt + "  ")
		indent := "   "
		return wrapIndented(prefix, indent, userTextStyle.Render(content), width)

	case "assistant":
		var b strings.Builder
		if strings.TrimSpace(msg.Content) != "" {
			rendered := renderMarkdownSafe(msg.Content, width-3)
			prefix := asstPrefixStyle.Render(symIdentity + "  ")
			indent := "   "
			b.WriteString(wrapIndented(prefix, indent, rendered, width))
		}
		for _, tc := range msg.ToolCalls {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			bar := toolBarStyle.Render(symBar + "  ")
			callLine := fmt.Sprintf("call: %s", sanitizeForDisplay(tc.Function.Name))
			b.WriteString(bar + toolNameStyle.Render(callLine))
		}
		return b.String()

	case "tool":
		disp := msg.Display
		if disp == "" {
			disp = truncate(msg.Content, maxUIDisplayChars)
		}
		disp = sanitizeForDisplay(disp)
		bar := toolBarStyle.Render(symBar + "  ")

		if isLastTool && expanded {
			full := sanitizeForDisplay(msg.Content)
			if len(full) > 4000 {
				full = full[:4000] + "\n...[output preview capped at 4000 chars]"
			}
			headerText := toolNameStyle.Render(disp) + "  " + toolHintStyle.Render("(Ctrl+O to collapse)")
			banner := toolHintStyle.Render("─── Full Output (Ctrl+O to collapse) ───")

			var b strings.Builder
			b.WriteString(wrapIndented(bar, bar, headerText, width) + "\n")
			b.WriteString(wrapIndented(bar, bar, banner, width))

			fullLines := strings.Split(full, "\n")
			for _, fl := range fullLines {
				b.WriteString("\n" + wrapIndented(bar, bar, fl, width))
			}
			return b.String()
		}

		toolLine := toolNameStyle.Render(disp) + "  " + toolHintStyle.Render("(Ctrl+O)")
		return wrapIndented(bar, bar, toolLine, width)

	case "error":
		body := sanitizeForDisplay(msg.Content)
		bar := errorBarStyle.Render(symBar + "  ")
		prefix := bar + errorLabelStyle.Render("Error: ")
		indent := bar
		return wrapIndented(prefix, indent, errorTextStyle.Render(body), width)

	case "local":
		body := sanitizeForDisplay(msg.Content)
		bar := localBarStyle.Render(symBar + "  ")
		lines := strings.Split(body, "\n")
		var b strings.Builder
		for i, l := range lines {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(bar + localTextStyle.Render(l))
		}
		return b.String()

	default:
		return sanitizeForDisplay(msg.Content)
	}
}

// formatHelp builds the agent help guide dynamically from the single source of truth slashCommands.
func formatHelp() string {
	var b strings.Builder
	b.WriteString("=== NabdCoder Help ===\nCommands:\n")
	for _, cmd := range slashCommands {
		cmdAlias := fmt.Sprintf("/%s, %s", cmd.Name, cmd.Name)
		fmt.Fprintf(&b, "  %-15s - %s\n", cmdAlias, cmd.Description)
	}
	b.WriteString("  undo [path]     - Restore file from shadow backup\n")
	b.WriteString("  exit, quit      - Exit NabdCoder\n\n")
	b.WriteString(`Keybindings:
  Enter           - Send prompt to model
  Alt+Enter/Ctrl+J- Insert newline in multi-line prompt
  Up / Down       - Navigate prompt history (at line 0)
  Esc             - Cancel active request (or reject approval/disclosure)
  Ctrl+O          - Toggle last tool output full view / summary
  Ctrl+C          - Force quit`)
	return b.String()
}
