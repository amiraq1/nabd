package main

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// slashCommand represents a registered slash command.
type slashCommand struct {
	Name        string // without "/", e.g. "status"
	Description string
	IsProject   bool
	Source      string
}

// slashCommands is the single source of truth for both the interactive palette and /help.
var slashCommands = []slashCommand{
	{
		Name:        "status",
		Description: "Show model, endpoint, sandbox, context usage, and REA status",
	},
	{
		Name:        "tools",
		Description: "List registered tools and approval requirements",
	},
	{
		Name:        "auto",
		Description: "Auto-approval mode; 'all' needs an explicit y (/auto off|readonly|all)",
	},
	{
		Name:        "clear",
		Description: "Clear conversation history (keeps the active system prompt)",
	},
	{
		Name:        "copy",
		Description: "Copy last reply (/copy code | /copy tool)",
	},
	{
		Name:        "compact",
		Description: "Compact old tool outputs to free memory",
	},
	{
		Name:        "help",
		Description: "Show available commands and keyboard shortcuts",
	},
}

// slashPalette tracks the interactive state of the command palette.
type slashPalette struct {
	open            bool
	selectedIndex   int
	filtered        []slashCommand
	dismissedInput  string // input text when user explicitly pressed Esc
	projectCommands []slashCommand
}

func (p *slashPalette) setProjectCommands(cmds []slashCommand) {
	p.projectCommands = append([]slashCommand(nil), cmds...)
}

// filterSlashCommands performs pure, case-insensitive filtering on built-in commands.
// Prefix matches appear first, followed by substring matches, preserving definition order (stable).
// It also preserves the previous selected command if it remains in the results.
func filterSlashCommands(query string, prevSelected string) ([]slashCommand, int) {
	return filterSlashCommandsWithExtras(query, prevSelected, nil)
}

func filterSlashCommandsWithExtras(query string, prevSelected string, extras []slashCommand) ([]slashCommand, int) {
	q := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(query)), "/")
	if q == "" {
		// Empty query after "/" returns built-ins first, then project commands
		res := make([]slashCommand, 0, len(slashCommands)+len(extras))
		res = append(res, slashCommands...)
		res = append(res, extras...)
		idx := 0
		if prevSelected != "" {
			for i, cmd := range res {
				if cmd.Name == prevSelected {
					idx = i
					break
				}
			}
		}
		return res, idx
	}

	var prefixMatches []slashCommand
	var substringMatches []slashCommand

	for _, cmd := range slashCommands {
		lowerName := strings.ToLower(cmd.Name)
		if strings.HasPrefix(lowerName, q) {
			prefixMatches = append(prefixMatches, cmd)
		} else if strings.Contains(lowerName, q) {
			substringMatches = append(substringMatches, cmd)
		}
	}
	for _, cmd := range extras {
		lowerName := strings.ToLower(cmd.Name)
		if strings.HasPrefix(lowerName, q) {
			prefixMatches = append(prefixMatches, cmd)
		} else if strings.Contains(lowerName, q) {
			substringMatches = append(substringMatches, cmd)
		}
	}

	result := append(prefixMatches, substringMatches...)
	if len(result) == 0 {
		return nil, 0
	}

	// Preserve selection if previously selected command still matches
	idx := 0
	if prevSelected != "" {
		for i, cmd := range result {
			if cmd.Name == prevSelected {
				idx = i
				break
			}
		}
	}

	return result, idx
}

// shouldOpenPalette evaluates whether the command palette should open for built-in commands.
func shouldOpenPalette(input string, busy bool, awaitingApproval bool, pendingDisclosure bool, dismissedInput string) bool {
	return shouldOpenPaletteWithExtras(input, busy, awaitingApproval, pendingDisclosure, dismissedInput, nil)
}

func shouldOpenPaletteWithExtras(input string, busy bool, awaitingApproval bool, pendingDisclosure bool, dismissedInput string, extras []slashCommand) bool {
	if busy || awaitingApproval || pendingDisclosure {
		return false
	}
	// Must be single line, start with "/", and contain no spaces
	if strings.Contains(input, "\n") || strings.Contains(input, " ") || !strings.HasPrefix(input, "/") {
		return false
	}
	// Must not have been explicitly dismissed for this exact input
	if dismissedInput != "" && input == dismissedInput {
		return false
	}
	// Must have at least one match
	filtered, _ := filterSlashCommandsWithExtras(input, "", extras)
	return len(filtered) > 0
}

// updatePalette updates the palette state given the current input value.
func (p *slashPalette) updatePalette(input string, busy bool, awaitingApproval bool, pendingDisclosure bool) {
	if input != p.dismissedInput {
		p.dismissedInput = ""
	}

	if !shouldOpenPaletteWithExtras(input, busy, awaitingApproval, pendingDisclosure, p.dismissedInput, p.projectCommands) {
		p.open = false
		p.filtered = nil
		p.selectedIndex = 0
		return
	}

	var prevName string
	if p.open && len(p.filtered) > 0 && p.selectedIndex >= 0 && p.selectedIndex < len(p.filtered) {
		prevName = p.filtered[p.selectedIndex].Name
	}

	filtered, selIdx := filterSlashCommandsWithExtras(input, prevName, p.projectCommands)
	if len(filtered) == 0 {
		p.open = false
		p.filtered = nil
		p.selectedIndex = 0
		return
	}

	p.open = true
	p.filtered = filtered
	p.selectedIndex = selIdx
}

