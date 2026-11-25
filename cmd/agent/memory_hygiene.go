package main

import (
	"errors"
	"fmt"
	"math"
	"os"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	defaultPromptHistoryLimit = 200
	defaultSessionUnitsLimit  = 200
	toolCompactionThreshold   = 1024 // 1 KiB
	maxSnippetBytes           = 256
	minMemLimitBytes          = 32 * 1024 * 1024        // 32 MiB
	maxMemLimitBytes          = 16 * 1024 * 1024 * 1024 // 16 GiB
	compactMarkerPrefix       = "... [compacted from "
)

// parseMemoryLimit parses documented memory limit strings into bytes.
// Supported explicit units:
// B: 1 byte
// K, KB: 1,000 bytes
// KiB: 1,024 bytes
// M, MB: 1,000,000 bytes
// MiB: 1,048,576 bytes
// G, GB: 1,000,000,000 bytes
// GiB: 1,073,741,824 bytes
// Rejects decimals, negatives, zeros, overflow, and values outside [32 MiB, 16 GiB].
func parseMemoryLimit(s string) (int64, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return 0, errors.New("memory limit string is empty")
	}

	// Split number and unit
	numEnd := 0
	for numEnd < len(trimmed) && trimmed[numEnd] >= '0' && trimmed[numEnd] <= '9' {
		numEnd++
	}

	if numEnd == 0 {
		return 0, fmt.Errorf("missing number in memory limit %q", trimmed)
	}

	val, err := strconv.ParseInt(trimmed[:numEnd], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number in memory limit: %w", err)
	}
	if val <= 0 {
		return 0, fmt.Errorf("memory limit must be positive, got %d", val)
	}

	unitStr := strings.TrimSpace(trimmed[numEnd:])
	var multiplier int64

	switch unitStr {
	case "", "B", "b":
		multiplier = 1
	case "K", "k", "KB", "kb":
		multiplier = 1000
	case "KiB", "kib", "KIB":
		multiplier = 1024
	case "M", "m", "MB", "mb":
		multiplier = 1000 * 1000
	case "MiB", "mib", "MIB":
		multiplier = 1024 * 1024
	case "G", "g", "GB", "gb":
		multiplier = 1000 * 1000 * 1000
	case "GiB", "gib", "GIB":
		multiplier = 1024 * 1024 * 1024
	default:
		return 0, fmt.Errorf("unrecognized memory unit %q; supported units: B, K, KiB, M, MiB, G, GiB", unitStr)
	}

	// Overflow check before multiplication
	if val > math.MaxInt64/multiplier {
		return 0, fmt.Errorf("memory limit overflows int64: %q", s)
	}

	totalBytes := val * multiplier
	if totalBytes < minMemLimitBytes || totalBytes > maxMemLimitBytes {
		return 0, fmt.Errorf("memory limit %d bytes (%s) is out of bounds [%d bytes (32 MiB), %d bytes (16 GiB)]",
			totalBytes, trimmed, minMemLimitBytes, maxMemLimitBytes)
	}

	return totalBytes, nil
}

// applyMemoryLimitFromEnv reads and enforces NABD_MEM_LIMIT if set in the environment.
// Returns (0, nil) if not set.
// If set and invalid or empty, returns an error.
func applyMemoryLimitFromEnv() (int64, error) {
	val, set := os.LookupEnv("NABD_MEM_LIMIT")
	if !set {
		return 0, nil
	}
	if strings.TrimSpace(val) == "" {
		return 0, errors.New("NABD_MEM_LIMIT is set but empty; expected e.g. 64MiB, 128M, 1GiB")
	}

	limitBytes, err := parseMemoryLimit(val)
	if err != nil {
		return 0, fmt.Errorf("invalid NABD_MEM_LIMIT: %w", err)
	}

	debug.SetMemoryLimit(limitBytes)
	return limitBytes, nil
}

// prunePromptHistory enforces a maximum size for the prompt history buffer.
// When exceeding maxEntries, older entries are dropped and a fresh slice is allocated.
func prunePromptHistory(history []string, maxEntries int) []string {
	if maxEntries <= 0 || len(history) <= maxEntries {
		return history
	}
	res := make([]string, maxEntries)
	copy(res, history[len(history)-maxEntries:])
	return res
}

// compactToolOutputs reduces old, large tool outputs in messages while preserving:
// 1. The last tool output in the session (for Ctrl+O full output and /copy tool).
// 2. The last two completed tool call units (active working context).
// 3. Any tool output that is in-flight, unapproved, undisclosed, or already compacted.
// Returns the count of compacted outputs and estimated bytes reduced.
func compactToolOutputs(messages []Message) (compactedCount int, bytesReduced int64) {
	if len(messages) == 0 {
		return 0, 0
	}

	// 1. Identify the last tool message index in the entire slice
	lastToolIdx := -1
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role == "tool" {
			lastToolIdx = i
			break
		}
	}

	// 2. Group messages into units to identify the last two completed tool units
	units := groupMessageUnits(messages)
	protectedUnitIndices := make(map[int]bool)

	// Collect completed tool units in reverse
	toolUnitCount := 0
	for uIdx := len(units) - 1; uIdx >= 0; uIdx-- {
		unit := units[uIdx]
		hasTool := false
		for _, msg := range unit {
			if msg.Role == "tool" || (msg.Role == "assistant" && len(msg.ToolCalls) > 0) {
				hasTool = true
				break
			}
		}
		if hasTool {
			protectedUnitIndices[uIdx] = true
			toolUnitCount++
			if toolUnitCount >= 2 {
				break
			}
		}
	}

	// Build a set of protected message pointers or indices
	protectedMsgIndices := make(map[int]bool)
	if lastToolIdx >= 0 {
		protectedMsgIndices[lastToolIdx] = true
	}

	msgGlobalIdx := 0
	for uIdx, unit := range units {
		isProtectedUnit := protectedUnitIndices[uIdx]
		for range unit {
			if isProtectedUnit {
				protectedMsgIndices[msgGlobalIdx] = true
			}
			msgGlobalIdx++
		}
	}

	// 3. Compact eligible tool messages
	for i := 0; i < len(messages); i++ {
		if messages[i].Role != "tool" || protectedMsgIndices[i] {
			continue
		}

		content := messages[i].Content
		origLen := len(content)
		if origLen <= toolCompactionThreshold {
			continue
		}

		// Idempotency check: don't compact if already compacted
		if strings.Contains(content, compactMarkerPrefix) {
			continue
		}

		// Clean UTF-8 truncation up to maxSnippetBytes
		cut := min(maxSnippetBytes, origLen)
		for cut > 0 && !utf8.RuneStart(content[cut]) {
			cut--
		}

		snippet := content[:cut]
		marker := fmt.Sprintf("\n... [compacted from %d bytes to save memory]", origLen)
		compactedContent := strings.Clone(snippet + marker)

		reduced := int64(origLen - len(compactedContent))
		if reduced > 0 {
			bytesReduced += reduced
		}

		messages[i].Content = compactedContent
		compactedCount++
	}

	return compactedCount, bytesReduced
}

