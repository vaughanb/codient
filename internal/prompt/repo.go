package prompt

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var repoInstructionFiles = []string{
	"AGENTS.md",
	filepath.Join(".codient", "instructions.md"),
}

// LoadRepoInstructions reads optional instruction files under workspace root (capped in total).
func LoadRepoInstructions(workspaceRoot string) (string, error) {
	root := strings.TrimSpace(workspaceRoot)
	if root == "" {
		return "", nil
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, rel := range repoInstructionFiles {
		p := filepath.Join(absRoot, rel)
		data, err := os.ReadFile(p)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return "", fmt.Errorf("read %s: %w", rel, err)
		}
		if len(data) > MaxRepoInstructionsBytes {
			data = append(data[:MaxRepoInstructionsBytes], []byte("\n\n[truncated]\n")...)
		}
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "### %s\n\n%s", rel, string(data))
		if b.Len() >= MaxRepoInstructionsBytes {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > MaxRepoInstructionsBytes {
		out = out[:MaxRepoInstructionsBytes] + "\n\n[truncated]\n"
	}
	return out, nil
}
