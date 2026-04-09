package tools

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// diffLine is one line inside a unified diff hunk (Op is ' ', '+', or '-').
type diffLine struct {
	Op   byte
	Text string
}

// diffHunk is one @@ ... @@ block in a unified diff.
type diffHunk struct {
	OldStart, OldCount int
	NewStart, NewCount int
	Lines               []diffLine
}

var hunkHeaderRE = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

// parseUnifiedDiff parses unified diff hunks from a string. Leading diff --git, ---, +++
// lines are skipped. Malformed input returns an error.
func parseUnifiedDiff(diff string) ([]diffHunk, error) {
	diff = strings.ReplaceAll(diff, "\r\n", "\n")
	diff = strings.TrimPrefix(diff, "\n")
	lines := strings.Split(diff, "\n")
	var hunks []diffHunk
	i := 0
	for i < len(lines) {
		line := lines[i]
		trim := strings.TrimSpace(line)
		if trim == "" {
			i++
			continue
		}
		if strings.HasPrefix(line, "diff --git ") ||
			strings.HasPrefix(line, "--- ") ||
			strings.HasPrefix(line, "+++ ") ||
			strings.HasPrefix(line, "index ") ||
			strings.HasPrefix(line, "similarity index ") {
			i++
			continue
		}
		if strings.HasPrefix(trim, "\\") { // \ No newline at end of file
			i++
			continue
		}
		m := hunkHeaderRE.FindStringSubmatch(line)
		if m == nil {
			if len(hunks) == 0 {
				return nil, fmt.Errorf("patch: expected hunk header @@ ... @@, got %q", truncate(line, 80))
			}
			break
		}
		oldStart := atoi(m[1])
		oldCount := 1
		if m[2] != "" {
			oldCount = atoi(m[2])
		}
		newStart := atoi(m[3])
		newCount := 1
		if m[4] != "" {
			newCount = atoi(m[4])
		}
		i++
		var hLines []diffLine
		for i < len(lines) {
			l := lines[i]
			if strings.HasPrefix(l, "@@ ") {
				break
			}
			if l == "" {
				hLines = append(hLines, diffLine{Op: ' ', Text: ""})
				i++
				continue
			}
			t := strings.TrimPrefix(l, "\t")
			if len(t) == 0 {
				return nil, fmt.Errorf("patch: empty line inside hunk")
			}
			op := t[0]
			switch op {
			case ' ', '+', '-':
				hLines = append(hLines, diffLine{Op: op, Text: t[1:]})
			default:
				if strings.HasPrefix(strings.TrimSpace(l), "\\") {
					i++
					continue
				}
				return nil, fmt.Errorf("patch: bad hunk line prefix %q", truncate(l, 80))
			}
			i++
		}
		if len(hLines) == 0 {
			return nil, fmt.Errorf("patch: empty hunk at @@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)
		}
		// Trim trailing empty context lines that exceed the header counts.
		// These arise from string formatting artifacts or inter-hunk whitespace;
		// genuine trailing empty context (matching the header) is preserved.
		for len(hLines) > 0 {
			last := hLines[len(hLines)-1]
			if last.Op != ' ' || last.Text != "" {
				break
			}
			gotO, gotN := countHunkSides(hLines)
			if gotO == oldCount && gotN == newCount {
				break
			}
			hLines = hLines[:len(hLines)-1]
		}
		gotOld, gotNew := countHunkSides(hLines)
		if oldCount != gotOld {
			return nil, fmt.Errorf("patch: hunk old count mismatch: header says %d, hunk has %d old lines", oldCount, gotOld)
		}
		if newCount != gotNew {
			return nil, fmt.Errorf("patch: hunk new count mismatch: header says %d, hunk has %d new lines", newCount, gotNew)
		}
		hunks = append(hunks, diffHunk{
			OldStart: oldStart,
			OldCount: oldCount,
			NewStart: newStart,
			NewCount: newCount,
			Lines:    hLines,
		})
	}
	if len(hunks) == 0 {
		return nil, fmt.Errorf("patch: no hunks found")
	}
	return hunks, nil
}

func atoi(s string) int {
	var n int
	for _, c := range s {
		if c < '0' || c > '9' {
			return n
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func countHunkSides(lines []diffLine) (oldN, newN int) {
	for _, l := range lines {
		switch l.Op {
		case ' ':
			oldN++
			newN++
		case '-':
			oldN++
		case '+':
			newN++
		}
	}
	return oldN, newN
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// splitLines splits s into lines without trailing \n on each element.
// endsWithNL is true if s ended with a newline.
func splitLines(s string) (lines []string, endsWithNL bool) {
	if s == "" {
		return nil, false
	}
	s = strings.ReplaceAll(s, "\r\n", "\n")
	endsWithNL = strings.HasSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return []string{}, endsWithNL
	}
	return strings.Split(s, "\n"), endsWithNL
}

func joinLines(lines []string, endsWithNL bool) string {
	out := strings.Join(lines, "\n")
	if endsWithNL {
		out += "\n"
	}
	return out
}

const patchFuzz = 3

// applyHunks applies unified diff hunks to original. Hunks must reference the original
// file line numbers; they are applied in reverse order of OldStart so positions stay valid.
func applyHunks(original string, hunks []diffHunk) (string, error) {
	if len(hunks) == 0 {
		return original, nil
	}
	lines, endsWithNL := splitLines(original)
	// Copy for mutation
	cur := append([]string(nil), lines...)

	sorted := append([]diffHunk(nil), hunks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].OldStart > sorted[j].OldStart })

	for hi, h := range sorted {
		start, err := findHunkStart(cur, h, hi)
		if err != nil {
			return "", err
		}
		cur = applyHunkAt(cur, start, h)
	}
	return joinLines(cur, endsWithNL), nil
}

func findHunkStart(lines []string, h diffHunk, hunkIndex int) (int, error) {
	want := h.OldStart - 1
	if want < 0 {
		return 0, fmt.Errorf("patch: hunk %d: invalid old start %d", hunkIndex+1, h.OldStart)
	}
	try := []int{0}
	for d := 1; d <= patchFuzz; d++ {
		try = append(try, d, -d)
	}
	for _, delta := range try {
		off := want + delta
		if off < 0 || off > len(lines) {
			continue
		}
		if hunkMatches(lines, off, h) {
			return off, nil
		}
	}
	return 0, fmt.Errorf("patch: hunk %d: context mismatch near line %d (tried ±%d lines)", hunkIndex+1, h.OldStart, patchFuzz)
}

func hunkMatches(lines []string, start int, h diffHunk) bool {
	pos := start
	for _, hl := range h.Lines {
		switch hl.Op {
		case ' ', '-':
			if pos >= len(lines) || lines[pos] != hl.Text {
				return false
			}
			pos++
		case '+':
			// no old line
		}
	}
	return true
}

func applyHunkAt(lines []string, start int, h diffHunk) []string {
	oldPos := start
	var mid []string
	for _, hl := range h.Lines {
		switch hl.Op {
		case ' ':
			mid = append(mid, lines[oldPos])
			oldPos++
		case '-':
			oldPos++
		case '+':
			mid = append(mid, hl.Text)
		}
	}
	out := make([]string, 0, start+len(mid)+len(lines)-oldPos)
	out = append(out, lines[:start]...)
	out = append(out, mid...)
	out = append(out, lines[oldPos:]...)
	return out
}

// patchFileWorkspace reads path under root, applies unified diff, overwrites the file.
func patchFileWorkspace(root, rel, diff string) (summary string, err error) {
	hunks, err := parseUnifiedDiff(diff)
	if err != nil {
		return "", err
	}
	const maxBytes = 10 * 1024 * 1024
	content, err := readFileWorkspace(root, rel, maxBytes, 0, 0)
	if err != nil {
		return "", err
	}
	patched, err := applyHunks(content, hunks)
	if err != nil {
		return "", err
	}
	if err := writeFileWorkspace(root, rel, patched, "overwrite"); err != nil {
		return "", err
	}
	added, removed := countPatchStats(hunks)
	return fmt.Sprintf("patched %s: %d hunks applied, %d lines added, %d lines removed", rel, len(hunks), added, removed), nil
}

func countPatchStats(hunks []diffHunk) (added, removed int) {
	for _, h := range hunks {
		for _, l := range h.Lines {
			switch l.Op {
			case '+':
				added++
			case '-':
				removed++
			}
		}
	}
	return added, removed
}
