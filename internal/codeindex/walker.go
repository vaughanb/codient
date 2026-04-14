package codeindex

import (
	"bytes"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const (
	maxFileBytes  = 30 * 1024 // ~8K tokens
	maxFilesTotal = 10_000
)

type document struct {
	Path    string
	ModTime time.Time
	Text    string
}

var skipDirs = map[string]struct{}{
	".git": {}, "node_modules": {}, "__pycache__": {},
	".venv": {}, "venv": {}, ".tox": {},
	".next": {}, "dist": {}, ".cache": {},
	".idea": {}, ".vscode": {}, ".codient": {},
	"vendor": {}, ".bundle": {},
}

var skipExtensions = map[string]struct{}{
	".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {},
	".ico": {}, ".svg": {}, ".bmp": {}, ".tiff": {},
	".mp3": {}, ".mp4": {}, ".wav": {}, ".avi": {}, ".mov": {},
	".zip": {}, ".tar": {}, ".gz": {}, ".bz2": {}, ".xz": {}, ".7z": {}, ".rar": {},
	".exe": {}, ".dll": {}, ".so": {}, ".dylib": {}, ".o": {}, ".a": {},
	".wasm": {}, ".class": {}, ".pyc": {}, ".pyo": {},
	".pdf": {}, ".doc": {}, ".docx": {}, ".xls": {}, ".xlsx": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".eot": {},
	".db": {}, ".sqlite": {}, ".sqlite3": {},
	".jar": {}, ".war": {},
	".min.js": {}, ".min.css": {},
}

// walkWorkspace discovers text files in the workspace and produces documents.
// It uses `git ls-files` when available, falling back to filepath.WalkDir.
func walkWorkspace(root string) ([]document, error) {
	paths, err := gitListFiles(root)
	if err != nil {
		paths, err = walkDir(root)
		if err != nil {
			return nil, err
		}
	}

	if len(paths) > maxFilesTotal {
		paths = paths[:maxFilesTotal]
	}

	docs := make([]document, 0, len(paths))
	for _, rel := range paths {
		if skipByExtension(rel) {
			continue
		}
		abs := filepath.Join(root, rel)
		info, err := os.Stat(abs)
		if err != nil || info.IsDir() {
			continue
		}
		text, err := extractContent(abs, rel)
		if err != nil || text == "" {
			continue
		}
		docs = append(docs, document{
			Path:    rel,
			ModTime: info.ModTime(),
			Text:    text,
		})
	}
	return docs, nil
}

func gitListFiles(root string) ([]string, error) {
	cmd := exec.Command("git", "ls-files", "--cached", "--others", "--exclude-standard")
	cmd.Dir = root
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var paths []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			paths = append(paths, filepath.FromSlash(l))
		}
	}
	return paths, nil
}

func walkDir(root string) ([]string, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, skip := skipDirs[d.Name()]; skip {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		paths = append(paths, rel)
		if len(paths) >= maxFilesTotal {
			return filepath.SkipAll
		}
		return nil
	})
	return paths, err
}

func skipByExtension(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	if _, skip := skipExtensions[ext]; skip {
		return true
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.HasSuffix(base, ".min.js") || strings.HasSuffix(base, ".min.css") {
		return true
	}
	return false
}

// extractContent reads a file and produces an embedding document string.
// Returns empty string for binary files.
func extractContent(abs, rel string) (string, error) {
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if len(data) == 0 {
		return "", nil
	}

	// Binary detection: check first 8KB for null bytes.
	sample := data
	if len(sample) > 8192 {
		sample = sample[:8192]
	}
	if bytes.ContainsRune(sample, 0) {
		return "", nil
	}

	if len(data) > maxFileBytes {
		data = data[:maxFileBytes]
	}

	var b strings.Builder
	b.WriteString("File: ")
	b.WriteString(filepath.ToSlash(rel))
	b.WriteByte('\n')
	b.Write(data)
	return b.String(), nil
}
