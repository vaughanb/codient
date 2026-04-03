package tools

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const defaultGrepMax = 50
const maxGrepMax = 200

var errGrepLimit = errors.New("grep match limit reached")

func grepWorkspace(ctx context.Context, root, pathPrefix, pattern string, literal bool, glob string, maxMatches int) (string, error) {
	if maxMatches <= 0 {
		maxMatches = defaultGrepMax
	}
	if maxMatches > maxGrepMax {
		maxMatches = maxGrepMax
	}
	pat := strings.TrimSpace(pattern)
	if pat == "" {
		return "", fmt.Errorf("pattern is required")
	}
	searchRoot, err := absUnderRoot(root, strings.TrimSpace(pathPrefix))
	if err != nil {
		return "", err
	}
	if rgPath, e := exec.LookPath("rg"); e == nil {
		return grepRipgrep(ctx, rgPath, searchRoot, pat, literal, glob, maxMatches)
	}
	return grepStdlib(searchRoot, pat, literal, glob, maxMatches)
}

func grepRipgrep(ctx context.Context, rgPath, searchRoot, pattern string, literal bool, glob string, maxMatches int) (string, error) {
	args := []string{
		"--line-number",
		"--max-count", strconv.Itoa(maxMatches),
		"--max-columns", "400",
	}
	if literal {
		args = append(args, "--fixed-strings")
	}
	if strings.TrimSpace(glob) != "" {
		args = append(args, "--glob", strings.TrimSpace(glob))
	}
	args = append(args, pattern, searchRoot)
	cmd := exec.CommandContext(ctx, rgPath, args...)
	out, err := cmd.CombinedOutput()
	s := string(out)
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 && strings.TrimSpace(s) == "" {
			return "(no matches)", nil
		}
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s), nil
		}
		return "", fmt.Errorf("rg: %w", err)
	}
	if strings.TrimSpace(s) == "" {
		return "(no matches)", nil
	}
	return strings.TrimSpace(s), nil
}

func grepStdlib(searchRoot, pattern string, literal bool, glob string, maxMatches int) (string, error) {
	var re *regexp.Regexp
	var err error
	if literal {
		re = regexp.MustCompile(regexp.QuoteMeta(pattern))
	} else {
		re, err = regexp.Compile(pattern)
		if err != nil {
			return "", fmt.Errorf("invalid regex: %w", err)
		}
	}
	globPat := strings.TrimSpace(glob)
	var matches []string
	walkErr := filepath.WalkDir(searchRoot, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if len(matches) >= maxMatches {
			return errGrepLimit
		}
		if d.IsDir() {
			return nil
		}
		if globPat != "" {
			base := filepath.Base(path)
			ok, _ := filepath.Match(globPat, base)
			if !ok {
				return nil
			}
		}
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 2*1024*1024 {
			return nil
		}
		rel, err := filepath.Rel(searchRoot, path)
		if err != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		sc := bufio.NewScanner(bytes.NewReader(data))
		lineNo := 0
		for sc.Scan() {
			if len(matches) >= maxMatches {
				return errGrepLimit
			}
			lineNo++
			line := sc.Text()
			if re.MatchString(line) {
				matches = append(matches, fmt.Sprintf("%s:%d:%s", rel, lineNo, line))
			}
		}
		return nil
	})
	if walkErr != nil && !errors.Is(walkErr, errGrepLimit) {
		return "", walkErr
	}
	if len(matches) == 0 {
		return "(no matches)", nil
	}
	var b strings.Builder
	for _, m := range matches {
		b.WriteString(m)
		b.WriteByte('\n')
	}
	out := strings.TrimSuffix(b.String(), "\n")
	if len(matches) >= maxMatches || errors.Is(walkErr, errGrepLimit) {
		out += fmt.Sprintf("\n\n[truncated at %d matches]\n", maxMatches)
	}
	return out, nil
}
