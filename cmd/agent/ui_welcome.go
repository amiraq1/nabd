package main

import (
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/lipgloss"
)

// welcomeLogoUnicode renders "NABDCODE" in 4 rows of 48 terminal cells (<= 56).
var welcomeLogoUnicode = [][2]string{
	{"█▄  █  ▄█▄  ██▀▀▄ ██▀▀▄", "▄█▀▀▀ ▄█▀█▄ ██▀▀▄ ██▀▀▀"},
	{"█▀▄ █ █▀ ▀█ ██▄▄▀ ██  █", "██    ██ ██ ██  █ ██▄▄ "},
	{"█ ▀▄█ █████ ██▀▀▄ ██  █", "██    ██ ██ ██  █ ██▀▀ "},
	{"█  ▀█ █   █ ██▄▄▀ ██▄▄▀", "▀█▄▄▄ ▀█▄█▀ ██▄▄▀ ██▄▄▄"},
}

// welcomeLogoASCII renders pure ASCII "NABDCODE" in 5 rows of 48 cells (<= 56).
var welcomeLogoASCII = [][2]string{
	{"#   #   #   ####  #### ", " ####  ###  ####  #####"},
	{"##  #  # #  #   # #   #", "#     #   # #   # #    "},
	{"# # # ##### ####  #   #", "#     #   # #   # #### "},
	{"#  ## #   # #   # #   #", "#     #   # #   # #    "},
	{"#   # #   # ####  #### ", " ####  ###  ####  #####"},
}

// shouldShowWelcome reports whether the viewport should display the quiet
// welcome screen instead of the conversation stream. It strictly requires no
// pending approval/disclosure and no visible non-system messages.
func shouldShowWelcome(m *uiModel) bool {
	if m == nil || m.awaitingApproval || m.pendingDisclosure != nil {
		return false
	}
	for _, msg := range m.messages {
		if msg.Role != "system" {
			return false
		}
	}
	return true
}

func welcomeColorDisabled() bool {
	if os.Getenv("TERM") == "dumb" {
		return true
	}
	if v, ok := os.LookupEnv("NO_COLOR"); ok && v != "" {
		return true
	}
	if clicolor, ok := os.LookupEnv("CLICOLOR"); ok && clicolor == "0" {
		if force := os.Getenv("CLICOLOR_FORCE"); force == "" || force == "0" {
			return true
		}
	}
	return false
}

func renderWelcomeSpan(style lipgloss.Style, text string) string {
	if text == "" || welcomeColorDisabled() {
		return text
	}
	return style.Render(text)
}

func sanitizeWelcomeField(s string) string {
	clean := sanitizeForDisplay(s)
	clean = strings.ReplaceAll(clean, "\r\n", " ")
	clean = strings.ReplaceAll(clean, "\n", " ")
	clean = strings.ReplaceAll(clean, "\r", " ")
	clean = strings.ReplaceAll(clean, "\t", " ")
	return strings.TrimSpace(clean)
}

