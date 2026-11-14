package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
)

const maxApprovalChars = 1600

type approvalPreview struct {
	Summary   string
	GuardPath string
	GuardSHA  string
	Blocked   bool
}

func (m *uiModel) approvalStillValid() bool {
	if m.approval.GuardPath == "" {
		return m.approval.GuardSHA == "" && !m.approval.Blocked
	}
	if m.approval.GuardSHA == "" {
		return false
	}
	sum, err := m.registry.FileDigest(m.approval.GuardPath)
	if err != nil {
		return false
	}
	return sum == m.approval.GuardSHA
}

func buildApprovalPreview(reg *tools.Registry, tc ToolCall) approvalPreview {
	switch tc.Function.Name {
	case "edit_file":
		var p struct {
			Path    string `json:"path"`
			OldText string `json:"old_text"`
			NewText string `json:"new_text"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &p); err != nil {
			return approvalPreview{Summary: "Invalid edit_file arguments; cannot approve.", Blocked: true}
		}

		preview := approvalPreview{GuardPath: p.Path}
		baseSHA, before, after, err := reg.PreviewEdit(p.Path, p.OldText, p.NewText)
		if err != nil {
			preview.Blocked = true
			preview.Summary = fmt.Sprintf("path: %s\nbase sha256: (unavailable: %s)\n[approval blocked: cannot compute the complete edit preview]", oneLineField(p.Path), oneLineField(err.Error()))
			return preview
		}
		preview.GuardSHA = baseSHA
		diff, diffComplete := renderEditDiff(before, after)

		var b strings.Builder
		fmt.Fprintf(&b, "path: %s\nbase sha256: %s\nold input sha256: %s\nnew input sha256: %s\n",
			oneLineField(p.Path), baseSHA, sha256Hex(p.OldText), sha256Hex(p.NewText))
		b.WriteString("--- actual final diff (includes Go formatting; CRLF is preserved)\n")
		b.WriteString(diff)
		preview.Summary = sanitizeForDisplay(b.String())
		unsafeText := unsafeSingleLine(p.Path) || sanitizeForDisplay(string(before)) != string(before) || sanitizeForDisplay(string(after)) != string(after) || sanitizeForDisplay(p.OldText) != p.OldText || sanitizeForDisplay(p.NewText) != p.NewText
		if unsafeText || !diffComplete || len(preview.Summary) > maxApprovalChars {
			preview.Blocked = true
			reason := "show the complete diff before approving"
			if unsafeText {
				reason = "control characters or line breaks are hidden/escaped; inspect the file manually"
			}
			preview.Summary = truncate(preview.Summary, maxApprovalChars) + "\n[approval blocked: " + reason + "]"
		}
		return preview

	case "mcp_call":
		var p struct {
			Command   string          `json:"command"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &p); err != nil || p.Command == "" || p.Tool == "" {
			return approvalPreview{Summary: "Invalid mcp_call arguments; cannot approve.", Blocked: true}
		}
		argsText, hidden, err := safeMCPArguments(p.Arguments)
		if err != nil {
			return approvalPreview{Summary: "Invalid or oversized mcp_call arguments; cannot approve.", Blocked: true}
		}

		var b strings.Builder
		fmt.Fprintf(&b, "command: %s\ntool: %s\narguments:\n%s\n",
			oneLineField(p.Command), oneLineField(p.Tool), argsText)
		b.WriteString("warning: launches an external process outside the workspace\n")
		unsafeFields := unsafeSingleLine(p.Command) || unsafeSingleLine(p.Tool)
		if hidden {
			b.WriteString("[approval blocked: string or sensitive argument values are redacted; inspect them before approving]\n")
		}
		if unsafeFields {
			b.WriteString("[approval blocked: command/tool contains control characters or line breaks]\n")
		}
		summary := sanitizeForDisplay(b.String())
		if len(summary) > maxApprovalChars {
			return approvalPreview{Summary: truncate(summary, maxApprovalChars) + "\n[approval blocked: preview exceeds limit]", Blocked: true}
		}
		return approvalPreview{Summary: summary, Blocked: hidden || unsafeFields}

	case "go_verify":
		var p struct {
			Pkg string `json:"pkg"`
		}
		if err := json.Unmarshal([]byte(tc.Function.Arguments), &p); err != nil {
			return approvalPreview{Summary: "Invalid go_verify arguments; cannot approve.", Blocked: true}
		}
		pkg := p.Pkg
		if pkg == "" {
			pkg = "./..."
		}
		summary := fmt.Sprintf("runs project tests via go test\npkg: %s\nwarning: executes project code; secrets are excluded from its environment", oneLineField(pkg))
		preview := approvalPreview{Summary: clip(sanitizeForDisplay(summary), maxApprovalChars)}
		if unsafeSingleLine(pkg) {
			preview.Blocked = true
			preview.Summary += "\n[approval blocked: package pattern contains control characters or line breaks]"
		}
		return preview

	default:
		return approvalPreview{Summary: "No safe approval preview is available for this tool.", Blocked: true}
	}
}

