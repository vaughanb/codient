package assistout

import (
	"regexp"
	"strings"
)

var (
	reMarkdownQuestionHeading = regexp.MustCompile(`(?mi)^#{1,6}\s*question\b`)
	// Mid-line packed options: "A) … B) …" or "**A)** … **B)** …"
	rePlanPackedBoldOption  = regexp.MustCompile(`([^\n])\s+(\*\*[BCD]\)\*\*)`)
	rePlanPackedPlainOption = regexp.MustCompile(`([^\n])\s+([BCD]\))(\s)`)
	// Model mistake: "## B) …" renders as huge blue headings in the terminal.
	rePlanLineOptionHeading = regexp.MustCompile(`^(\s*)#{1,6}\s+([ABCD]\)\s.*)$`)
	rePlanLinePlainOption   = regexp.MustCompile(`^(\s*)([ABCD]\)\s.*)$`)
	rePlanLineBoldOption    = regexp.MustCompile(`^(\s*)(\*\*[ABCD]\)\*\*\s*.+)$`)
	rePlanLineListedOption  = regexp.MustCompile(`^(\s*)[-*•]\s*((?:\*\*)?[ABCD]\)(?:\*\*)?\s.*)$`)
	rePlanLineEmptyMarker   = regexp.MustCompile(`^\s*[-*•]\s*$`)
)

const (
	planWaitBoldLower  = "**waiting for your answer**"
	planWaitPlainLower = "waiting for your answer"
)

// findPlanWaitBounds returns the byte range in s of the blocking wait phrase.
// Accepts markdown-bold (**…**) or plain text—models often omit the asterisks.
func findPlanWaitBounds(s string) (start, end int, ok bool) {
	lower := strings.ToLower(s)
	if i := strings.Index(lower, planWaitBoldLower); i >= 0 {
		return i, i + len(planWaitBoldLower), true
	}
	if i := strings.Index(lower, planWaitPlainLower); i >= 0 {
		return i, i + len(planWaitPlainLower), true
	}
	return 0, 0, false
}

// ReplySignalsPlanWait is true when the assistant used the blocking wait line
// (REPL should show the Answer prompt, not an optional follow-up prompt).
func ReplySignalsPlanWait(s string) bool {
	_, _, ok := findPlanWaitBounds(s)
	return ok
}

// InsertPlanQuestionHeading inserts a markdown "## Question" heading before the
// block that ends with the plan wait line, when that phrase is present and no
// Question heading already exists. Plan mode only (caller should gate).
func InsertPlanQuestionHeading(s string) string {
	if s == "" {
		return s
	}
	idx, _, ok := findPlanWaitBounds(s)
	if !ok {
		return s
	}
	before := s[:idx]
	if reMarkdownQuestionHeading.MatchString(before) {
		return s
	}
	trimmed := strings.TrimRight(before, " \t\r\n")
	lastBreak := strings.LastIndex(trimmed, "\n\n")
	insertAt := 0
	if lastBreak >= 0 {
		insertAt = lastBreak + 2
	}
	return s[:insertAt] + "## Question\n\n" + s[insertAt:]
}

// NormalizePlanQuestionOptionLines breaks B/C/D options onto separate lines when the
// model packed them on one line before the wait phrase. No-op when the wait phrase
// is absent. Plan mode only (caller should gate).
func NormalizePlanQuestionOptionLines(s string) string {
	if s == "" {
		return s
	}
	idx, _, ok := findPlanWaitBounds(s)
	if !ok {
		return s
	}
	prefix, suffix := s[:idx], s[idx:]
	// Bold **B)** / **C)** / **D)** first so plain regex does not touch inside **…**
	for {
		next := rePlanPackedBoldOption.ReplaceAllString(prefix, "$1\n\n$2")
		if next == prefix {
			break
		}
		prefix = next
	}
	for {
		next := rePlanPackedPlainOption.ReplaceAllString(prefix, "$1\n\n$2$3")
		if next == prefix {
			break
		}
		prefix = next
	}
	prefix = sanitizePlanQuestionListMarkdown(prefix)
	return prefix + suffix
}

// sanitizePlanQuestionListMarkdown fixes common model markdown mistakes in the
// question block: empty bullet lines, ATX headings before B)/C)/D), and bare
// A–D option lines so glamour renders a consistent list.
func sanitizePlanQuestionListMarkdown(prefix string) string {
	lines := strings.Split(prefix, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			out = append(out, line)
			continue
		}
		if rePlanLineEmptyMarker.MatchString(line) {
			continue
		}
		if rePlanLineListedOption.MatchString(line) {
			out = append(out, line)
			continue
		}
		if m := rePlanLineOptionHeading.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"- "+strings.TrimSpace(m[2]))
			continue
		}
		if m := rePlanLineBoldOption.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"- "+strings.TrimSpace(m[2]))
			continue
		}
		if m := rePlanLinePlainOption.FindStringSubmatch(line); m != nil {
			out = append(out, m[1]+"- "+strings.TrimSpace(m[2]))
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
