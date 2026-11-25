package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/term"
)

const (
	maxClipboardSize = 256 * 1024          // 256 KiB maximum raw text limit
	maxOSC52Payload  = 75 * 1024           // 75 KiB base64 maximum payload limit
	maxOSC52RawBytes = (75 * 1024 * 3) / 4 // 57,600 bytes maximum raw text before base64 exceeds 75 KiB
	clipboardTimeout = 3 * time.Second     // 3 seconds total timeout across all fallback attempts
)

type copyTarget int

const (
	targetReply copyTarget = iota
	targetCode
	targetTool
)

var (
	errNothingToCopy = errors.New("nothing to copy yet")
	errNoCodeBlock   = errors.New("no code block found in last reply")
	errNoToolOutput  = errors.New("no tool output to copy")
	errInvalidUsage  = errors.New("usage: /copy, /copy code, or /copy tool")
	errLimitExceeded = errors.New("text exceeds clipboard limit (256 KiB)")
	errOSC52Limit    = errors.New("clipboard request via OSC 52 exceeds terminal size limit (75 KiB base64)")
)

// clipboardResult holds metadata about a successful clipboard operation.
// It explicitly never holds the copied text content itself.
type clipboardResult struct {
	Method      string // e.g. "wl-copy", "clip.exe", "termux-clipboard-set", "pbcopy", "OSC 52"
	Bytes       int    // size of original text copied
	Unconfirmed bool   // true for OSC 52 where terminal delivery cannot be confirmed
	RemoteSSH   bool   // true if local tool executed inside an SSH session
}

// findCopyContent selects the raw content to copy according to the target specification.
func findCopyContent(messages []Message, target copyTarget) (string, error) {
	switch target {
	case targetReply:
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			if msg.Role == "assistant" && strings.TrimSpace(msg.Content) != "" {
				return msg.Content, nil
			}
		}
		return "", errNothingToCopy

	case targetCode:
		var asstContent string
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			if msg.Role == "assistant" && strings.TrimSpace(msg.Content) != "" {
				asstContent = msg.Content
				break
			}
		}
		if asstContent == "" {
			return "", errNothingToCopy
		}
		code, found := extractLastCodeBlock(asstContent)
		if !found {
			return "", errNoCodeBlock
		}
		return code, nil

	case targetTool:
		for i := len(messages) - 1; i >= 0; i-- {
			msg := messages[i]
			if msg.Role == "tool" && isEligibleToolOutput(msg) {
				return msg.Content, nil
			}
		}
		return "", errNoToolOutput

	default:
		return "", errInvalidUsage
	}
}

// isEligibleToolOutput determines if a tool message is authorized, disclosed, and not an error or rejection.
func isEligibleToolOutput(msg Message) bool {
	if strings.TrimSpace(msg.Content) == "" {
		return false
	}
	// Check against blocked disclosures or rejected actions
	blockedPrefixes := []string{
		"Tool output disclosure rejected",
		"Tool disclosure blocked",
		"Rejected by user",
		"Rejected:",
		"Error: file changed since approval preview",
		"Error: iteration limit reached",
		"Error: repeated identical tool call blocked",
		"Error: approval could not be recorded",
	}
	for _, p := range blockedPrefixes {
		if strings.HasPrefix(msg.Content, p) {
			return false
		}
	}
	if strings.HasPrefix(msg.Display, "Rejected") || strings.HasPrefix(msg.Display, "Blocked") || strings.HasPrefix(msg.Display, "Skipped:") {
		return false
	}
	return true
}

// extractLastCodeBlock extracts the last complete fenced code block from content.
// Fences must be at least 3 backticks (`). Language tag is ignored.
// Inner backticks of shorter length than the fence are preserved as code.
// Returns the raw code bytes and true if found, or empty string and false if not found.
func extractLastCodeBlock(content string) (string, bool) {
	lines := strings.Split(content, "\n")
	var lastBlock string
	found := false

	inBlock := false
	fenceLen := 0
	var blockLines []string

	for _, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		trimmedLeading := strings.TrimLeft(trimmed, " \t")

		if !inBlock {
			// Check for opening fence: at least 3 backticks
			if strings.HasPrefix(trimmedLeading, "```") {
				btCount := 0
				for btCount < len(trimmedLeading) && trimmedLeading[btCount] == '`' {
					btCount++
				}
				// Remaining info string must not contain backticks
				info := trimmedLeading[btCount:]
				if !strings.Contains(info, "`") {
					inBlock = true
					fenceLen = btCount
					blockLines = nil
					continue
				}
			}
		} else {
			// Inside block: check for closing fence
			// Must have at least fenceLen backticks and no other content except optional spaces
			if strings.HasPrefix(trimmedLeading, strings.Repeat("`", fenceLen)) {
				btCount := 0
				for btCount < len(trimmedLeading) && trimmedLeading[btCount] == '`' {
					btCount++
				}
				rest := strings.TrimSpace(trimmedLeading[btCount:])
				if rest == "" {
					// Complete closing fence!
					lastBlock = strings.Join(blockLines, "\n")
					// If the source had CRLF, normalize lines accordingly if needed, or keep lines as joined
					found = true
					inBlock = false
					fenceLen = 0
					continue
				}
			}
			blockLines = append(blockLines, trimmed)
		}
	}

	return lastBlock, found
}

// safeLookPath verifies that executable path resolved from PATH is safe to execute.
func safeLookPath(file string) (string, error) {
	path, err := exec.LookPath(file)
	if err != nil {
		return "", err
	}
	if errors.Is(err, exec.ErrDot) {
		return "", fmt.Errorf("insecure relative path for %s", file)
	}
	if filepath.IsLocal(path) && !filepath.IsAbs(path) && strings.ContainsRune(path, filepath.Separator) {
		return "", fmt.Errorf("insecure relative path for %s", file)
	}
	return path, nil
}

