package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/amiraq1/nabd/internal/tools"
)

const maxApprovalChars = 1600
const maxApprovalArgs = 4000
const mcpIdentityWarning = "approval is bound to the configured server, guard and environment digests; a local user may still replace a file in the short verification window; filesystem and network bounds come from the proven sandbox, not from this prompt"

type approvalPreview struct {
	Summary   string
	GuardPath string
	GuardSHA  string
	State     tools.ApprovalState
}

func (m *uiModel) approvalStillValid() bool {
	if m.approval.GuardPath == "" {
		return true
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
			return approvalPreview{Summary: clip(sanitizeForDisplay("arguments: "+tc.Function.Arguments), maxApprovalChars)}
		}

		preview := approvalPreview{GuardPath: p.Path}
		base := "base sha256: (unavailable)"
		if sum, err := reg.FileDigest(p.Path); err == nil {
			base = "base sha256: " + sum
			preview.GuardSHA = sum
		} else {
			preview.GuardPath = ""
			base = "base sha256: (unavailable: " + sanitizeForDisplay(err.Error()) + ")"
		}

		var b strings.Builder
		fmt.Fprintf(&b, "path: %s\n%s\nold sha256: %s\nnew sha256: %s\n",
			sanitizeForDisplay(p.Path), base, sha256Hex(p.OldText), sha256Hex(p.NewText))
		b.WriteString("--- old\n")
		b.WriteString(prefixBlock(p.OldText, "- "))
		b.WriteString("+++ new\n")
		b.WriteString(prefixBlock(p.NewText, "+ "))
		preview.Summary = clip(sanitizeForDisplay(b.String()), maxApprovalChars)
		return preview

	case "mcp_call":
		var p struct {
			Command   string          `json:"command"`
			Tool      string          `json:"tool"`
			Arguments json.RawMessage `json:"arguments"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &p)

		var b strings.Builder
		b.WriteString("runs with your user permissions only after an isolation backend proved filesystem, network, memory and process limits; only user-preconfigured servers are accepted\n")
		b.WriteString(mcpIdentityWarning + "\n")
		fmt.Fprintf(&b, "command: %s\ntool: %s\narguments sha256: %s\n",
			sanitizeForDisplay(p.Command), sanitizeForDisplay(p.Tool), sha256Hex(string(p.Arguments)))
		return approvalPreview{Summary: clip(sanitizeForDisplay(b.String()), maxApprovalChars)}

	case "go_verify":
		var p struct {
			Pkg string `json:"pkg"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &p)
		pkg := p.Pkg
		if pkg == "" {
			pkg = "./..."
		}
		summary := fmt.Sprintf("runs project tests via go test with your user permissions inside the proven sandbox when enabled; otherwise the tool is disabled\nfilesystem and network access are bounded, with memory and process limits\npkg: %s", sanitizeForDisplay(pkg))
		return approvalPreview{Summary: clip(sanitizeForDisplay(summary), maxApprovalChars)}

	case "web_search":
		var p struct {
			Query      string `json:"query"`
			MaxResults *int   `json:"max_results"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &p)
		provider := "disabled"
		destHost := "(unknown)"
		if reg != nil {
			provider = reg.WebSearchProvider()
			if h := reg.WebSearchDestinationHost(); h != "" {
				destHost = h
			}
		}
		maxRes := 5
		if p.MaxResults != nil {
			maxRes = *p.MaxResults
		}
		var b strings.Builder
		fmt.Fprintf(&b, "outbound network request: web search via %s\n", sanitizeForDisplay(provider))
		fmt.Fprintf(&b, "destination: %s\n", sanitizeForDisplay(destHost))
		b.WriteString("warning: search query leaves your machine to the external search provider; ensure no secrets are included\n")
		fmt.Fprintf(&b, "query (%d bytes): %s\n", len(p.Query), sanitizeForDisplay(p.Query))
		fmt.Fprintf(&b, "max_results: %d", maxRes)
		return approvalPreview{Summary: clip(sanitizeForDisplay(b.String()), maxApprovalChars)}

	case "web_fetch":
		var p struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal([]byte(tc.Function.Arguments), &p)
		dest := "(invalid URL)"
		queryNotice := "none"
		if u, err := url.Parse(p.URL); err == nil {
			if u.User != nil {
				dest = "(blocked: URL contains userinfo)"
			} else if u.Hostname() != "" {
				dest = u.Scheme + "://" + u.Hostname()
				if port := u.Port(); port != "" {
					dest += ":" + port
				}
				dest += u.EscapedPath()
			}
			if u.RawQuery != "" {
				queryNotice = fmt.Sprintf("present (%d bytes; parameter values omitted from preview)", len(u.RawQuery))
			}
		}
		isFirecrawl := reg != nil && reg.WebSearchProvider() == "firecrawl"
		var b strings.Builder
		if isFirecrawl {
			fcHost := "api.firecrawl.dev"
			if h := reg.WebSearchDestinationHost(); h != "" {
				fcHost = h
			}
			b.WriteString("outbound network request: delegated scrape via Firecrawl API (25s timeout)\n")
			fmt.Fprintf(&b, "firecrawl_endpoint: %s\n", sanitizeForDisplay(fcHost))
			fmt.Fprintf(&b, "destination: %s\n", sanitizeForDisplay(dest))
			fmt.Fprintf(&b, "query_string: %s\n", queryNotice)
			b.WriteString("delegation_limit: local SSRF/DNS check runs before sending, but Firecrawl resolves DNS, follows redirects, runs JavaScript, and loads subresources remotely (Nabd cannot enforce SSRF/rebinding on Firecrawl servers)\n")
			b.WriteString("warning: target URL leaves your device to third-party Firecrawl; verify destination and avoid signed, private, or token-bearing URLs")
		} else {
			b.WriteString("outbound network request: direct public HTTPS fetch (max 3 verified redirects, 15s timeout)\n")
			fmt.Fprintf(&b, "destination: %s\n", sanitizeForDisplay(dest))
			fmt.Fprintf(&b, "query_string: %s\n", queryNotice)
			b.WriteString("warning: request leaves your device; verify destination and check for any embedded tokens in URL parameters")
		}
		return approvalPreview{Summary: clip(sanitizeForDisplay(b.String()), maxApprovalChars)}

	default:
		return approvalPreview{Summary: clip(sanitizeForDisplay("arguments: "+tc.Function.Arguments), maxApprovalChars)}
	}
}

// prepareApprovalPreview refuses to ask for consent if any relevant material
// would be hidden or sanitized, and records the exact file state shown.
func prepareApprovalPreview(reg *tools.Registry, tc ToolCall) (approvalPreview, error) {
	raw := tc.Function.Arguments
	if len(raw) > maxApprovalArgs {
		return approvalPreview{}, fmt.Errorf("arguments are %d bytes, above the %d-byte approval limit; split into smaller edits and retry", len(raw), maxApprovalArgs)
	}
	p := buildApprovalPreview(reg, tc)
	if tc.Function.Name == "edit_file" {
		preview, err := reg.PreviewApproval(tc.Function.Name, json.RawMessage(raw))
		if err != nil {
			return approvalPreview{}, err
		}
		if sanitizeForDisplay(preview.Display) != preview.Display {
			return approvalPreview{}, fmt.Errorf("approval diff contains undisplayable control characters")
		}
		p.State = preview.State
		p.Summary += "\n" + preview.Display
	} else {
		if sanitizeForDisplay(raw) != raw {
			return approvalPreview{}, fmt.Errorf("arguments contain undisplayable control characters")
		}
		if tc.Function.Name == "mcp_call" {
			var a struct {
				Command string `json:"command"`
			}
			if err := json.Unmarshal([]byte(raw), &a); err != nil {
				return approvalPreview{}, err
			}
			if strings.ContainsAny(a.Command, "\n\r\t") || sanitizeForDisplay(a.Command) != a.Command || !strings.Contains(p.Summary, "command: "+a.Command+"\n") {
				return approvalPreview{}, fmt.Errorf("command cannot be fully displayed for approval")
			}
		}
		if tc.Function.Name == "web_fetch" {
			var a struct {
				URL string `json:"url"`
			}
			if err := json.Unmarshal([]byte(raw), &a); err == nil {
				if u, uErr := url.Parse(a.URL); uErr == nil {
					if u.User != nil {
						return approvalPreview{}, fmt.Errorf("web_fetch URL contains prohibited userinfo")
					}
					if a.URL != "" && (!strings.EqualFold(u.Scheme, "https") || u.Hostname() == "") {
						return approvalPreview{}, fmt.Errorf("web_fetch URL must be a valid https:// destination")
					}
				}
			}
		} else if tc.Function.Name != "web_search" {
			p.Summary += "\narguments: " + raw
		}
	}
	if len(p.Summary) > maxApprovalArgs {
		return approvalPreview{}, fmt.Errorf("approval preview exceeds the %d-byte limit; split the request", maxApprovalArgs)
	}
	return p, nil
}

func prefixBlock(text, prefix string) string {
	const maxLines = 12
	const maxLine = 160

	lines := strings.Split(sanitizeForDisplay(text), "\n")
	truncated := len(lines) > maxLines
	if truncated {
		lines = lines[:maxLines]
	}

	var b strings.Builder
	for _, line := range lines {
		b.WriteString(prefix)
		b.WriteString(clip(line, maxLine))
		b.WriteString("\n")
	}
	if truncated {
		b.WriteString(prefix + "...\n")
	}
	return b.String()
}

// sanitizeForDisplay removes ANSI escape sequences and control characters so
// model- or file-controlled text cannot repaint the terminal or spoof the
// approval prompt. Unicode bidirectional controls are rendered visibly.
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
		if c >= utf8.RuneSelf {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == '\u061c' || r == '\u200e' || r == '\u200f' || (r >= '\u202a' && r <= '\u202e') || (r >= '\u2066' && r <= '\u206f') {
				fmt.Fprintf(&b, "\\u%04X", r)
				i += size
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return strings.ToValidUTF8(b.String(), "\uFFFD")
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
