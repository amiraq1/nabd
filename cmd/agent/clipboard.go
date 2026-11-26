package main

import (
	"bytes"
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
	"sync"
	"time"
	"unicode/utf16"

	"golang.org/x/term"
)

const (
	maxClipboardSize   = 256 * 1024             // 256 KiB maximum raw text limit
	maxOSC52Payload    = 75 * 1024              // 75 KiB base64 maximum payload limit
	maxOSC52RawBytes   = (75 * 1024 * 3) / 4    // 57,600 bytes maximum raw text before base64 exceeds 75 KiB
	clipboardTimeout   = 3 * time.Second        // 3 seconds total timeout across all fallback attempts
	clipboardWaitDelay = 500 * time.Millisecond // grace period to close inherited I/O pipes after exit/cancel
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

// syncTTYWriter serializes writes to an underlying terminal *os.File so OSC 52
// escape sequences cannot interleave with Bubble Tea's alt-screen renderer.
type syncTTYWriter struct {
	mu   sync.Mutex
	file *os.File
}

func newSyncTTYWriter(f *os.File) *syncTTYWriter {
	return &syncTTYWriter{file: f}
}

func (w *syncTTYWriter) Read(p []byte) (int, error) {
	return w.file.Read(p)
}

func (w *syncTTYWriter) Close() error {
	return w.file.Close()
}

func (w *syncTTYWriter) Fd() uintptr {
	return w.file.Fd()
}

func (w *syncTTYWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.Write(p)
}

func (w *syncTTYWriter) WriteString(s string) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.file.WriteString(s)
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
	if filepath.IsLocal(path) && !filepath.IsAbs(path) && strings.ContainsRune(path, filepath.Separator) {
		return "", fmt.Errorf("insecure relative path for %s", file)
	}
	return path, nil
}

// isForbiddenSecretEnvKey rejects secret-bearing key names even when matched by a prefix allowlist.
func isForbiddenSecretEnvKey(upperKey string) bool {
	if strings.HasPrefix(upperKey, "NABD_") {
		return true
	}
	for _, suffix := range []string{"_KEY", "_SECRET", "_TOKEN", "_PASSWORD", "_CREDENTIAL"} {
		if strings.HasSuffix(upperKey, suffix) {
			return true
		}
	}
	return false
}

// isAllowedClipboardEnvKey evaluates the strict per-platform environment allowlist.
func isAllowedClipboardEnvKey(env systemEnv, key string) bool {
	upper := strings.ToUpper(key)
	if isForbiddenSecretEnvKey(upper) {
		return false
	}

	// Termux / Android: termux-clipboard-set is a wrapper/IPC client that invokes Termux/Android helpers.
	if env.isTermux || env.goos == "android" {
		switch upper {
		case "PATH", // locate helper binaries such as termux-api / am
			"PREFIX",          // Termux sysroot directory
			"LD_LIBRARY_PATH", // Termux shared libraries for dynamic linker
			"TMPDIR",          // temporary socket/FIFO storage
			"HOME",            // Termux user home directory
			"LANG":            // UTF-8 locale encoding
			return true
		}
		if strings.HasPrefix(upper, "LC_") || // locale encoding overrides (e.g. LC_ALL, LC_CTYPE)
			strings.HasPrefix(upper, "ANDROID_") || // Android runtime/binder/storage paths
			strings.HasPrefix(upper, "TERMUX_") { // Termux runtime version and API socket context
			return true
		}
		return false
	}

	switch env.goos {
	case "linux":
		switch upper {
		case "PATH", // locate clipboard tools and helpers
			"HOME",            // fallback location for ~/.Xauthority
			"DISPLAY",         // X11 display server for xclip/xsel
			"WAYLAND_DISPLAY", // Wayland compositor socket name for wl-copy
			"XDG_RUNTIME_DIR", // runtime socket directory for Wayland/XDG
			"XAUTHORITY",      // X11 authentication cookie file
			"TMPDIR",          // temporary backing storage for large clipboard payloads
			"LANG":            // UTF-8 locale encoding
			return true
		}
		return strings.HasPrefix(upper, "LC_") // locale encoding overrides (LC_ALL, LC_CTYPE)

	case "windows":
		switch upper {
		case "PATH", // locate system DLLs and clip.exe helpers
			"SYSTEMROOT", // Windows system directory (%SystemRoot%\System32)
			"WINDIR",     // legacy Windows directory alias
			"COMSPEC",    // standard Windows command processor path expected by CRT
			"TEMP",       // Windows temporary directory
			"TMP":        // Windows temporary directory alias
			return true
		}
		return false

	case "darwin":
		switch upper {
		case "PATH", // locate clipboard helpers
			"HOME",   // user home context for pasteboard services
			"TMPDIR", // per-user macOS temporary directory
			"LANG":   // UTF-8 locale encoding for pbcopy
			return true
		}
		return strings.HasPrefix(upper, "LC_") // locale encoding overrides (LC_ALL, LC_CTYPE)

	default:
		switch upper {
		case "PATH", // locate tool helpers
			"HOME", // user directory context
			"LANG": // UTF-8 locale encoding
			return true
		}
		return strings.HasPrefix(upper, "LC_") // locale encoding overrides
	}
}

