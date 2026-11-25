package main

import (
	"os"

	"github.com/charmbracelet/lipgloss"
)

// Minimal, quiet design tokens (Claude Code / Codex CLI style).
// TrueColor with ANSI256 and ANSI16 fallbacks for Termux and standard terminal emulators.
var (
	// Base text - conversation text and titles
	colorFg = lipgloss.CompleteColor{TrueColor: "#E6EDF3", ANSI256: "252", ANSI: "15"}
	// Secondary - metadata, hints, and footer
	colorMuted = lipgloss.CompleteColor{TrueColor: "#7D8590", ANSI256: "244", ANSI: "8"}
	// Subtle - horizontal dividers and input frame
	colorSubtle = lipgloss.CompleteColor{TrueColor: "#30363D", ANSI256: "238", ANSI: "8"}
	// Accent - user prompt ❯ and identity ●
	colorAccent = lipgloss.CompleteColor{TrueColor: "#E3A857", ANSI256: "215", ANSI: "3"}
	// Assistant - agent response ● and diff +
	colorAssistant = lipgloss.CompleteColor{TrueColor: "#4ADE80", ANSI256: "78", ANSI: "2"}
	// Danger - errors, warnings, and diff -
	colorDanger = lipgloss.CompleteColor{TrueColor: "#F87171", ANSI256: "203", ANSI: "1"}
)

// Glyphs with ASCII fallbacks.
const (
	glyphIdentity      = "●"
	glyphIdentityASCII = "*"
	glyphPrompt        = "❯"
	glyphPromptASCII   = ">"
	glyphBar           = "│"
	glyphBarASCII      = "|"
	glyphDivider       = "─"
	glyphDividerASCII  = "-"
)

var (
	symIdentity = glyphIdentity
	symPrompt   = glyphPrompt
	symBar      = glyphBar
	symDivider  = glyphDivider
)

func init() {
	if os.Getenv("TERM") == "dumb" || os.Getenv("NABD_ASCII") == "1" {
		symIdentity = glyphIdentityASCII
		symPrompt = glyphPromptASCII
		symBar = glyphBarASCII
		symDivider = glyphDividerASCII
	}
}

// Lipgloss styles for quiet minimal agent UI.
var (
	// Header styles (quiet hairline header)
	headerIdentityStyle = lipgloss.NewStyle().Foreground(colorAccent)
	headerTitleStyle    = lipgloss.NewStyle().Foreground(colorFg).Bold(true)
	headerMutedStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	headerDividerStyle  = lipgloss.NewStyle().Foreground(colorSubtle)

	// Stream styles (borderless, quiet)
	userPrefixStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	userTextStyle   = lipgloss.NewStyle().Foreground(colorFg)
	asstPrefixStyle = lipgloss.NewStyle().Foreground(colorAssistant).Bold(true)
	toolBarStyle    = lipgloss.NewStyle().Foreground(colorMuted)
	toolNameStyle   = lipgloss.NewStyle().Foreground(colorFg).Bold(true)
	toolHintStyle   = lipgloss.NewStyle().Foreground(colorMuted)
	errorBarStyle   = lipgloss.NewStyle().Foreground(colorDanger)
	errorLabelStyle = lipgloss.NewStyle().Foreground(colorDanger).Bold(true)
	errorTextStyle  = lipgloss.NewStyle().Foreground(colorFg)
	localBarStyle   = lipgloss.NewStyle().Foreground(colorSubtle)
	localTextStyle  = lipgloss.NewStyle().Foreground(colorMuted)

	// Markdown-lite styling (no filled backgrounds)
	mdHeading1Style   = lipgloss.NewStyle().Bold(true).Foreground(colorFg).MarginTop(1)
	mdHeading2Style   = lipgloss.NewStyle().Bold(true).Foreground(colorFg).MarginTop(1)
	mdHeading3Style   = lipgloss.NewStyle().Bold(true).Foreground(colorMuted).MarginTop(1)
	mdBulletStyle     = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	mdInlineCodeStyle = lipgloss.NewStyle().Foreground(colorAccent)
	mdCodeBlockStyle  = lipgloss.NewStyle().Foreground(colorFg).Padding(0, 1)
	mdCodeLangStyle   = lipgloss.NewStyle().Foreground(colorMuted).Italic(true)

	// Diff display styles (quiet foregrounds, no filled colored backgrounds)
	diffAddStyle    = lipgloss.NewStyle().Foreground(colorAssistant)
	diffDelStyle    = lipgloss.NewStyle().Foreground(colorDanger)
	diffHeaderStyle = lipgloss.NewStyle().Foreground(colorMuted).Bold(true)
	diffInfoStyle   = lipgloss.NewStyle().Foreground(colorMuted)

	// Advisory credential warning alert box (only boxed item in stream)
	advisoryAlertBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorDanger).
				Foreground(colorDanger).
				Padding(0, 1).
				MarginBottom(1).
				Bold(true)

	// Composer frame style
	composerBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(colorSubtle)

	// Footer styles
	footerStatusStyle      = lipgloss.NewStyle().Foreground(colorMuted)
	footerKeyHintStyle     = lipgloss.NewStyle().Foreground(colorMuted)
	scrollPercentStyle     = lipgloss.NewStyle().Foreground(colorMuted)
	scrollBottomReadyStyle = lipgloss.NewStyle().Foreground(colorAssistant)

	// Approval prompt style
	approvalPromptStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)

	// Slash command palette styles
	slashBoxStyle            = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colorSubtle)
	slashSelectedPrefixStyle = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	slashSelectedNameStyle   = lipgloss.NewStyle().Foreground(colorAccent).Bold(true)
	slashSelectedDescStyle   = lipgloss.NewStyle().Foreground(colorFg)
	slashNormalNameStyle     = lipgloss.NewStyle().Foreground(colorFg)
	slashNormalDescStyle     = lipgloss.NewStyle().Foreground(colorMuted)
	slashHintStyle           = lipgloss.NewStyle().Foreground(colorMuted)
)
