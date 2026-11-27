package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// renderHeader builds a quiet, responsive hairline status bar.
// Width >= 60: single line followed by subtle hairline divider.
// Width < 60: two compact lines followed by subtle hairline divider.
func renderHeader(m *uiModel, width int) string {
	if width <= 0 {
		width = 80
	}

	tokens := estimateSessionTokens(m.messages, m.projectCtx)
	budget := m.historyBudget
	if budget <= 0 {
		budget = defaultHistoryTokens
	}

	modelPill := sanitizeSingleLineDisplay(m.modelName)
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
	ws := sanitizeSingleLineDisplay(m.workspace)
	if ws == "" {
		ws = "."
	}

	var autoBadge string
	switch m.autoApprove {
	case AutoApproveReadOnly:
		autoBadge = headerAutoReadOnlyStyle.Render("Auto: ReadOnly")
	case AutoApproveAll:
		autoBadge = headerAutoAllStyle.Render("Auto: ALL")
	}

	hairline := headerDividerStyle.Render(strings.Repeat(symDivider, width))

	// Narrow layout (< 60 columns)
	if width < 60 {
		var line1 string
		if autoBadge != "" {
			prefix := headerIdentityStyle.Render(symIdentity) + " " + headerTitleStyle.Render("NabdCoder") + " " + autoBadge
			avail := width - lipgloss.Width(prefix) - 1
			displayModel := modelPill
			if avail >= 4 {
				line1 = prefix + " " + headerMutedStyle.Render(truncateEnd(displayModel, avail))
			} else if lipgloss.Width(prefix) <= width {
				line1 = prefix
			} else {
				line1 = truncateEnd(prefix, width)
			}
		} else {
			prefix := headerIdentityStyle.Render(symIdentity) + " " + headerTitleStyle.Render("NabdCoder") + "   "
			availModel := width - lipgloss.Width(prefix)
			displayModel := modelPill
			if availModel > 0 && lipgloss.Width(displayModel) > availModel {
				displayModel = truncateEnd(displayModel, availModel)
			}
			line1 = prefix + headerMutedStyle.Render(displayModel)
		}

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
		if lipgloss.Width(line2Text) > width {
			sbShort := "SB: Off"
			if m.sandboxProven {
				sbShort = "SB: Proven"
			}
			line2Text = fmt.Sprintf("%s %s %s", sbShort, ctxMetric, iterMetric)
		}
		if lipgloss.Width(line2Text) > width {
			line2Text = truncateEnd(line2Text, width)
		}
		line2 := headerMutedStyle.Render(line2Text)

		return line1 + "\n" + line2 + "\n" + hairline
	}

	// Desktop / standard terminal layout (>= 60 columns): single line
	// Format: ● NabdCoder   [Auto: ALL]   <model>   <workspace>   Sandbox: Off|Proven   0.4k/24k   Iter 0/10
	prefix := headerIdentityStyle.Render(symIdentity) + " " + headerTitleStyle.Render("NabdCoder")
	if autoBadge != "" {
		prefix += "   " + autoBadge
	}
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

// truncateEndWithEllipsis truncates a string from the end using the active
// ellipsis glyph ("…" in Unicode mode, "..." in ASCII mode).
func truncateEndWithEllipsis(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= maxLen {
		return s
	}
	ell := activeEllipsisGlyph()
	ellW := lipgloss.Width(ell)
	if maxLen <= ellW {
		if isASCIIMode() {
			return ell[:maxLen]
		}
		if maxLen == 1 {
			return "…"
		}
		return ""
	}
	runes := []rune(s)
	for len(runes) > 0 && lipgloss.Width(string(runes)+ell) > maxLen {
		runes = runes[:len(runes)-1]
	}
	return string(runes) + ell
}

// formatTurnDuration formats a completed turn's elapsed duration for local
// display. Sub-second turns return "" so they stay hidden.
func formatTurnDuration(d time.Duration) string {
	if d < time.Second {
		return ""
	}
	totalSecs := int64(d / time.Second)
	mins := totalSecs / 60
	secs := totalSecs % 60
	if mins == 0 {
		return fmt.Sprintf("Worked for %ds", secs)
	}
	hours := mins / 60
	remMins := mins % 60
	if hours > 0 {
		return fmt.Sprintf("Worked for %dh %dm %ds", hours, remMins, secs)
	}
	return fmt.Sprintf("Worked for %dm %ds", mins, secs)
}

// renderFooter builds a quiet, minimal footer with status, optional local turn
// timer, and responsive key hints. Security/approval instructions always take
// priority over staying on a single line.
func renderFooter(m *uiModel, width int) string {
	if width <= 0 {
		width = 80
	}

	sep := activeInlineSep()
	isSecurityPrompt := m.pendingDisclosure != nil || m.awaitingApproval

	var statusPart string
	statusText := sanitizeSingleLineDisplay(m.status)
	if m.busy {
		spin := m.spinner.View()
		if isASCIIMode() {
			spin = "*"
		} else if isColorDisabled() {
			spin = sanitizeForDisplay(spin)
		}
		spinW := lipgloss.Width(spin)
		if !isSecurityPrompt && lipgloss.Width(statusText)+spinW+1 > width {
			statusText = truncateEndWithEllipsis(statusText, max(1, width-spinW-1))
		}
		statusPart = spin + " " + renderStyledSpan(footerStatusStyle, statusText)
	} else {
		if statusText == "" {
			statusText = "Ready"
		}
		worked := ""
		if m.hasLastTurnDuration {
			worked = formatTurnDuration(m.lastTurnDuration)
		}
		if worked != "" {
			suffix := sep + worked
			if isSecurityPrompt || lipgloss.Width(statusText+suffix) <= width {
				statusPart = renderStyledSpan(footerStatusStyle, statusText) + renderStyledSpan(toolHintStyle, suffix)
			} else if rem := width - lipgloss.Width(suffix); rem >= 4 {
				statusPart = renderStyledSpan(footerStatusStyle, truncateEndWithEllipsis(statusText, rem)) + renderStyledSpan(toolHintStyle, suffix)
			} else {
				statusPart = renderStyledSpan(footerStatusStyle, truncateEndWithEllipsis(statusText+suffix, width))
			}
		} else {
			if !isSecurityPrompt && lipgloss.Width(statusText) > width {
				statusText = truncateEndWithEllipsis(statusText, width)
			}
			statusPart = renderStyledSpan(footerStatusStyle, statusText)
		}
	}

	wStatus := lipgloss.Width(statusPart)

	if m.pendingDisclosure != nil {
		var hintPart string
		check := activeCheckGlyph()
		if m.viewport.AtBottom() {
			if width >= 44 {
				hintPart = renderStyledSpan(scrollBottomReadyStyle, "At Bottom "+check+" (Press Y to send / N to reject)")
			} else {
				hintPart = renderStyledSpan(scrollBottomReadyStyle, "At Bottom "+check+" (Y send / N reject)")
			}
		} else {
			pct := int(m.viewport.ScrollPercent() * 100)
			if width >= 44 {
				hintPart = renderStyledSpan(scrollPercentStyle, fmt.Sprintf("Scroll: %d%% (Scroll to end before Y)", pct))
			} else {
				hintPart = renderStyledSpan(scrollPercentStyle, fmt.Sprintf("Scroll: %d%% (Scroll to end)", pct))
			}
		}
		wHint := lipgloss.Width(hintPart)
		if wStatus+wHint+2 <= width {
			return statusPart + strings.Repeat(" ", width-wStatus-wHint) + hintPart
		}
		return statusPart + "\n" + hintPart
	}

	if m.awaitingApproval {
		hintPart := renderStyledSpan(footerKeyHintStyle, "Y approve"+sep+"N reject")
		wHint := lipgloss.Width(hintPart)
		if wStatus+wHint+2 <= width {
			return statusPart + strings.Repeat(" ", width-wStatus-wHint) + hintPart
		}
		return statusPart + "\n" + hintPart
	}

	hintCandidates := []string{
		"Enter send" + sep + "Alt+Enter newline" + sep + "Esc cancel" + sep + "Ctrl+O tool" + sep + "/help",
		"Enter send" + sep + "Ctrl+O expand" + sep + "/help",
		"Enter send" + sep + "Esc cancel" + sep + "/help",
		"Enter send" + sep + "/help",
		"Enter" + sep + "/help",
		"/help",
	}

	for _, cand := range hintCandidates {
		wHint := lipgloss.Width(cand)
		if wStatus+wHint+2 <= width {
			hintPart := renderStyledSpan(footerKeyHintStyle, cand)
			return statusPart + strings.Repeat(" ", width-wStatus-wHint) + hintPart
		}
	}

	fallback := "Enter send" + sep + "/help"
	if width >= lipgloss.Width("Enter send"+sep+"Esc cancel"+sep+"/help") {
		fallback = "Enter send" + sep + "Esc cancel" + sep + "/help"
	} else if lipgloss.Width(fallback) > width {
		if lipgloss.Width("/help") <= width {
			fallback = "/help"
		} else {
			fallback = truncateEndWithEllipsis("/help", width)
		}
	}
	return statusPart + "\n" + renderStyledSpan(footerKeyHintStyle, fallback)
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

	case "edit_file", "create_file":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		displayPath := p.Path
		if displayPath == "" {
			displayPath = "file"
		}
		if strings.HasPrefix(result, "Successfully ") {
			return fmt.Sprintf("%s(%s) -> %s", name, displayPath, result)
		}
		return fmt.Sprintf("%s(%s) -> %s", name, displayPath, truncate(result, 80))

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

	case "repo_map":
		var p struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		reqPath := p.Path
		if reqPath == "" {
			reqPath = "."
		}
		if strings.HasPrefix(result, "=== REPO MAP") {
			var scannedFiles, symbols int
			idx := strings.Index(result, "scanned:")
			if idx >= 0 {
				if _, err := fmt.Sscanf(result[idx:], "scanned: %d files, symbols: %d", &scannedFiles, &symbols); err == nil {
					return fmt.Sprintf("repo_map(%s) -> %d files, %d symbols", reqPath, scannedFiles, symbols)
				}
			}
		}
		return fmt.Sprintf("repo_map(%s) -> %d bytes", reqPath, len(result))

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

	case "web_search":
		if strings.HasPrefix(result, "Error:") || strings.HasPrefix(result, "Action rejected") {
			return "web_search -> " + truncate(result, 80)
		}
		var res struct {
			ResultCount int  `json:"result_count"`
			Truncated   bool `json:"truncated"`
		}
		if err := json.Unmarshal([]byte(result), &res); err == nil {
			if res.Truncated {
				return fmt.Sprintf("web_search -> %d results (truncated)", res.ResultCount)
			}
			return fmt.Sprintf("web_search -> %d results", res.ResultCount)
		}
		return fmt.Sprintf("web_search -> %d bytes", len(result))

	case "web_fetch":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(rawArgs), &p)
		host := "host"
		if u, err := url.Parse(p.URL); err == nil && u.Hostname() != "" {
			host = sanitizeForDisplay(u.Hostname())
		}
		if strings.HasPrefix(result, "Error:") || strings.HasPrefix(result, "Action rejected") {
			return fmt.Sprintf("web_fetch(%s) -> %s", host, truncate(result, 80))
		}
		var res struct {
			Bytes           int  `json:"bytes"`
			SourceTruncated bool `json:"source_truncated"`
			OutputTruncated bool `json:"output_truncated"`
		}
		if err := json.Unmarshal([]byte(result), &res); err == nil {
			if res.SourceTruncated || res.OutputTruncated {
				return fmt.Sprintf("web_fetch(%s) -> %d bytes (truncated)", host, res.Bytes)
			}
			return fmt.Sprintf("web_fetch(%s) -> %d bytes", host, res.Bytes)
		}
		return fmt.Sprintf("web_fetch(%s) -> %d bytes", host, len(result))

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
			strings.HasPrefix(line, "status:") || strings.HasPrefix(line, "size:") ||
			strings.HasPrefix(line, "content sha256:") ||
			strings.HasPrefix(line, "old sha256:") || strings.HasPrefix(line, "new sha256:") ||
			strings.HasPrefix(line, "arguments sha256:") || strings.HasPrefix(line, "pkg:") ||
			strings.HasPrefix(line, "command:") || strings.HasPrefix(line, "tool:") ||
			strings.HasPrefix(line, "destination:") || strings.HasPrefix(line, "firecrawl_endpoint:") ||
			strings.HasPrefix(line, "delegation_limit:") || strings.HasPrefix(line, "query_string:") ||
			strings.HasPrefix(line, "max_results:") || strings.HasPrefix(line, "query (") ||
			strings.HasPrefix(line, "query:"):
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

// splitByVisualWidth splits a single line (potentially containing ANSI CSI
// sequences and wide Unicode runes) into chunks each at most maxWidth columns
// wide according to lipgloss.Width, preserving active SGR styling across chunks.
func splitByVisualWidth(s string, maxWidth int) []string {
	if maxWidth <= 0 {
		maxWidth = 1
	}
	if lipgloss.Width(s) <= maxWidth {
		return []string{s}
	}

	var chunks []string
	var cur strings.Builder
	curWidth := 0
	var activeSGR strings.Builder

	flush := func() {
		if cur.Len() == 0 {
			return
		}
		if activeSGR.Len() > 0 {
			cur.WriteString("\x1b[0m")
		}
		chunks = append(chunks, cur.String())
		cur.Reset()
		if activeSGR.Len() > 0 {
			cur.WriteString(activeSGR.String())
		}
		curWidth = 0
	}

	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := i + 2
			for end < len(s) && !(s[end] >= 0x40 && s[end] <= 0x7e) {
				end++
			}
			if end < len(s) {
				end++
			}
			seq := s[i:end]
			cur.WriteString(seq)
			if strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					activeSGR.Reset()
				} else {
					activeSGR.WriteString(seq)
				}
			}
			i = end
			continue
		}

		r, size := utf8.DecodeRuneInString(s[i:])
		rs := s[i : i+size]
		rw := lipgloss.Width(rs)
		if curWidth > 0 && curWidth+rw > maxWidth {
			flush()
		}
		cur.WriteString(rs)
		curWidth += rw
		i += size
		_ = r
	}
	if cur.Len() > 0 {
		chunks = append(chunks, cur.String())
	}
	return chunks
}