type cmdRunnerFunc func(ctx context.Context, prog string, args []string, text string) error

// defaultCmdRunner executes the external clipboard command, streaming text via stdin.
func defaultCmdRunner(ctx context.Context, prog string, args []string, text string) error {
	binPath, err := safeLookPath(prog)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Stdin = strings.NewReader(text)
	cmd.Stdout = nil
	cmd.Stderr = nil // discard stderr to avoid leaking sensitive contents in errors
	return cmd.Run()
}

// systemEnv encapsulates the detected runtime environment for clipboard routing.
type systemEnv struct {
	isSSH     bool
	isTermux  bool
	isWayland bool
	isX11     bool
	goos      string
	isTTY     bool
	inTmux    bool
	ttyWriter io.Writer
}

// detectSystemEnv inspects standard environment variables and OS info.
func detectSystemEnv() systemEnv {
	ssh := os.Getenv("SSH_CLIENT") != "" || os.Getenv("SSH_TTY") != "" || os.Getenv("SSH_CONNECTION") != ""
	termux := os.Getenv("TERMUX_VERSION") != "" || strings.Contains(os.Getenv("PREFIX"), "com.termux")
	wayland := os.Getenv("WAYLAND_DISPLAY") != ""
	x11 := os.Getenv("DISPLAY") != ""
	tmux := os.Getenv("TMUX") != "" || strings.HasPrefix(os.Getenv("TERM"), "tmux") || strings.HasPrefix(os.Getenv("TERM"), "screen")
	isTTY := term.IsTerminal(int(os.Stdout.Fd()))

	return systemEnv{
		isSSH:     ssh,
		isTermux:  termux,
		isWayland: wayland,
		isX11:     x11,
		goos:      runtime.GOOS,
		isTTY:     isTTY,
		inTmux:    tmux,
		ttyWriter: os.Stdout,
	}
}

// writeOSC52 formats and writes an OSC 52 sequence to the provided TTY writer.
func writeOSC52(w io.Writer, text string, inTmux bool) error {
	if w == nil {
		return errors.New("no terminal writer available")
	}
	b64 := base64.StdEncoding.EncodeToString([]byte(text))
	if len(b64) > maxOSC52Payload {
		return errors.New("payload exceeds OSC 52 limit (75 KiB)")
	}

	var seq string
	if inTmux {
		// tmux passthrough sequence
		seq = "\x1bPtmux;\x1b\x1b]52;c;" + b64 + "\x07\x1b\\"
	} else {
		seq = "\x1b]52;c;" + b64 + "\x07"
	}

	_, err := io.WriteString(w, seq)
	return err
}

// copyToClipboard performs multi-platform clipboard writing with fallback, bounded by context timeout.
func copyToClipboard(ctx context.Context, text string, env systemEnv, runCmd cmdRunnerFunc) (clipboardResult, error) {
	if len(text) > maxClipboardSize {
		return clipboardResult{}, errLimitExceeded
	}
	if runCmd == nil {
		runCmd = defaultCmdRunner
	}

	totalCtx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()

	// In an SSH session, prioritize OSC 52 if TTY is available, because local commands
	// would affect the remote server's clipboard rather than the user's client machine.
	if env.isSSH && env.isTTY && env.ttyWriter != nil {
		if len(text) <= maxOSC52RawBytes {
			if err := writeOSC52(env.ttyWriter, text, env.inTmux); err == nil {
				return clipboardResult{
					Method:      "OSC 52",
					Bytes:       len(text),
					Unconfirmed: true,
				}, nil
			}
		}
	}

	// Helper for local tool invocation
	tryLocal := func(prog string, args ...string) (clipboardResult, bool) {
		if totalCtx.Err() != nil {
			return clipboardResult{}, false
		}
		if err := runCmd(totalCtx, prog, args, text); err == nil {
			return clipboardResult{
				Method:      prog,
				Bytes:       len(text),
				Unconfirmed: false,
				RemoteSSH:   env.isSSH,
			}, true
		}
		return clipboardResult{}, false
	}

	// 1. Android / Termux
	if env.isTermux || env.goos == "android" {
		if res, ok := tryLocal("termux-clipboard-set"); ok {
			return res, nil
		}
	}

	// 2. Linux
	if env.goos == "linux" {
		if env.isWayland {
			if res, ok := tryLocal("wl-copy"); ok {
				return res, nil
			}
		}
		if env.isX11 || !env.isWayland {
			if res, ok := tryLocal("xclip", "-selection", "clipboard"); ok {
				return res, nil
			}
			if res, ok := tryLocal("xsel", "--clipboard", "--input"); ok {
				return res, nil
			}
		}
	}

	// 3. macOS
	if env.goos == "darwin" {
		if res, ok := tryLocal("pbcopy"); ok {
			return res, nil
		}
	}

	// 4. Windows
	if env.goos == "windows" {
		if res, ok := tryLocal("clip.exe"); ok {
			return res, nil
		}
		if res, ok := tryLocal("clip"); ok {
			return res, nil
		}
	}

	// 5. OSC 52 Fallback if not already attempted or if local tools failed
	if env.isTTY && env.ttyWriter != nil {
		if len(text) > maxOSC52RawBytes {
			return clipboardResult{}, errOSC52Limit
		}
		if err := writeOSC52(env.ttyWriter, text, env.inTmux); err == nil {
			return clipboardResult{
				Method:      "OSC 52",
				Bytes:       len(text),
				Unconfirmed: true,
			}, nil
		}
	}

	if totalCtx.Err() != nil {
		return clipboardResult{}, totalCtx.Err()
	}

	return clipboardResult{}, errors.New("no available clipboard tool found on this system")
}
