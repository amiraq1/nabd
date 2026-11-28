package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	maxGrepQueryBytes    = 256
	maxGrepOutputBytes   = 8 * 1024  // 8 KiB JSON output budget
	maxGrepLineBytes     = 240       // per-line text cap
	maxGrepFileBytes     = 512 << 10 // 512 KiB per file
	maxGrepTotalBytes    = 16 << 20  // 16 MiB total read budget
	maxGrepFiles         = 2000      // max files inspected
	maxGrepDepth         = 15        // max directory recursion depth
	maxGrepDirEntries    = 8192      // max directory entries scanned
	defaultGrepResults   = 50        // default max_results
	grepMaxResultsLimit  = 100       // hard max_results cap
	grepBinarySniffBytes = 8 << 10   // NUL sniff window
)

type grepSearchArgs struct {
	Query         string `json:"query"`
	Path          string `json:"path"`
	IsRegex       bool   `json:"is_regex"`
	CaseSensitive *bool  `json:"case_sensitive"`
	FileGlob      string `json:"file_glob"`
	MaxResults    *int   `json:"max_results"`
}

type grepMatch struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Text string `json:"text"`
}

type grepSearchResult struct {
	Matches      []grepMatch `json:"matches"`
	TotalMatches int         `json:"total_matches"`
	FilesScanned int         `json:"files_scanned"`
	Truncated    bool        `json:"truncated"`
}

func parseGrepSearchArgs(args json.RawMessage) (grepSearchArgs, error) {
	var p grepSearchArgs
	if len(args) == 0 || string(args) == "null" {
		return p, fmt.Errorf("query is required")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return p, fmt.Errorf("invalid grep_search arguments: %w", err)
	}
	if strings.TrimSpace(p.Query) == "" {
		return p, fmt.Errorf("query is required")
	}
	if len(p.Query) > maxGrepQueryBytes {
		return p, fmt.Errorf("query must be at most %d bytes", maxGrepQueryBytes)
	}
	if strings.TrimSpace(p.Path) == "" {
		p.Path = "."
	}
	if p.MaxResults != nil {
		if *p.MaxResults < 1 || *p.MaxResults > grepMaxResultsLimit {
			return p, fmt.Errorf("max_results must be between 1 and %d (received %d)", grepMaxResultsLimit, *p.MaxResults)
		}
	}
	if p.FileGlob != "" {
		if _, err := filepath.Match(p.FileGlob, "x"); err != nil {
			return p, fmt.Errorf("invalid file_glob: %w", err)
		}
	}
	return p, nil
}

// validateGrepSearchPath mirrors validateRepoMapPath: the target must resolve
// inside the workspace, avoid sensitive paths, and have no symlink component.
// os.Root remains the primary containment barrier at open time.
func (r *Registry) validateGrepSearchPath(rawPath string) (string, error) {
	rel, err := r.resolveSafePath(rawPath)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return ".", nil
	}
	parts := strings.Split(filepath.ToSlash(rel), "/")
	cur := ""
	for _, part := range parts {
		if cur == "" {
			cur = part
		} else {
			cur = cur + "/" + part
		}
		info, err := r.root.Lstat(cur)
		if err != nil {
			return "", fmt.Errorf("stat path component %q: %w", cur, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("symlink path components are not allowed: %s", cur)
		}
	}
	return rel, nil
}

func grepGlobMatches(glob, rel string) bool {
	if glob == "" {
		return true
	}
	if ok, _ := filepath.Match(glob, filepath.Base(rel)); ok {
		return true
	}
	ok, _ := filepath.Match(glob, filepath.ToSlash(rel))
	return ok
}

// truncateGrepLine caps a line at maxGrepLineBytes without splitting a UTF-8
// rune and marks the cut so the caller knows the text is partial.
func truncateGrepLine(line string) string {
	if len(line) <= maxGrepLineBytes {
		return line
	}
	cut := maxGrepLineBytes - 3
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return line[:cut] + "..."
}