// groupMessageUnits decomposes a slice of messages into atomic units.
// An assistant message with tool calls and all corresponding tool results form a single atomic unit.
// Non-tool messages form their own 1-message units.
func groupMessageUnits(messages []Message) [][]Message {
	if len(messages) == 0 {
		return nil
	}

	var units [][]Message
	for i := 0; i < len(messages); {
		msg := messages[i]
		if msg.Role == "assistant" && len(msg.ToolCalls) > 0 {
			callIDs := make(map[string]bool, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				callIDs[tc.ID] = true
			}
			unit := []Message{msg}
			j := i + 1
			for j < len(messages) && messages[j].Role == "tool" {
				if callIDs[messages[j].ToolCallID] {
					unit = append(unit, messages[j])
				}
				j++
			}
			units = append(units, unit)
			i = j
			continue
		}
		units = append(units, []Message{msg})
		i++
	}
	return units
}

// pruneSessionMessages limits the session messages to maxUnits atomic units while preserving:
// 1. Initial system message unit.
// 2. The newest units up to maxUnits.
// 3. Any unit containing the last assistant reply or last tool output.
func pruneSessionMessages(messages []Message, maxUnits int, isPending bool) []Message {
	if isPending || maxUnits <= 0 || len(messages) == 0 {
		return messages
	}

	units := groupMessageUnits(messages)
	if len(units) <= maxUnits {
		return messages
	}

	var systemUnit []Message
	start := 0
	if len(units) > 0 && len(units[0]) > 0 && units[0][0].Role == "system" {
		systemUnit = units[0]
		start = 1
	}

	candidateUnits := units[start:]
	keepCount := maxUnits - len(systemUnit)
	if keepCount <= 0 {
		keepCount = 1
	}

	if len(candidateUnits) <= keepCount {
		return messages
	}

	keptUnits := candidateUnits[len(candidateUnits)-keepCount:]

	// Count total messages
	totalMsgs := len(systemUnit)
	for _, u := range keptUnits {
		totalMsgs += len(u)
	}

	res := make([]Message, 0, totalMsgs)
	if len(systemUnit) > 0 {
		res = append(res, systemUnit...)
	}
	for _, u := range keptUnits {
		res = append(res, u...)
	}
	return res
}

// formatMemoryStatus formats HeapAlloc and the active Go soft memory limit for /status.
func formatMemoryStatus() string {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	heapAllocMB := float64(ms.HeapAlloc) / (1024 * 1024)

	// Query Go soft memory limit without mutating it
	curLimit := debug.SetMemoryLimit(-1)

	var limitDesc string
	if curLimit == math.MaxInt64 || curLimit <= 0 {
		limitDesc = "unset"
	} else {
		limitDesc = formatBytes(curLimit)
	}

	var sourceDesc string
	if envVal := os.Getenv("NABD_MEM_LIMIT"); envVal != "" {
		sourceDesc = "NABD_MEM_LIMIT"
	} else if gomem := os.Getenv("GOMEMLIMIT"); gomem != "" {
		sourceDesc = "GOMEMLIMIT"
	} else {
		sourceDesc = "default"
	}

	return fmt.Sprintf("Memory: HeapAlloc: %.1f MiB | Go soft limit: %s (%s)", heapAllocMB, limitDesc, sourceDesc)
}

func formatBytes(b int64) string {
	if b >= 1024*1024*1024 && b%(1024*1024*1024) == 0 {
		return fmt.Sprintf("%d GiB", b/(1024*1024*1024))
	}
	if b >= 1000*1000*1000 && b%(1000*1000*1000) == 0 {
		return fmt.Sprintf("%d GB", b/(1000*1000*1000))
	}
	if b >= 1024*1024 && b%(1024*1024) == 0 {
		return fmt.Sprintf("%d MiB", b/(1024*1024))
	}
	if b >= 1000*1000 && b%(1000*1000) == 0 {
		return fmt.Sprintf("%d MB", b/(1000*1000))
	}
	if b >= 1024 && b%1024 == 0 {
		return fmt.Sprintf("%d KiB", b/1024)
	}
	if b >= 1024*1024 {
		return fmt.Sprintf("%.1f MiB", float64(b)/(1024*1024))
	}
	return fmt.Sprintf("%d B", b)
}