func truncateWelcomeEnd(s string, maxLen int) string {
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

func truncateWelcomeWorkspace(wsSafe, wsBase string, maxLen int) string {
	if maxLen <= 0 || wsSafe == "" {
		return ""
	}
	if lipgloss.Width(wsSafe) <= maxLen {
		return wsSafe
	}
	ell := activeEllipsisGlyph()
	if wsBase == "" || wsBase == "." || wsBase == "/" || wsBase == `\` {
		return truncateWelcomeEnd(wsSafe, maxLen)
	}
	candidate := ell + "/" + wsBase
	if lipgloss.Width(candidate) <= maxLen {
		return candidate
	}
	if lipgloss.Width(wsBase) <= maxLen {
		return wsBase
	}
	return truncateWelcomeEnd(wsBase, maxLen)
}

func welcomeSandboxLabel(m *uiModel, short bool) string {
	proven := m != nil && m.sandboxProven
	if short {
		if proven {
			return "SB: Proven"
		}
		return "SB: Off"
	}
	if proven {
		return "Sandbox: Proven"
	}
	return "Sandbox: Off"
}

func renderWelcomeSandbox(m *uiModel, label string) string {
	if m != nil && m.sandboxProven {
		return renderWelcomeSpan(welcomeSandboxOnStyle, label)
	}
	return renderWelcomeSpan(welcomeSandboxOffStyle, label)
}

func welcomePulseLine(wide bool, ascii bool, maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if wide {
		var left, peak, midLeft, dot, midRight, right string
		if ascii {
			left = "------------/\\--"
			peak = "/|\\"
			midLeft = "--"
			dot = "*"
			midRight = "--"
			right = "/\\------------"
		} else {
			left = "────────────╱╲──"
			peak = "╱│╲"
			midLeft = "──"
			dot = "▪"
			midRight = "──"
			right = "╱╲────────────"
		}
		plain := left + peak + midLeft + dot + midRight + right
		if lipgloss.Width(plain) <= maxW {
			return renderWelcomeSpan(welcomePulseLineStyle, left) +
				renderWelcomeSpan(welcomePulsePeakStyle, peak) +
				renderWelcomeSpan(welcomePulseLineStyle, midLeft) +
				renderWelcomeSpan(welcomePulsePeakStyle, dot) +
				renderWelcomeSpan(welcomePulseLineStyle, midRight) +
				renderWelcomeSpan(welcomePulseLineStyle, right)
		}
	}

	if ascii {
		raw := "---/\\---/\\---"
		if lipgloss.Width(raw) > maxW {
			raw = truncateWelcomeEnd(raw, maxW)
			return renderWelcomeSpan(welcomePulseLineStyle, raw)
		}
		return renderWelcomeSpan(welcomePulseLineStyle, "---") +
			renderWelcomeSpan(welcomePulsePeakStyle, "/\\") +
			renderWelcomeSpan(welcomePulseLineStyle, "---") +
			renderWelcomeSpan(welcomePulsePeakStyle, "/\\") +
			renderWelcomeSpan(welcomePulseLineStyle, "---")
	}

	raw := "──╱╲──╱╲──"
	if lipgloss.Width(raw) > maxW {
		raw = truncateWelcomeEnd(raw, maxW)
		return renderWelcomeSpan(welcomePulseLineStyle, raw)
	}
	return renderWelcomeSpan(welcomePulseLineStyle, "──") +
		renderWelcomeSpan(welcomePulsePeakStyle, "╱╲") +
		renderWelcomeSpan(welcomePulseLineStyle, "──") +
		renderWelcomeSpan(welcomePulsePeakStyle, "╱╲") +
		renderWelcomeSpan(welcomePulseLineStyle, "──")
}

func welcomeCompactTitle(maxW int) string {
	if maxW <= 0 {
		return ""
	}
	if maxW >= 16 {
		return renderWelcomeSpan(welcomeNabdStyle, "N A B D") + "  " + renderWelcomeSpan(welcomeCodeStyle, "C O D E")
	}
	if maxW >= 8 {
		return renderWelcomeSpan(welcomeNabdStyle, "NABD") + renderWelcomeSpan(welcomeCodeStyle, "CODE")
	}
	return renderWelcomeSpan(welcomeNabdStyle, truncateWelcomeEnd("NABDCODE", maxW))
}

func welcomeFields(m *uiModel) (modelSafe, wsSafe, wsBase string) {
	rawWS := ""
	if m != nil {
		modelSafe = sanitizeWelcomeField(m.modelName)
		rawWS = m.workspace
		wsSafe = sanitizeWelcomeField(rawWS)
	}
	if modelSafe == "" || modelSafe == "unknown" {
		modelSafe = "default-model"
	}
	if wsSafe == "" {
		return modelSafe, ".", "."
	}
	flat := strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(rawWS)
	wsBase = sanitizeWelcomeField(filepath.Base(flat))
	return modelSafe, wsSafe, wsBase
}

func welcomeWideMetaLine(m *uiModel, maxW int, sep string) string {
	modelSafe, wsSafe, wsBase := welcomeFields(m)
	sbText := welcomeSandboxLabel(m, false)
	if lipgloss.Width(sbText) > maxW {
		sbText = welcomeSandboxLabel(m, true)
	}
	if lipgloss.Width(sbText) > maxW {
		return renderWelcomeSandbox(m, truncateWelcomeEnd(sbText, maxW))
	}

	sepW := lipgloss.Width(sep)
	availLeft := maxW - lipgloss.Width(sbText) - sepW
	if availLeft <= 0 {
		return renderWelcomeSandbox(m, sbText)
	}

	fullLeft := modelSafe + sep + wsSafe
	if lipgloss.Width(fullLeft) <= availLeft {
		return renderWelcomeSpan(welcomeMetaStyle, fullLeft+sep) + renderWelcomeSandbox(m, sbText)
	}

	// Shrink workspace first (preferring .../last-dir) before truncating model.
	wsBudget := availLeft - lipgloss.Width(modelSafe) - sepW
	if wsBudget >= 4 {
		wsClipped := truncateWelcomeWorkspace(wsSafe, wsBase, wsBudget)
		if wsClipped != "" {
			return renderWelcomeSpan(welcomeMetaStyle, modelSafe+sep+wsClipped+sep) + renderWelcomeSandbox(m, sbText)
		}
	}

	minModel := min(12, lipgloss.Width(modelSafe))
	wsFallbackBudget := availLeft - minModel - sepW
	if wsFallbackBudget >= 4 {
		wsClipped := truncateWelcomeWorkspace(wsSafe, wsBase, wsFallbackBudget)
		modelBudget := availLeft - lipgloss.Width(wsClipped) - sepW
		modelClipped := truncateWelcomeEnd(modelSafe, modelBudget)
		if modelClipped != "" && wsClipped != "" {
			return renderWelcomeSpan(welcomeMetaStyle, modelClipped+sep+wsClipped+sep) + renderWelcomeSandbox(m, sbText)
		}
	}

	modelOnly := truncateWelcomeEnd(modelSafe, availLeft)
	if modelOnly != "" {
		return renderWelcomeSpan(welcomeMetaStyle, modelOnly+sep) + renderWelcomeSandbox(m, sbText)
	}
	return renderWelcomeSandbox(m, sbText)
}

func welcomeCompactUserLine(m *uiModel, maxW int, sep string) string {
	if maxW <= 0 {
		return ""
	}
	modelSafe, wsSafe, wsBase := welcomeFields(m)
	full := modelSafe + sep + wsSafe
	if lipgloss.Width(full) <= maxW {
		return renderWelcomeSpan(welcomeMetaStyle, full)
	}
	sepW := lipgloss.Width(sep)
	if maxW >= 18 {
		modelBudget := max(6, (maxW-sepW)/2)
		wsBudget := maxW - modelBudget - sepW
		wsClipped := truncateWelcomeWorkspace(wsSafe, wsBase, wsBudget)
		modelClipped := truncateWelcomeEnd(modelSafe, maxW-lipgloss.Width(wsClipped)-sepW)
		if modelClipped != "" && wsClipped != "" {
			return renderWelcomeSpan(welcomeMetaStyle, modelClipped+sep+wsClipped)
		}
	}
	return renderWelcomeSpan(welcomeMetaStyle, truncateWelcomeEnd(modelSafe, maxW))
}

func centerWelcomeLines(lines []string, width, height int) string {
	if height <= 0 || width <= 0 || len(lines) == 0 {
		return ""
	}
	if len(lines) > height {
		lines = lines[:height]
	}
	topPad := max(0, (height-len(lines))/2)
	var b strings.Builder
	for i := 0; i < topPad; i++ {
		b.WriteByte('\n')
	}
	for i, line := range lines {
		lw := lipgloss.Width(line)
		leftPad := max(0, (width-lw)/2)
		b.WriteString(strings.Repeat(" ", leftPad))
		b.WriteString(line)
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// renderWelcomeScreen renders the responsive Nabd ECG welcome view constrained
// cleanly to width x height viewport cells without modifying session state.
func renderWelcomeScreen(m *uiModel, width, height int) string {
	if height <= 0 {
		return ""
	}
	if width <= 0 {
		width = 1
	}

	ascii := isASCIIMode()
	sep := " · "
	wideSep := "  ·  "
	if ascii {
		sep = " | "
		wideSep = "  |  "
	}

	// Wide mode (>= 60 cols and sufficient vertical height)
	if width >= 60 {
		logoRows := welcomeLogoUnicode
		if ascii {
			logoRows = welcomeLogoASCII
		}
		needMinHeight := len(logoRows) + 3 // logo + pulse + meta + hint
		if height >= needMinHeight {
			metaCap := min(width, 56)
			lines := make([]string, 0, len(logoRows)+5)
			for _, row := range logoRows {
				styled := renderWelcomeSpan(welcomeNabdStyle, row[0]) + "  " + renderWelcomeSpan(welcomeCodeStyle, row[1])
				lines = append(lines, styled)
			}
			withSpacers := height >= len(logoRows)+5
			if withSpacers {
				lines = append(lines, "")
			}
			lines = append(lines, welcomePulseLine(true, ascii, metaCap))
			if withSpacers {
				lines = append(lines, "")
			}
			lines = append(lines, welcomeWideMetaLine(m, metaCap, wideSep))
			var wideHint string
			if ascii {
				wideHint = "Type / for commands | /status | /auto | /help"
			} else {
				wideHint = "Type / for commands · /status · /auto · /help"
			}
			lines = append(lines, renderWelcomeSpan(welcomeHintStyle, truncateWelcomeEnd(wideHint, metaCap)))
			return centerWelcomeLines(lines, width, height)
		}
	}

	// Compact / narrow mode (< 60 cols or height < 8)
	maxLineW := width
	if width >= 36 && width < 60 {
		maxLineW = min(width, 32)
	} else if width >= 60 {
		maxLineW = min(width, 56)
	}

	sbLabel := welcomeSandboxLabel(m, false)
	if lipgloss.Width(sbLabel) > maxLineW {
		sbLabel = welcomeSandboxLabel(m, true)
	}
	if lipgloss.Width(sbLabel) > maxLineW {
		sbLabel = truncateWelcomeEnd(sbLabel, maxLineW)
	}

	if height == 1 {
		// Ultra-tight height: drop decorative logo/pulse and keep real security state.
		brand := "NabdCoder"
		full := brand + sep + sbLabel
		if lipgloss.Width(full) <= width {
			line := renderWelcomeSpan(welcomeNabdStyle, brand) + renderWelcomeSpan(welcomeMetaStyle, sep) + renderWelcomeSandbox(m, sbLabel)
			return centerWelcomeLines([]string{line}, width, height)
		}
		sbLine := renderWelcomeSandbox(m, truncateWelcomeEnd(welcomeSandboxLabel(m, lipgloss.Width(welcomeSandboxLabel(m, false)) > width), width))
		return centerWelcomeLines([]string{sbLine}, width, height)
	}

	titleLine := welcomeCompactTitle(maxLineW)
	sbRendered := renderWelcomeSandbox(m, sbLabel)

	if height == 2 {
		return centerWelcomeLines([]string{titleLine, sbRendered}, width, height)
	}

	userLine := welcomeCompactUserLine(m, maxLineW, sep)
	if height == 3 {
		// Hide pulse & hints BEFORE logo.
		return centerWelcomeLines([]string{titleLine, userLine, sbRendered}, width, height)
	}

	pulseLine := welcomePulseLine(false, ascii, maxLineW)
	if height == 4 {
		// Hide hint BEFORE pulse & logo.
		return centerWelcomeLines([]string{titleLine, pulseLine, userLine, sbRendered}, width, height)
	}

	hintText := "Type / · /status · /auto · /help"
	if ascii {
		hintText = "Type / | /status | /auto | /help"
	}
	hintLine := renderWelcomeSpan(welcomeHintStyle, truncateWelcomeEnd(hintText, maxLineW))

	if height >= 7 {
		lines := []string{titleLine, pulseLine, "", userLine, sbRendered, hintLine}
		return centerWelcomeLines(lines, width, height)
	}

	// height == 5 or 6
	lines := []string{titleLine, pulseLine, userLine, sbRendered, hintLine}
	return centerWelcomeLines(lines, width, height)
}

// welcomePlainWidth returns the visible cell width even for valid UTF-8 strings.
func welcomeVisualCheck(s string) bool {
	return utf8.ValidString(s)
}