// navigateUp moves the selection up with wrapping.
func (p *slashPalette) navigateUp() {
	if len(p.filtered) == 0 {
		return
	}
	p.selectedIndex = (p.selectedIndex - 1 + len(p.filtered)) % len(p.filtered)
}

// navigateDown moves the selection down with wrapping.
func (p *slashPalette) navigateDown() {
	if len(p.filtered) == 0 {
		return
	}
	p.selectedIndex = (p.selectedIndex + 1) % len(p.filtered)
}

// selectedCommand returns the currently selected command, or nil if none.
func (p *slashPalette) selectedCommand() *slashCommand {
	if !p.open || len(p.filtered) == 0 || p.selectedIndex < 0 || p.selectedIndex >= len(p.filtered) {
		return nil
	}
	return &p.filtered[p.selectedIndex]
}

// renderSlashPalette renders the command palette box.
// It displays up to maxRows visible items (scrolled if needed), formatted descriptions,
// and a bottom hint bar inside a rounded border matching the composer's width.
func renderSlashPalette(p *slashPalette, width int, maxRows int) string {
	if !p.open || len(p.filtered) == 0 {
		return ""
	}
	if width <= 0 {
		width = 80
	}
	if maxRows <= 0 {
		maxRows = 6
	}

	boxWidth := max(10, width-2) // subtract 2 for left and right border chars
	innerAvail := boxWidth - 2   // inner available width inside the frame

	totalItems := len(p.filtered)
	visibleCount := min(totalItems, maxRows)

	// Calculate scrolling window so selected item is always visible
	startIdx := 0
	if p.selectedIndex >= visibleCount {
		startIdx = p.selectedIndex - visibleCount + 1
	}
	endIdx := min(totalItems, startIdx+visibleCount)

	// Determine max command name length among visible items for description alignment
	maxNameLen := 0
	for i := startIdx; i < endIdx; i++ {
		cmdName := "/" + p.filtered[i].Name
		if l := lipgloss.Width(cmdName); l > maxNameLen {
			maxNameLen = l
		}
	}

	var lines []string

	for i := startIdx; i < endIdx; i++ {
		cmd := p.filtered[i]
		cmdName := "/" + cmd.Name
		isSelected := (i == p.selectedIndex)

		var prefix string
		var nameStyled string
		var descStyled string

		if isSelected {
			prefix = slashSelectedPrefixStyle.Render(symPrompt + " ")
			nameStyled = slashSelectedNameStyle.Render(cmdName)
		} else {
			prefix = "  "
			nameStyled = slashNormalNameStyle.Render(cmdName)
		}

		// Calculate spacing to align description
		nameVisualWidth := lipgloss.Width(cmdName)
		gap := max(2, maxNameLen-nameVisualWidth+2)
		gapStr := strings.Repeat(" ", gap)

		// Space left for description
		usedWidth := 2 + nameVisualWidth + gap
		availDesc := innerAvail - usedWidth

		if width >= 45 && availDesc > 5 {
			desc := cmd.Description
			if cmd.IsProject {
				desc = "[project] " + desc
			}
			desc = sanitizeForDisplay(desc)
			if lipgloss.Width(desc) > availDesc {
				desc = truncateEnd(desc, availDesc)
			}
			if isSelected {
				descStyled = slashSelectedDescStyle.Render(desc)
			} else {
				descStyled = slashNormalDescStyle.Render(desc)
			}
			lines = append(lines, prefix+nameStyled+gapStr+descStyled)
		} else if innerAvail >= 2+nameVisualWidth {
			// Width < 45 or not enough room for description: render command name alone
			lines = append(lines, prefix+nameStyled)
		} else {
			// Extremely narrow terminal: truncate command name if necessary
			lines = append(lines, prefix+truncateEnd(cmdName, max(3, innerAvail-2)))
		}
	}

	// Hint line at bottom inside the frame
	var hintText string
	isASCII := (symDivider == glyphDividerASCII)
	if width >= 55 {
		if isASCII {
			hintText = "Up/Down select · Tab complete · Enter run · Esc close"
		} else {
			hintText = "↑/↓ select · Tab complete · Enter run · Esc close"
		}
	} else {
		if isASCII {
			hintText = "Up/Down · Tab · Enter · Esc"
		} else {
			hintText = "↑↓ · Tab · Enter · Esc"
		}
	}

	hintAvail := innerAvail - 2
	if lipgloss.Width(hintText) > hintAvail {
		hintText = truncateEnd(hintText, hintAvail)
	}
	lines = append(lines, slashHintStyle.Render("  "+hintText))

	content := strings.Join(lines, "\n")
	return slashBoxStyle.Width(boxWidth).Render(content)
}
