package main

import (
	"fmt"
	"strings"
)

const maxDiffCells = 1_000_000

type diffLine struct {
	kind byte
	text string
}

// renderEditDiff returns a compact, complete diff of the changed lines. It
// fails closed when calculating a diff would require an unbounded matrix.
func renderEditDiff(before, after []byte) (string, bool) {
	oldLines := strings.Split(strings.ReplaceAll(string(before), "\r\n", "\n"), "\n")
	newLines := strings.Split(strings.ReplaceAll(string(after), "\r\n", "\n"), "\n")
	rows, cols := len(oldLines)+1, len(newLines)+1
	if rows > maxDiffCells/cols {
		return "[diff blocked: too many lines to compare safely]\n", false
	}
	width := cols
	dp := make([]uint32, rows*cols)
	for i := len(oldLines) - 1; i >= 0; i-- {
		for j := len(newLines) - 1; j >= 0; j-- {
			at := i*width + j
			if oldLines[i] == newLines[j] {
				dp[at] = dp[(i+1)*width+j+1] + 1
			} else {
				down, right := dp[(i+1)*width+j], dp[i*width+j+1]
				if down >= right {
					dp[at] = down
				} else {
					dp[at] = right
				}
			}
		}
	}

	ops := make([]diffLine, 0, len(oldLines)+len(newLines))
	i, j := 0, 0
	for i < len(oldLines) && j < len(newLines) {
		if oldLines[i] == newLines[j] {
			ops = append(ops, diffLine{kind: ' ', text: oldLines[i]})
			i++
			j++
			continue
		}
		if dp[(i+1)*width+j] >= dp[i*width+j+1] {
			ops = append(ops, diffLine{kind: '-', text: oldLines[i]})
			i++
		} else {
			ops = append(ops, diffLine{kind: '+', text: newLines[j]})
			j++
		}
	}
	for ; i < len(oldLines); i++ {
		ops = append(ops, diffLine{kind: '-', text: oldLines[i]})
	}
	for ; j < len(newLines); j++ {
		ops = append(ops, diffLine{kind: '+', text: newLines[j]})
	}

	keep := make([]bool, len(ops))
	for index, op := range ops {
		if op.kind == ' ' {
			continue
		}
		start, end := index-2, index+2
		if start < 0 {
			start = 0
		}
		if end >= len(ops) {
			end = len(ops) - 1
		}
		for k := start; k <= end; k++ {
			keep[k] = true
		}
	}

	var out strings.Builder
	lastKept := -2
	for index, op := range ops {
		if !keep[index] {
			continue
		}
		if index > lastKept+1 {
			if lastKept >= 0 {
				out.WriteString("  ... unchanged lines omitted ...\n")
			}
		}
		fmt.Fprintf(&out, "%c %s\n", op.kind, op.text)
		lastKept = index
	}
	return out.String(), true
}