// updateActiveSGR scans s for CSI SGR sequences (\x1b[...m) and returns the
// updated active SGR prefix.
func updateActiveSGR(s, activeSGR string) string {
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			end := i + 2
			for end < len(s) && !(s[end] >= 0x40 && s[end] <= 0x7e) {
				end++
			}
			if end < len(s) {
				end++
			}
			seq := s[i:end]
			if strings.HasSuffix(seq, "m") {
				if seq == "\x1b[0m" || seq == "\x1b[m" {
					activeSGR = ""
				} else {
					activeSGR += seq
				}
			}
			i = end
			continue
		}
		i++
	}
	return activeSGR
}

// wrapIndented splits and wraps text such that the first line is prefixed with prefix
// and all subsequent lines are prefixed with indent, ensuring no line exceeds totalWidth.
func wrapIndented(prefix, indent, text string, totalWidth int) string {
	if totalWidth <= 0 {
		totalWidth = 80
	}
	pw := lipgloss.Width(prefix)
	iw := lipgloss.Width(indent)
	avail := max(1, totalWidth-max(pw, iw))

	rawLines := strings.Split(text, "\n")
	var wrappedLines []string
	activeSGR := ""

	for _, rl := range rawLines {
		if lipgloss.Width(rl) <= avail {
			line := rl
			if activeSGR != "" && !strings.HasPrefix(line, "\x1b[") {
				line = activeSGR + line
			}
			activeSGR = updateActiveSGR(rl, activeSGR)
			if activeSGR != "" && !strings.HasSuffix(line, "\x1b[0m") {
				line += "\x1b[0m"
			}
			wrappedLines = append(wrappedLines, line)
			continue
		}

		// Word wrap line to avail columns while preserving active SGR across breaks.
		words := strings.Fields(rl)
		if len(words) == 0 {
			wrappedLines = append(wrappedLines, "")
			continue
		}

		var current strings.Builder
		if activeSGR != "" {
			current.WriteString(activeSGR)
		}

		emitCurrent := func() {
			line := current.String()
			if activeSGR != "" && !strings.HasSuffix(line, "\x1b[0m") {
				line += "\x1b[0m"
			}
			wrappedLines = append(wrappedLines, line)
			current.Reset()
			if activeSGR != "" {
				current.WriteString(activeSGR)
			}
		}

		for _, w := range words {
			if lipgloss.Width(current.String()) == 0 {
				if lipgloss.Width(w) > avail {
					parts := splitByVisualWidth(activeSGR+w, avail)
					for k := 0; k < len(parts)-1; k++ {
						wrappedLines = append(wrappedLines, parts[k])
					}
					last := parts[len(parts)-1]
					current.Reset()
					current.WriteString(last)
					activeSGR = updateActiveSGR(w, activeSGR)
				} else {
					current.WriteString(w)
					activeSGR = updateActiveSGR(w, activeSGR)
				}
				continue
			}

			if lipgloss.Width(current.String())+1+lipgloss.Width(w) <= avail {
				current.WriteString(" " + w)
				activeSGR = updateActiveSGR(w, activeSGR)
			} else {
				emitCurrent()
				if lipgloss.Width(w) > avail {
					parts := splitByVisualWidth(activeSGR+w, avail)
					for k := 0; k < len(parts)-1; k++ {
						wrappedLines = append(wrappedLines, parts[k])
					}
					last := parts[len(parts)-1]
					current.Reset()
					current.WriteString(last)
					activeSGR = updateActiveSGR(w, activeSGR)
				} else {
					current.WriteString(w)
					activeSGR = updateActiveSGR(w, activeSGR)
				}
			}
		}
		if lipgloss.Width(current.String()) > 0 || current.Len() > len(activeSGR) {
			line := current.String()
			if activeSGR != "" && !strings.HasSuffix(line, "\x1b[0m") {
				line += "\x1b[0m"
			}
			wrappedLines = append(wrappedLines, line)
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

const (
	toolGroupFoldThreshold = 5
	toolGroupKeepVisible   = 3
	maxExpandedToolBytes   = 4000
	maxExpandedToolLines   = 24
)

type toolBadgeMeta struct {
	label string
	style lipgloss.Style
}

// toolBadgeCatalog maps tool names to purely visual category badges.
// Visual badges never affect approval, disclosure, or model tool execution;
// grep_search and undo are included solely for display classification.
var toolBadgeCatalog = map[string]toolBadgeMeta{
	"read_file":   {label: "READ", style: toolBadgeReadStyle},
	"list_dir":    {label: "LIST", style: toolBadgeReadStyle},
	"repo_map":    {label: "MAP", style: toolBadgeReadStyle},
	"grep_search": {label: "GREP", style: toolBadgeReadStyle},
	"create_file": {label: "CREATE", style: toolBadgeEditStyle},
	"edit_file":   {label: "EDIT", style: toolBadgeEditStyle},
	"undo":        {label: "UNDO", style: toolBadgeEditStyle},
	"web_search":  {label: "WEB", style: toolBadgeWebStyle},
	"web_fetch":   {label: "FETCH", style: toolBadgeWebStyle},
	"go_verify":   {label: "VERIFY", style: toolBadgeVerifyStyle},
	"mcp_call":    {label: "MCP", style: toolBadgeVerifyStyle},
}

func lookupToolBadge(toolName string) toolBadgeMeta {
	clean := strings.TrimSpace(toolName)
	if meta, ok := toolBadgeCatalog[clean]; ok {
		return meta
	}
	if strings.HasPrefix(clean, "rea:") {
		return toolBadgeCatalog["mcp_call"]
	}
	return toolBadgeMeta{label: "TOOL", style: toolBadgeNeutralStyle}
}

func renderToolBadge(toolName string) string {
	meta := lookupToolBadge(toolName)
	return renderStyledSpan(meta.style, "["+meta.label+"]")
}

// isToolFailure reports whether a tool message represents a failure, rejection,
// or blocked action based on reliable control-plane and summary signals.
func isToolFailure(msg Message) bool {
	contentPrefixes := []string{
		"Tool output disclosure rejected",
		"Tool disclosure blocked",
		"Tool execution denied",
		"Rejected by user",
		"Rejected:",
		"Action rejected",
		"Cancelled by user",
		"Error:",
	}
	for _, p := range contentPrefixes {
		if strings.HasPrefix(msg.Content, p) {
			return true
		}
	}

	displayPrefixes := []string{
		"Tool output disclosure rejected",
		"Tool disclosure blocked",
		"Tool execution denied",
		"Rejected",
		"Blocked",
		"Skipped:",
		"Approval not recorded",
		"Error:",
	}
	for _, p := range displayPrefixes {
		if strings.HasPrefix(msg.Display, p) {
			return true
		}
	}

	return strings.Contains(msg.Display, "-> FAIL") ||
		strings.Contains(msg.Display, "-> Error:") ||
		strings.Contains(msg.Display, "-> Action rejected")
}

// isExpandableToolOutput reports whether a tool message has authorized,
// non-rejected, non-blocked output eligible for Ctrl+O expansion.
func isExpandableToolOutput(msg Message) bool {
	if !isEligibleToolOutput(msg) {
		return false
	}
	deniedPrefixes := []string{
		"Tool execution denied",
		"Action rejected by user",
		"Tool output disclosure rejected",
		"Tool disclosure blocked",
		"Approval not recorded",
	}
	for _, p := range deniedPrefixes {
		if strings.HasPrefix(msg.Content, p) || strings.HasPrefix(msg.Display, p) {
			return false
		}
	}
	return true
}

func toolLinePrefixes(glyphRendered string) (prefix, indent string) {
	bar := activeBarGlyph()
	prefix = renderStyledSpan(toolBarStyle, bar+" ") + glyphRendered + " "
	indent = renderStyledSpan(toolBarStyle, bar+"   ")
	return prefix, indent
}

func toolStatusPrefix(msg Message) (prefix, indent string) {
	if isToolFailure(msg) {
		return toolLinePrefixes(renderStyledSpan(toolErrorStyle, activeCrossGlyph()))
	}
	return toolLinePrefixes(renderStyledSpan(toolSuccessStyle, activeCheckGlyph()))
}

func toolWaitLine(name string, width int) string {
	prefix, indent := toolLinePrefixes(renderStyledSpan(toolPendingStyle, activeEllipsisGlyph()))
	cleanName := sanitizeForDisplay(name)
	badge := renderToolBadge(cleanName)
	text := renderStyledSpan(toolPendingStyle, cleanName) + " " + badge
	return wrapIndented(prefix, indent, text, width)
}

func resolveToolName(msg Message, toolNamesByID map[string]string) string {
	if msg.ToolCallID != "" {
		if name, ok := toolNamesByID[msg.ToolCallID]; ok && name != "" {
			return sanitizeForDisplay(name)
		}
	}
	d := strings.TrimSpace(msg.Display)
	d = strings.TrimPrefix(d, "Rejected ")
	if idx := strings.Index(d, "("); idx > 0 {
		candidate := strings.TrimSpace(d[:idx])
		if candidate != "" && !strings.ContainsAny(candidate, " \t\n") {
			return sanitizeForDisplay(candidate)
		}
	}
	if idx := strings.Index(d, " ->"); idx > 0 {
		candidate := strings.TrimSpace(d[:idx])
		if candidate != "" && !strings.ContainsAny(candidate, " \t\n") {
			return sanitizeForDisplay(candidate)
		}
	}
	if d != "" && !strings.ContainsAny(d, " \t\n") {
		return sanitizeForDisplay(d)
	}
	return "tool"
}

func formatFoldSummary(folded []Message, toolNamesByID map[string]string) string {
	counts := make(map[string]int)
	for _, msg := range folded {
		name := resolveToolName(msg, toolNamesByID)
		counts[name]++
	}
	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})

	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, fmt.Sprintf("%d %s", counts[name], name))
	}
	return fmt.Sprintf("+%d earlier tool calls (%s)", len(folded), strings.Join(parts, ", "))
}

