package tools

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	defaultReadMaxBytes   = 256 * 1024
	defaultListMaxDepth   = 2
	defaultListMaxEntries = 200
	defaultSearchMax      = 200
)

var errHitLimit = errors.New("hit listing or search limit")

// skipDirs is the set of directory names filtered from listings, searches, and grep.
var skipDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "__pycache__": {},
	".venv": {}, "venv": {}, ".tox": {},
	".next": {}, "dist": {}, ".cache": {},
	".idea": {}, ".vscode": {}, ".codient": {},
}

func shouldSkipDir(name string) bool {
	_, ok := skipDirs[name]
	return ok
}

// absUnderRoot resolves rel inside root and ensures the result stays under root.
func absUnderRoot(root, rel string) (abs string, err error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	joined := filepath.Join(absRoot, filepath.Clean(rel))
	absFull, err := filepath.Abs(joined)
	if err != nil {
		return "", err
	}
	relPath, err := filepath.Rel(absRoot, absFull)
	if err != nil || strings.HasPrefix(relPath, "..") {
		return "", fmt.Errorf("path escapes workspace root")
	}
	return absFull, nil
}

func readFileWorkspace(root, rel string, maxBytes int64, startLine, endLine int) (string, error) {
	if maxBytes <= 0 {
		maxBytes = defaultReadMaxBytes
	}
	abs, err := absUnderRoot(root, rel)
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if int64(len(b)) > maxBytes {
		b = b[:maxBytes]
		trunc := "\n\n[truncated: exceeded max_bytes]"
		if int64(len(b)+len(trunc)) > maxBytes {
			b = b[:max(0, int(maxBytes)-len(trunc))]
		}
		b = append(b, trunc...)
	}
	if !utf8.Valid(b) {
		return "", fmt.Errorf("file is not valid UTF-8")
	}
	s := string(b)
	if startLine > 0 || endLine > 0 {
		lines := strings.Split(s, "\n")
		n := len(lines)
		start := 1
		end := n
		if startLine > 0 {
			start = startLine
		}
		if endLine > 0 {
			end = endLine
		}
		if start < 1 {
			start = 1
		}
		if end > n {
			end = n
		}
		if start > end {
			return "", fmt.Errorf("start_line (%d) after end_line (%d)", startLine, endLine)
		}
		var b strings.Builder
		for i := start - 1; i < end; i++ {
			if i >= 0 && i < n {
				b.WriteString(lines[i])
				if i < end-1 {
					b.WriteByte('\n')
				}
			}
		}
		s = fmt.Sprintf("(lines %d-%d of file)\n%s", start, end, b.String())
	}
	return s, nil
}