// safeMCPArguments never renders string values. Since those values may be
// credentials or arbitrary media payloads, any hidden value blocks approval.
// Numeric and boolean arguments remain visible unless their key is sensitive.
func safeMCPArguments(raw json.RawMessage) (string, bool, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return "", false, fmt.Errorf("arguments must be a JSON object")
	}
	state := &mcpPreviewState{remaining: 256}
	text, err := renderMCPValue(raw, "", 0, state)
	if err != nil {
		return "", false, err
	}
	return text, state.hidden, nil
}

type mcpPreviewState struct {
	remaining int
	hidden    bool
}

func renderMCPValue(raw json.RawMessage, key string, depth int, state *mcpPreviewState) (string, error) {
	state.remaining--
	if state.remaining < 0 || depth > 8 {
		state.hidden = true
		return `"<redacted: preview limit>"`, nil
	}
	if sensitiveMCPKey(key) {
		state.hidden = true
		return `"<redacted: sensitive field>"`, nil
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	switch v := value.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(v), nil
	case json.Number:
		return string(v), nil
	case string:
		state.hidden = true
		return strconv.Quote(fmt.Sprintf("<redacted: string, %d bytes>", len(v))), nil
	case []any:
		if len(v) > 24 {
			state.hidden = true
			v = v[:24]
		}
		parts := make([]string, 0, len(v))
		for _, item := range v {
			itemRaw, err := json.Marshal(item)
			if err != nil {
				return "", err
			}
			text, err := renderMCPValue(itemRaw, "", depth+1, state)
			if err != nil {
				return "", err
			}
			parts = append(parts, text)
		}
		return "[" + strings.Join(parts, ", ") + "]", nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, childKey := range keys {
			keyText := oneLineField(childKey)
			if keyText != childKey {
				state.hidden = true
			}
			childRaw, err := json.Marshal(v[childKey])
			if err != nil {
				return "", err
			}
			text, err := renderMCPValue(childRaw, childKey, depth+1, state)
			if err != nil {
				return "", err
			}
			parts = append(parts, strconv.Quote(keyText)+": "+text)
		}
		return "{" + strings.Join(parts, ", ") + "}", nil
	default:
		return "", fmt.Errorf("unsupported JSON value")
	}
}

func sensitiveMCPKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, marker := range []string{
		"password", "passwd", "secret", "token", "apikey", "authorization", "cookie",
		"credential", "privatekey", "clientsecret", "accesskey", "refresh",
		"image", "audio", "video", "media", "blob", "bytes", "base64", "attachment",
	} {
		if strings.Contains(key, marker) {
			return true
		}
	}
	return false
}

// sanitizeForDisplay removes ANSI escape sequences and terminal controls while
// preserving intentional newlines in a diff block.
func sanitizeForDisplay(s string) string {
	s = strings.ToValidUTF8(s, "\uFFFD")
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		if c == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		if c < 0x20 {
			if c == '\n' || c == '\t' {
				b.WriteByte(c)
			}
			i++
			continue
		}
		if c == 0x7f {
			i++
			continue
		}
		if c == 0xc2 && i+1 < len(s) && s[i+1] >= 0x80 && s[i+1] <= 0x9f {
			i += 2
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size == 0 {
			size = 1
		}
		r := []rune(s[i : i+size])[0]
		if r == '\u2028' || r == '\u2029' || isBidiControl(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
		} else {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return strings.ToValidUTF8(b.String(), "\uFFFD")
}

func isBidiControl(r rune) bool {
	return r == '\u061c' || r == '\u200e' || r == '\u200f' ||
		(r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u206f')
}

func oneLineField(s string) string {
	s = sanitizeForDisplay(s)
	replacer := strings.NewReplacer("\n", `\n`, "\r", `\r`, "\t", `\t`, "\v", `\v`, "\f", `\f`)
	return replacer.Replace(s)
}

func unsafeSingleLine(s string) bool {
	return sanitizeForDisplay(s) != s || strings.ContainsAny(s, "\r\n\t\v\f") || strings.Contains(s, "\u2028") || strings.Contains(s, "\u2029")
}

func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	switch s[i+1] {
	case '[':
		for j := i + 2; j < len(s); j++ {
			if s[j] >= 0x40 && s[j] <= 0x7e {
				return j + 1
			}
		}
		return len(s)
	case ']':
		for j := i + 2; j < len(s); j++ {
			if s[j] == 0x07 {
				return j + 1
			}
			if s[j] == 0x1b && j+1 < len(s) && s[j+1] == '\\' {
				return j + 2
			}
		}
		return len(s)
	default:
		_, size := utf8.DecodeRuneInString(s[i+1:])
		return i + 1 + size
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return truncate(s, n)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 0 {
		return ""
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "..."
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
