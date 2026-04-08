package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// Bracketed paste mode escape sequences.
// When enabled, the terminal wraps pasted text in start/end markers so we can
// collect a multi-line paste as a single logical input.
const (
	bracketPasteStart = "\x1b[200~"
	bracketPasteEnd   = "\x1b[201~"
	bracketPasteOn    = "\x1b[?2004h"
	bracketPasteOff   = "\x1b[?2004l"
)

func enableBracketedPaste() {
	if stdinIsInteractive() {
		fmt.Fprint(os.Stderr, bracketPasteOn)
	}
}

func disableBracketedPaste() {
	if stdinIsInteractive() {
		fmt.Fprint(os.Stderr, bracketPasteOff)
	}
}

// readUserInput reads one logical user input from the scanner, handling:
//   - Bracketed paste: all lines between terminal paste markers are joined.
//   - Backslash continuation: a trailing \ causes the next line to be appended.
//
// Returns the trimmed input and whether the read succeeded (false on EOF).
func readUserInput(sc *bufio.Scanner) (string, bool) {
	if !sc.Scan() {
		return "", false
	}
	line := sc.Text()

	if strings.Contains(line, bracketPasteStart) {
		return collectBracketedPaste(sc, line)
	}

	text := strings.TrimSpace(line)

	if strings.HasSuffix(text, `\`) {
		return collectContinuation(sc, text)
	}

	return text, true
}

// collectBracketedPaste accumulates all lines between the paste start and end
// markers into a single string.
func collectBracketedPaste(sc *bufio.Scanner, firstLine string) (string, bool) {
	firstLine = strings.Replace(firstLine, bracketPasteStart, "", 1)

	if idx := strings.Index(firstLine, bracketPasteEnd); idx >= 0 {
		return strings.TrimSpace(firstLine[:idx]), true
	}

	var lines []string
	lines = append(lines, firstLine)

	for sc.Scan() {
		line := sc.Text()
		if idx := strings.Index(line, bracketPasteEnd); idx >= 0 {
			if s := line[:idx]; s != "" {
				lines = append(lines, s)
			}
			break
		}
		lines = append(lines, line)
	}

	return strings.TrimSpace(strings.Join(lines, "\n")), true
}

// collectContinuation reads additional lines while the current line ends with \.
func collectContinuation(sc *bufio.Scanner, firstLine string) (string, bool) {
	var lines []string
	lines = append(lines, strings.TrimSuffix(firstLine, `\`))

	for {
		fmt.Fprint(os.Stderr, "  > ")
		if !sc.Scan() {
			break
		}
		next := strings.TrimSpace(sc.Text())
		if strings.HasSuffix(next, `\`) {
			lines = append(lines, strings.TrimSuffix(next, `\`))
			continue
		}
		lines = append(lines, next)
		break
	}

	return strings.TrimSpace(strings.Join(lines, "\n")), true
}