func listDirWorkspace(root, rel string, maxDepth, maxEntries int) (string, error) {
	if maxDepth < 0 {
		maxDepth = defaultListMaxDepth
	}
	if maxEntries <= 0 {
		maxEntries = defaultListMaxEntries
	}
	abs, err := absUnderRoot(root, rel)
	if err != nil {
		return "", err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("not a directory: %s", rel)
	}
	type line struct {
		rel string
		dir bool
	}
	var out []line
	var walk func(dirAbs string, depth int) error
	walk = func(dirAbs string, depth int) error {
		if len(out) >= maxEntries {
			return errHitLimit
		}
		entries, err := os.ReadDir(dirAbs)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if len(out) >= maxEntries {
				return errHitLimit
			}
			if e.IsDir() && shouldSkipDir(e.Name()) {
				continue
			}
			full := filepath.Join(dirAbs, e.Name())
			rp, err := filepath.Rel(abs, full)
			if err != nil {
				continue
			}
			rp = filepath.ToSlash(rp)
			isDir := e.IsDir()
			out = append(out, line{rel: rp, dir: isDir})
			if isDir && depth < maxDepth {
				if err := walk(full, depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(abs, 0); err != nil && !errors.Is(err, errHitLimit) {
		return "", err
	}
	var b strings.Builder
	for _, l := range out {
		suffix := ""
		if l.dir {
			suffix = "/"
		}
		fmt.Fprintf(&b, "%s%s\n", l.rel, suffix)
	}
	if len(out) >= maxEntries {
		fmt.Fprintf(&b, "\n[truncated: max_entries=%d]\n", maxEntries)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func searchFilesWorkspace(root string, under, substring, suffix string, maxResults int) (string, error) {
	if maxResults <= 0 {
		maxResults = defaultSearchMax
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	searchRoot := absRoot
	if strings.TrimSpace(under) != "" {
		searchRoot, err = absUnderRoot(root, under)
		if err != nil {
			return "", err
		}
		fi, err := os.Stat(searchRoot)
		if err != nil {
			return "", err
		}
		if !fi.IsDir() {
			return "", fmt.Errorf("search under is not a directory: %s", under)
		}
	}
	sub := strings.TrimSpace(substring)
	suf := strings.TrimSpace(suffix)
	if sub == "" && suf == "" {
		return "", fmt.Errorf("provide substring and/or suffix")
	}
	var matches []string
	err = filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if len(matches) >= maxResults {
			return errHitLimit
		}
		if d.IsDir() {
			if shouldSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		rel, relErr := filepath.Rel(absRoot, path)
		if relErr != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		base := filepath.Base(path)
		if sub != "" && !strings.Contains(relSlash, sub) && !strings.Contains(base, sub) {
			return nil
		}
		if suf != "" && !strings.HasSuffix(base, suf) {
			return nil
		}
		matches = append(matches, relSlash)
		return nil
	})
	if err != nil && !errors.Is(err, errHitLimit) {
		return "", err
	}
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	if len(matches) >= maxResults {
		fmt.Fprintf(&b, "\n[truncated: max_results=%d]\n", maxResults)
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

func strReplaceWorkspace(root, rel, oldStr, newStr string, replaceAll bool) (string, error) {
	abs, err := absUnderRoot(root, rel)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in %s; verify exact whitespace/newlines match the file, or use insert_lines to append content", rel)
	}
	if count > 1 && !replaceAll {
		return "", fmt.Errorf("old_string has %d matches in %s; use replace_all or provide more context to make it unique", count, rel)
	}
	if replaceAll {
		content = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		content = strings.Replace(content, oldStr, newStr, 1)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		return "", err
	}
	verb := "replaced 1 occurrence"
	if replaceAll {
		verb = fmt.Sprintf("replaced %d occurrences", count)
	}
	return fmt.Sprintf("%s in %s", verb, rel), nil
}

func writeFileWorkspace(root, rel, content, mode string) error {
	abs, err := absUnderRoot(root, rel)
	if err != nil {
		return err
	}
	switch mode {
	case "", "overwrite":
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		return os.WriteFile(abs, []byte(content), 0o644)
	case "create":
		if _, err := os.Stat(abs); err == nil {
			return fmt.Errorf("file already exists (use mode overwrite)")
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			return err
		}
		return os.WriteFile(abs, []byte(content), 0o644)
	default:
		return fmt.Errorf("invalid mode %q (use create or overwrite)", mode)
	}
}

// ensureDirWorkspace creates a directory (and any parents) under root using os.MkdirAll.
// rel is a path relative to the workspace; works the same on all operating systems.
func ensureDirWorkspace(root, rel string) error {
	abs, err := absUnderRoot(root, rel)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, 0o755)
}

func insertLinesWorkspace(root, rel, content, position string, afterLine int) (string, error) {
	abs, err := absUnderRoot(root, rel)
	if err != nil {
		return "", err
	}
	if content == "" {
		return "", fmt.Errorf("content is empty; insert_lines requires non-empty content")
	}

	var existing []byte
	if data, err := os.ReadFile(abs); err == nil {
		existing = data
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	if len(existing) > 0 && !utf8.Valid(existing) {
		return "", fmt.Errorf("file is not valid UTF-8")
	}

	fileText := string(existing)
	insertText := content

	// Determine insertion point.
	if afterLine > 0 {
		lines := strings.Split(fileText, "\n")
		idx := afterLine
		if idx > len(lines) {
			idx = len(lines)
		}
		// Ensure the text before the insertion ends with a newline.
		before := strings.Join(lines[:idx], "\n")
		if before != "" && !strings.HasSuffix(before, "\n") {
			before += "\n"
		}
		after := strings.Join(lines[idx:], "\n")
		if !strings.HasSuffix(insertText, "\n") && after != "" {
			insertText += "\n"
		}
		result := before + insertText + after
		if err := os.WriteFile(abs, []byte(result), 0o644); err != nil {
			return "", err
		}
		inserted := strings.Count(content, "\n") + 1
		return fmt.Sprintf("inserted %d lines in %s after line %d", inserted, rel, afterLine), nil
	}

	switch strings.ToLower(strings.TrimSpace(position)) {
	case "beginning":
		if !strings.HasSuffix(insertText, "\n") && fileText != "" {
			insertText += "\n"
		}
		result := insertText + fileText
		if err := os.WriteFile(abs, []byte(result), 0o644); err != nil {
			return "", err
		}
		inserted := strings.Count(content, "\n") + 1
		return fmt.Sprintf("inserted %d lines at beginning of %s", inserted, rel), nil

	default: // "end" or empty
		if fileText != "" && !strings.HasSuffix(fileText, "\n") {
			fileText += "\n"
		}
		result := fileText + insertText
		if err := os.WriteFile(abs, []byte(result), 0o644); err != nil {
			return "", err
		}
		inserted := strings.Count(content, "\n") + 1
		return fmt.Sprintf("inserted %d lines at end of %s", inserted, rel), nil
	}
}
