package main

import (
	"github.com/charmbracelet/lipgloss"
)

// UI Color Palette (ANSI 256-color safe for Termux and standard terminal emulators).
var (
	// Brand / Header colors
	colorBrandPrimary = lipgloss.Color("39")  // Deep Sky Blue / Cyan
	colorDarkBg       = lipgloss.Color("236") // Dark Gray for bars
	colorMuted        = lipgloss.Color("244") // Medium Gray
	colorLightMuted   = lipgloss.Color("250") // Light Gray text

	// Status & Pill colors
	colorSuccess = lipgloss.Color("42")  // Spring Green
	colorWarning = lipgloss.Color("214") // Orange / Amber
	colorInfo    = lipgloss.Color("75")  // Light Steel Blue

	// Message Role colors
	colorUserBg      = lipgloss.Color("235")
	colorUserBorder  = lipgloss.Color("33") // Dodger Blue
	colorAsstBg      = lipgloss.Color("234")
	colorAsstBorder  = lipgloss.Color("35") // Emerald Green
	colorToolBg      = lipgloss.Color("235")
	colorToolBorder  = lipgloss.Color("208") // Dark Orange
	colorErrorBg     = lipgloss.Color("52")  // Dark Maroon
	colorErrorBorder = lipgloss.Color("160") // Bright Crimson
	colorLocalBg     = lipgloss.Color("236")
	colorLocalBorder = lipgloss.Color("242")

	// Diff colors
	colorDiffAddBg  = lipgloss.Color("22")  // Dark Green
	colorDiffAddFg  = lipgloss.Color("84")  // Mint Green
	colorDiffDelBg  = lipgloss.Color("52")  // Dark Red
	colorDiffDelFg  = lipgloss.Color("203") // Coral Red
	colorDiffHdrFg  = lipgloss.Color("117") // Light Cyan
	colorDiffInfoFg = lipgloss.Color("245") // Subtle Gray
)

// Lipgloss styles for header, cards, diffs, and footer.
var (
	// Header bar styles
	headerBarStyle = lipgloss.NewStyle().
			Padding(0, 1).
			Background(colorDarkBg)

	headerLogoStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("15")).
			Background(colorBrandPrimary).
			Padding(0, 1)

	headerPillStyle = lipgloss.NewStyle().
			Padding(0, 1).
			Foreground(lipgloss.Color("255")).
			Background(lipgloss.Color("239")).
			Bold(true)

	headerPillSuccessStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Foreground(lipgloss.Color("16")).
				Background(colorSuccess).
				Bold(true)

	headerPillWarnStyle = lipgloss.NewStyle().
				Padding(0, 1).
				Foreground(lipgloss.Color("16")).
				Background(colorWarning).
				Bold(true)

	headerMetricStyle = lipgloss.NewStyle().
				Foreground(colorLightMuted).
				Background(lipgloss.Color("238")).
				Padding(0, 1)

	// Card styles
	cardBaseStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1).
			MarginBottom(1)

	userCardStyle = cardBaseStyle.
			BorderForeground(colorUserBorder).
			Background(colorUserBg)

	asstCardStyle = cardBaseStyle.
			BorderForeground(colorAsstBorder).
			Background(colorAsstBg)

	toolCardStyle = cardBaseStyle.
			BorderForeground(colorToolBorder).
			Background(colorToolBg)

	errorCardStyle = cardBaseStyle.
			BorderForeground(colorErrorBorder).
			Background(colorErrorBg)

	localCardStyle = cardBaseStyle.
			BorderForeground(colorLocalBorder).
			Background(colorLocalBg)

	// Markdown-lite styling
	mdHeading1Style = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorBrandPrimary).
			MarginTop(1).
			MarginBottom(0)

	mdHeading2Style = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorInfo).
			MarginTop(1).
			MarginBottom(0)

	mdHeading3Style = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorLightMuted).
			MarginTop(1).
			MarginBottom(0)

	mdBulletStyle = lipgloss.NewStyle().
			Foreground(colorBrandPrimary).
			Bold(true)

	mdInlineCodeStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("222")).
				Background(lipgloss.Color("237")).
				Padding(0, 1)

	mdCodeBlockStyle = lipgloss.NewStyle().
				Border(lipgloss.RoundedBorder()).
				BorderForeground(lipgloss.Color("240")).
				Background(lipgloss.Color("235")).
				Padding(0, 1).
				Margin(0, 0, 1, 0)

	mdCodeLangStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	// Diff display styles
	diffAddStyle = lipgloss.NewStyle().
			Foreground(colorDiffAddFg).
			Background(colorDiffAddBg)

	diffDelStyle = lipgloss.NewStyle().
			Foreground(colorDiffDelFg).
			Background(colorDiffDelBg)

	diffHeaderStyle = lipgloss.NewStyle().
			Foreground(colorDiffHdrFg).
			Bold(true)

	diffInfoStyle = lipgloss.NewStyle().
			Foreground(colorDiffInfoFg)

	// Advisory credential warning alert box
	advisoryAlertBoxStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(colorWarning).
				Background(lipgloss.Color("234")).
				Foreground(lipgloss.Color("220")).
				Padding(0, 1).
				MarginBottom(1).
				Bold(true)

	// Footer styles
	footerBarStyle = lipgloss.NewStyle().
			Background(colorDarkBg).
			Padding(0, 1)

	footerStatusStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Bold(true)

	footerKeyHintStyle = lipgloss.NewStyle().
				Foreground(colorMuted)

	footerKeyPillStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color("255")).
				Background(lipgloss.Color("240")).
				Padding(0, 1).
				Bold(true)

	scrollPercentStyle = lipgloss.NewStyle().
				Foreground(colorInfo).
				Bold(true)

	scrollBottomReadyStyle = lipgloss.NewStyle().
				Foreground(colorSuccess).
				Bold(true)
)