func renderAssistantMessage(msg Message, resolvedIDs map[string]bool, width int) string {
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	if strings.TrimSpace(msg.Content) != "" {
		rendered := renderMarkdownSafe(msg.Content, max(10, width-3))
		prefix := renderStyledSpan(asstPrefixStyle, activeIdentityGlyph()+"  ")
		indent := "   "
		b.WriteString(wrapIndented(prefix, indent, rendered, width))
	}
	for _, tc := range msg.ToolCalls {
		if tc.ID != "" && resolvedIDs[tc.ID] {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		if tc.ID != "" {
			b.WriteString(toolWaitLine(tc.Function.Name, width))
		} else {
			bar := renderStyledSpan(toolBarStyle, activeBarGlyph()+"  ")
			cleanName := sanitizeForDisplay(tc.Function.Name)
			callLine := fmt.Sprintf("call: %s ", cleanName)
			b.WriteString(wrapIndented(bar, bar, renderStyledSpan(toolNameStyle, callLine)+renderToolBadge(cleanName), width))
		}
	}
	return b.String()
}

// compactArgPath shortens directory prefixes inside a tool argument path while
// preserving the basename and indicating truncation with the active ellipsis.
func compactArgPath(arg string, maxLen int) string {
	if maxLen <= 0 || lipgloss.Width(arg) <= maxLen {
		return arg
	}
	ell := activeEllipsisGlyph()
	if strings.Contains(arg, "/") {
		base := filepath.Base(arg)
		if base != "" && base != "." && base != "/" {
			cand := ell + "/" + base
			if lipgloss.Width(cand) <= maxLen {
				return cand
			}
		}
	}
	return truncateEndWithEllipsis(arg, maxLen)
}

// compactToolDisplay shortens non-security parts of a successful tool summary
// on narrow screens so each collapsed tool stays on a single line when possible.
func compactToolDisplay(disp, toolName string, avail int) string {
	if avail <= 0 || lipgloss.Width(disp) <= avail {
		return disp
	}
	// Try shortening path/argument inside name(arg) -> rhs first.
	callPart, rhs, hasArrow := strings.Cut(disp, " -> ")
	if openIdx := strings.IndexByte(callPart, '('); openIdx > 0 && strings.HasSuffix(callPart, ")") {
		fn := callPart[:openIdx]
		arg := callPart[openIdx+1 : len(callPart)-1]
		if hasArrow {
			overhead := lipgloss.Width(fn) + lipgloss.Width("() -> ") + lipgloss.Width(rhs)
			minUsefulArg := 8
			if strings.Contains(arg, "/") {
				base := filepath.Base(arg)
				if w := lipgloss.Width(activeEllipsisGlyph() + "/" + base); w > minUsefulArg {
					minUsefulArg = min(w, 16)
				}
			}
			if avail-overhead >= minUsefulArg {
				compactedArg := compactArgPath(arg, avail-overhead)
				cand := fmt.Sprintf("%s(%s) -> %s", fn, compactedArg, rhs)
				if lipgloss.Width(cand) <= avail {
					return cand
				}
			}
			if argAvail := avail - lipgloss.Width(fn) - 2; argAvail >= 4 {
				compactedArg := compactArgPath(arg, argAvail)
				cand := fmt.Sprintf("%s(%s)", fn, compactedArg)
				if lipgloss.Width(cand) <= avail {
					return cand
				}
			}
		} else if argAvail := avail - lipgloss.Width(fn) - 2; argAvail >= 4 {
			compactedArg := compactArgPath(arg, argAvail)
			cand := fmt.Sprintf("%s(%s)", fn, compactedArg)
			if lipgloss.Width(cand) <= avail {
				return cand
			}
		}
	}

	if lipgloss.Width(toolName)+lipgloss.Width(activeEllipsisGlyph()) <= avail {
		return truncateEndWithEllipsis(disp, avail)
	}
	return disp
}

// renderExpandedToolTree renders sanitized tool output using quiet muted tree
// connectors ("│ " / "└─ ", or "| " / "+- " in ASCII mode).
func renderExpandedToolTree(rawContent, disp, indent string, width int) string {
	sanitized := sanitizeForDisplay(rawContent)
	byteCapped := false
	preview := sanitized
	if len(preview) > maxExpandedToolBytes {
		cut := maxExpandedToolBytes
		for cut > 0 && !utf8.RuneStart(preview[cut]) {
			cut--
		}
		preview = preview[:cut]
		byteCapped = true
	}

	midPrefix := indent + renderStyledSpan(toolTreeStyle, activeTreeMidPrefix())
	lastPrefix := indent + renderStyledSpan(toolTreeStyle, activeTreeLastPrefix())
	lastContIndent := indent + "   "

	if !byteCapped && strings.TrimSpace(preview) == "" {
		return wrapIndented(lastPrefix, lastContIndent, renderStyledSpan(toolHintStyle, "(empty output)"), width)
	}

	if !byteCapped {
		preview = strings.TrimRight(preview, "\n")
	}
	lines := strings.Split(preview, "\n")

	upstreamTruncated := strings.Contains(disp, "(truncated") ||
		strings.Contains(sanitized, "[truncated;") ||
		strings.Contains(sanitized, compactMarkerPrefix) ||
		(len(rawContent) >= maxLLMToolOutput && strings.HasSuffix(rawContent, "..."))

	var tailNotice string
	visible := lines
	if byteCapped {
		if len(visible) > maxExpandedToolLines {
			visible = visible[:maxExpandedToolLines]
			tailNotice = fmt.Sprintf("... (output preview capped at %d chars; showing first %d lines)", maxExpandedToolBytes, maxExpandedToolLines)
		} else {
			tailNotice = fmt.Sprintf("... (output preview capped at %d chars)", maxExpandedToolBytes)
		}
	} else if len(lines) > maxExpandedToolLines {
		visible = lines[:maxExpandedToolLines]
		if upstreamTruncated {
			tailNotice = "... (additional lines omitted; output truncated)"
		} else {
			tailNotice = fmt.Sprintf("+%d more lines", len(lines)-maxExpandedToolLines)
		}
	} else if upstreamTruncated {
		tailNotice = "... (output truncated)"
	}

	var b strings.Builder
	for i, line := range visible {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i == len(visible)-1 && tailNotice == "" {
			b.WriteString(wrapIndented(lastPrefix, lastContIndent, line, width))
		} else {
			b.WriteString(wrapIndented(midPrefix, midPrefix, line, width))
		}
	}
	if tailNotice != "" {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(wrapIndented(lastPrefix, lastContIndent, renderStyledSpan(toolHintStyle, tailNotice), width))
	}
	return b.String()
}

func renderToolMessage(msg Message, width int, showCtrlO bool, expanded bool) string {
	return renderToolMessageWithName(msg, resolveToolName(msg, nil), width, showCtrlO, expanded)
}

func renderToolMessageWithName(msg Message, toolName string, width int, showCtrlO bool, expanded bool) string {
	if width <= 0 {
		width = 80
	}
	disp := msg.Display
	if disp == "" {
		disp = truncate(msg.Content, maxUIDisplayChars)
	}
	disp = sanitizeForDisplay(disp)
	prefix, indent := toolStatusPrefix(msg)
	badge := renderToolBadge(toolName)
	eligible := isExpandableToolOutput(msg)

	if showCtrlO && expanded && eligible {
		collapseHint := renderStyledSpan(toolHintStyle, "(Ctrl+O to collapse)")
		headerCore := badge + " " + renderStyledSpan(toolNameStyle, disp)
		bannerText := "─── Full Output ───"
		if isASCIIMode() {
			bannerText = "--- Full Output ---"
		}
		if availBanner := max(1, width-lipgloss.Width(indent)); lipgloss.Width(bannerText) > availBanner {
			bannerText = truncateEndWithEllipsis(bannerText, availBanner)
		}
		banner := renderStyledSpan(toolHintStyle, bannerText)

		var b strings.Builder
		if lipgloss.Width(prefix)+lipgloss.Width(headerCore)+2+lipgloss.Width(collapseHint) <= width {
			b.WriteString(wrapIndented(prefix, indent, headerCore+"  "+collapseHint, width) + "\n")
		} else {
			b.WriteString(wrapIndented(prefix, indent, headerCore, width) + "\n")
			b.WriteString(wrapIndented(indent, indent, collapseHint, width) + "\n")
		}
		b.WriteString(wrapIndented(indent, indent, banner, width) + "\n")
		b.WriteString(renderExpandedToolTree(msg.Content, disp, indent, width))
		return b.String()
	}

	summaryText := disp
	hintText := ""
	if showCtrlO && eligible {
		hintText = "  " + renderStyledSpan(toolHintStyle, "(Ctrl+O)")
	}
	if !isToolFailure(msg) {
		avail := width - lipgloss.Width(prefix) - lipgloss.Width(badge) - 1 - lipgloss.Width(hintText)
		if avail > 0 {
			summaryText = compactToolDisplay(summaryText, toolName, avail)
		}
	}

	toolLine := badge + " " + renderStyledSpan(toolNameStyle, summaryText) + hintText
	return wrapIndented(prefix, indent, toolLine, width)
}

func collectResolvedToolIDs(messages []Message, assistantIdx int) map[string]bool {
	resolved := make(map[string]bool)
	for j := assistantIdx + 1; j < len(messages); j++ {
		r := messages[j].Role
		if r == "user" || r == "assistant" {
			break
		}
		if r == "tool" && messages[j].ToolCallID != "" {
			resolved[messages[j].ToolCallID] = true
		}
	}
	return resolved
}

func buildToolNamesByID(messages []Message) map[string]string {
	names := make(map[string]string)
	for _, msg := range messages {
		if msg.Role != "assistant" {
			continue
		}
		for _, tc := range msg.ToolCalls {
			if tc.ID != "" && tc.Function.Name != "" {
				names[tc.ID] = tc.Function.Name
			}
		}
	}
	return names
}

func visibleExpandableToolIndex(messages []Message) int {
	expandableIdx := -1
	for i := 0; i < len(messages); {
		if messages[i].Role != "tool" {
			i++
			continue
		}
		j := i
		for j < len(messages) && messages[j].Role == "tool" {
			j++
		}
		visStart := i
		if j-i > toolGroupFoldThreshold {
			visStart = j - toolGroupKeepVisible
		}
		for k := visStart; k < j; k++ {
			if isExpandableToolOutput(messages[k]) {
				expandableIdx = k
			}
		}
		i = j
	}
	return expandableIdx
}

// renderMessages renders the full conversation stream with:
// - deduplicated completed tool calls and quiet waiting indicators,
// - compact contiguous tool result groups (no blank lines inside a group),
// - folding for groups exceeding 5 tool results (keeping the last 3 visible),
// - status indicators (✓/✗ or ASCII +/x), and
// - a single (Ctrl+O) hint on the newest visible eligible tool output.
func renderMessages(messages []Message, width int, expandLastTool bool) string {
	if width <= 0 {
		width = 80
	}
	toolNamesByID := buildToolNamesByID(messages)
	expandableIdx := visibleExpandableToolIndex(messages)

	var blocks []string
	for i := 0; i < len(messages); {
		msg := messages[i]
		if msg.Role == "system" {
			i++
			continue
		}

		if msg.Role == "tool" {
			j := i
			for j < len(messages) && messages[j].Role == "tool" {
				j++
			}
			group := messages[i:j]
			var groupLines []string
			visOffset := 0
			if len(group) > toolGroupFoldThreshold {
				cut := len(group) - toolGroupKeepVisible
				summary := formatFoldSummary(group[:cut], toolNamesByID)
				prefix, indent := toolLinePrefixes(renderStyledSpan(toolPendingStyle, activeEllipsisGlyph()))
				groupLines = append(groupLines, wrapIndented(prefix, indent, renderStyledSpan(toolPendingStyle, summary), width))
				visOffset = cut
			}
			for k := visOffset; k < len(group); k++ {
				globalIdx := i + k
				isTarget := globalIdx == expandableIdx
				toolName := resolveToolName(group[k], toolNamesByID)
				groupLines = append(groupLines, renderToolMessageWithName(group[k], toolName, width, isTarget, isTarget && expandLastTool))
			}
			blocks = append(blocks, strings.Join(groupLines, "\n"))
			i = j
			continue
		}

		var rendered string
		if msg.Role == "assistant" {
			resolved := collectResolvedToolIDs(messages, i)
			rendered = renderAssistantMessage(msg, resolved, width)
		} else {
			rendered = renderMessageCard(msg, width, false, false)
		}
		if rendered != "" {
			blocks = append(blocks, rendered)
		}
		i++
	}

	return strings.Join(blocks, "\n\n")
}

// renderMessageCard renders a single message in a quiet, borderless stream.
func renderMessageCard(msg Message, width int, isLastTool bool, expanded bool) string {
	if width <= 0 {
		width = 80
	}

	switch msg.Role {
	case "user":
		content := sanitizeForDisplay(msg.Content)
		prefix := userPrefixStyle.Render(activePromptGlyph() + "  ")
		indent := "   "
		return wrapIndented(prefix, indent, userTextStyle.Render(content), width)

	case "assistant":
		return renderAssistantMessage(msg, nil, width)

	case "tool":
		return renderToolMessage(msg, width, isLastTool, expanded)

	case "error":
		body := sanitizeForDisplay(msg.Content)
		bar := errorBarStyle.Render(activeBarGlyph() + "  ")
		prefix := bar + errorLabelStyle.Render("Error: ")
		indent := bar
		return wrapIndented(prefix, indent, errorTextStyle.Render(body), width)

	case "local":
		body := sanitizeForDisplay(msg.Content)
		bar := localBarStyle.Render(activeBarGlyph() + "  ")
		lines := strings.Split(body, "\n")
		var b strings.Builder
		for i, l := range lines {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(wrapIndented(bar, bar, localTextStyle.Render(l), width))
		}
		return b.String()

	default:
		return sanitizeForDisplay(msg.Content)
	}
}

// formatHelp builds the agent help guide dynamically from the single source of truth slashCommands.
func formatHelp() string {
	return formatHelpWithProject(projectContextSnapshot{})
}

func formatHelpWithProject(snap projectContextSnapshot) string {
	var b strings.Builder
	b.WriteString("=== NabdCoder Help ===\nCommands:\n")
	for _, cmd := range slashCommands {
		cmdAlias := fmt.Sprintf("/%s, %s", cmd.Name, cmd.Name)
		fmt.Fprintf(&b, "  %-15s - %s\n", cmdAlias, cmd.Description)
	}
	b.WriteString("  undo [path]     - Restore file from shadow backup\n")
	b.WriteString("  exit, quit      - Exit NabdCoder\n\n")
	b.WriteString("Auto-approval (NABD_AUTO_APPROVE or /auto):\n")
	b.WriteString("  off      - every tool approval and disclosure needs a manual decision (default)\n")
	b.WriteString("  readonly - read-only tool output is disclosed automatically unless the advisory secret heuristic matches; that heuristic is not a guarantee\n")
	b.WriteString("  all      - needs an explicit y to enable; bypasses approvals and disclosure review, except go_verify and mcp_call which always need a manual approval\n")
	b.WriteString("  A mode change never authorizes tool calls that were already requested.\n\n")
	if snap.Disabled {
		b.WriteString("Project Commands: disabled via NABD_NO_PROJECT_RULES=1\n\n")
	} else if len(snap.Commands) > 0 {
		b.WriteString("Project Commands (.nabd/commands/*.md):\n")
		for _, cmd := range snap.Commands {
			cmdName := "/" + sanitizeForDisplay(cmd.Name)
			fmt.Fprintf(&b, "  %-15s - [project] %s\n", cmdName, sanitizeForDisplay(cmd.Description))
		}
		b.WriteString("\n")
	}
	b.WriteString(`Keybindings:
  Enter           - Send prompt to model
  Alt+Enter/Ctrl+J- Insert newline in multi-line prompt
  Up / Down       - Navigate prompt history (at line 0)
  Esc             - Cancel active request (or reject approval/disclosure)
  Ctrl+O          - Toggle last tool output full view / summary
  Ctrl+C          - Force quit`)
	return b.String()
}