// buildClipboardEnv constructs a minimal, allowlisted environment for clipboard child processes.
func buildClipboardEnv(env systemEnv) []string {
	return buildClipboardEnvFrom(env, os.Environ())
}

func buildClipboardEnvFrom(env systemEnv, environ []string) []string {
	out := make([]string, 0, len(environ))
	for _, entry := range environ {
		k, _, ok := strings.Cut(entry, "=")
		if !ok || k == "" {
			continue
		}
		if isAllowedClipboardEnvKey(env, k) {
			out = append(out, entry)
		}
	}
	return out
}

// encodeWindowsClipboardBytes encodes UTF-8 text as UTF-16LE with BOM and CRLF line endings for clip.exe.
func encodeWindowsClipboardBytes(text string) []byte {
	normalized := strings.ReplaceAll(text, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\n", "\r\n")

	units := utf16.Encode([]rune(normalized))
	out := make([]byte, 2+len(units)*2)
	out[0] = 0xFF
	out[1] = 0xFE
	for i, u := range units {
		out[2+i*2] = byte(u)
		out[2+i*2+1] = byte(u >> 8)
	}
	return out
}

type cmdRunnerFunc func(ctx context.Context, prog string, args []string, text string) error

// defaultCmdRunner executes the external clipboard command using the detected environment.
func defaultCmdRunner(ctx context.Context, prog string, args []string, text string) error {
	return defaultCmdRunnerWithEnv(ctx, prog, args, detectSystemEnv(), text)
}

// defaultCmdRunnerWithEnv executes the external clipboard command in an isolated environment,
// streaming stdin cleanly with process-group termination and WaitDelay protection against hangs.
func defaultCmdRunnerWithEnv(ctx context.Context, prog string, args []string, env systemEnv, text string) error {
	binPath, err := safeLookPath(prog)
	if err != nil {
		return err
	}

	payload := []byte(text)
	if env.goos == "windows" || strings.EqualFold(filepath.Base(prog), "clip.exe") {
		payload = encodeWindowsClipboardBytes(text)
	}

	cmd := exec.CommandContext(ctx, binPath, args...)
	cmd.Env = buildClipboardEnv(env)
	// bytes.NewReader causes os/exec to stream over a pipe, close the write end once done,
	// ignore EPIPE when the child exits 0 early (skipStdinCopyError), and honor WaitDelay
	// by forcibly closing the pipe if an orphan descendant keeps fd 0 open.
	cmd.Stdin = bytes.NewReader(payload)

	if devNull, devErr := os.OpenFile(os.DevNull, os.O_RDWR, 0); devErr == nil {
		defer devNull.Close()
		cmd.Stdout = devNull
		cmd.Stderr = devNull // discard stderr to avoid leaking sensitive contents or hanging on inherited pipes
	} else {
		cmd.Stdout = nil
		cmd.Stderr = nil
	}

	cmd.WaitDelay = clipboardWaitDelay
	configureClipboardProcess(cmd)

	runErr := cmd.Run()
	if runErr != nil {
		if ctx.Err() != nil || errors.Is(runErr, exec.ErrWaitDelay) {
			killClipboardProcessTree(cmd)
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return runErr
	}
	return nil
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
	tmux := os.Getenv("TMUX") != ""
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

	totalCtx, cancel := context.WithTimeout(ctx, clipboardTimeout)
	defer cancel()

	if err := totalCtx.Err(); err != nil {
		return clipboardResult{}, err
	}

	// In an SSH session, prioritize OSC 52 if TTY is available, because local commands
	// would affect the remote server's clipboard rather than the user's client machine.
	if env.isSSH && env.isTTY && env.ttyWriter != nil {
		if totalCtx.Err() != nil {
			return clipboardResult{}, totalCtx.Err()
		}
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

	var lastLocalTool string
	var lastLocalErr error

	// Helper for local tool invocation
	tryLocal := func(prog string, args ...string) (clipboardResult, bool) {
		if totalCtx.Err() != nil {
			return clipboardResult{}, false
		}
		var err error
		if runCmd != nil {
			err = runCmd(totalCtx, prog, args, text)
		} else {
			err = defaultCmdRunnerWithEnv(totalCtx, prog, args, env, text)
		}
		if err == nil {
			return clipboardResult{
				Method:      prog,
				Bytes:       len(text),
				Unconfirmed: false,
				RemoteSSH:   env.isSSH,
			}, true
		}
		lastLocalTool = prog
		lastLocalErr = err
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
		if env.isX11 {
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
	}

	// Check timeout/cancellation BEFORE attempting OSC 52 fallback so timeouts or
	// hung subprocesses are never masked by errOSC52Limit or silent fallback.
	if err := totalCtx.Err(); err != nil {
		if lastLocalTool != "" {
			return clipboardResult{}, fmt.Errorf("%s timed out or cancelled: %w", lastLocalTool, err)
		}
		return clipboardResult{}, err
	}
	if lastLocalErr != nil && errors.Is(lastLocalErr, exec.ErrWaitDelay) {
		return clipboardResult{}, fmt.Errorf("%s hung holding clipboard pipe open: %w", lastLocalTool, lastLocalErr)
	}

	// 5. OSC 52 Fallback if not already attempted or if local tools failed cleanly
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

	return clipboardResult{}, errors.New("no available clipboard tool found on this system")
}