// grepSearch implements the grep_search tool. Output is deterministic: files
// are visited breadth-first with lexicographically sorted directory entries,
// and matches keep that order.
func (r *Registry) grepSearch(ctx context.Context, args json.RawMessage) (string, error) {
	p, err := parseGrepSearchArgs(args)
	if err != nil {
		return "", err
	}
	relPath, err := r.validateGrepSearchPath(p.Path)
	if err != nil {
		return "", err
	}

	pattern := p.Query
	if !p.IsRegex {
		pattern = regexp.QuoteMeta(pattern)
	}
	caseSensitive := true
	if p.CaseSensitive != nil {
		caseSensitive = *p.CaseSensitive
	}
	if !caseSensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex: %w", err)
	}

	maxResults := defaultGrepResults
	if p.MaxResults != nil {
		maxResults = *p.MaxResults
	}

	var (
		matches      []grepMatch
		totalMatches int
		filesScanned int
		truncated    bool
		totalBytes   int64
	)

	// scan reads one regular file and appends matches. It reports whether the
	// scan must stop because a resource budget was reached.
	scan := func(rel string) (bool, error) {
		if ctx.Err() != nil {
			return true, ctx.Err()
		}
		if totalBytes >= maxGrepTotalBytes || filesScanned >= maxGrepFiles {
			return true, nil
		}
		info, err := r.root.Stat(rel)
		if err != nil || !info.Mode().IsRegular() {
			return false, nil
		}
		if totalBytes+info.Size() > maxGrepTotalBytes {
			return true, nil
		}
		f, err := openRegularSingleLink(r.root, rel)
		if err != nil {
			return false, nil
		}
		data, readErr := io.ReadAll(io.LimitReader(f, maxGrepFileBytes+1))
		_ = f.Close()
		if readErr != nil || len(data) > maxGrepFileBytes {
			return false, nil
		}
		totalBytes += int64(len(data))
		filesScanned++

		sniff := data
		if len(sniff) > grepBinarySniffBytes {
			sniff = sniff[:grepBinarySniffBytes]
		}
		if bytes.IndexByte(sniff, 0) >= 0 {
			return false, nil
		}

		for i, raw := range bytes.Split(data, []byte("\n")) {
			if ctx.Err() != nil {
				return true, ctx.Err()
			}
			line := strings.TrimSuffix(string(raw), "\r")
			if !re.MatchString(line) {
				continue
			}
			totalMatches++
			if len(matches) < maxResults {
				matches = append(matches, grepMatch{
					Path: rel,
					Line: i + 1,
					Text: truncateGrepLine(line),
				})
			}
		}
		return false, nil
	}

	targetInfo, err := r.root.Stat(relPath)
	if err != nil {
		return "", err
	}

	if !targetInfo.IsDir() {
		if _, err := scan(relPath); err != nil {
			return "", err
		}
	} else {
		type dirWork struct {
			rel   string
			depth int
		}
		queue := []dirWork{{rel: relPath, depth: 0}}
		scannedEntries := 0

	walk:
		for len(queue) > 0 {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			curr := queue[0]
			queue = queue[1:]

			f, err := openWorkspaceFile(r.root, curr.rel)
			if err != nil {
				continue
			}
			entries, err := f.ReadDir(-1)
			_ = f.Close()
			if err != nil {
				continue
			}
			sort.Slice(entries, func(i, j int) bool {
				return entries[i].Name() < entries[j].Name()
			})

			for _, e := range entries {
				scannedEntries++
				if scannedEntries > maxGrepDirEntries {
					truncated = true
					break walk
				}
				name := e.Name()
				if e.Type()&os.ModeSymlink != 0 {
					continue
				}
				childRel := filepath.Join(curr.rel, name)
				if isSensitivePath(childRel) {
					continue
				}
				if e.IsDir() {
					if shouldSkipRepoMapDir(name) {
						continue
					}
					if curr.depth < maxGrepDepth {
						queue = append(queue, dirWork{rel: childRel, depth: curr.depth + 1})
					}
					continue
				}
				if !grepGlobMatches(p.FileGlob, childRel) {
					continue
				}
				stop, err := scan(childRel)
				if err != nil {
					return "", err
				}
				if stop {
					truncated = true
					break walk
				}
				if len(matches) >= maxResults {
					truncated = true
					break walk
				}
			}
		}
	}

	if len(matches) >= maxResults {
		truncated = true
	}

	res := grepSearchResult{
		Matches:      matches,
		TotalMatches: totalMatches,
		FilesScanned: filesScanned,
		Truncated:    truncated,
	}
	if res.Matches == nil {
		res.Matches = []grepMatch{}
	}
	out, err := json.Marshal(res)
	if err != nil {
		return "", err
	}
	for len(out) > maxGrepOutputBytes && len(res.Matches) > 0 {
		res.Matches = res.Matches[:len(res.Matches)-1]
		res.Truncated = true
		out, err = json.Marshal(res)
		if err != nil {
			return "", err
		}
	}
	return string(out), nil
}
