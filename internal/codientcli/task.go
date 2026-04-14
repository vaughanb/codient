package codientcli

import (
	"fmt"
	"os"
	"strings"
)

const maxTaskFileBytes = 32 * 1024

// buildTaskDirective returns markdown sections Objective / Constraints / Definition of done.
// goal and/or taskFilePath may be empty; if both empty, returns "".
func buildTaskDirective(goal, taskFilePath string) (string, error) {
	goal = strings.TrimSpace(goal)
	taskFilePath = strings.TrimSpace(taskFilePath)
	var fileBody string
	if taskFilePath != "" {
		data, err := os.ReadFile(taskFilePath)
		if err != nil {
			return "", fmt.Errorf("task file: %w", err)
		}
		if len(data) > maxTaskFileBytes {
			data = append(data[:maxTaskFileBytes], []byte("\n\n[truncated]\n")...)
		}
		fileBody = strings.TrimSpace(string(data))
	}
	if goal == "" && fileBody == "" {
		return "", nil
	}
	var b strings.Builder
	b.WriteString("## Task directive\n\n")
	b.WriteString("### Objective\n\n")
	if goal != "" {
		b.WriteString(goal)
		b.WriteString("\n\n")
	}
	if fileBody != "" {
		b.WriteString(fileBody)
		b.WriteString("\n\n")
	}
	b.WriteString("### Constraints\n\n")
	b.WriteString("(none specified)\n\n")
	b.WriteString("### Definition of done\n\n")
	b.WriteString("(none specified)\n")
	return strings.TrimSpace(b.String()), nil
}

// prefixUserWithTask prepends the task directive to the user message when directive is non-empty.
func prefixUserWithTask(user, directive string) string {
	user = strings.TrimSpace(user)
	if directive == "" {
		return user
	}
	if user == "" {
		return directive
	}
	return directive + "\n\n---\n\n" + user
}

// applyTaskToFirstTurnIfNeeded prepends the task block only when there is no prior conversation (REPL or one-shot).
func applyTaskToFirstTurnIfNeeded(priorTurns int, user, goal, taskFilePath string) (string, error) {
	if priorTurns > 0 {
		return strings.TrimSpace(user), nil
	}
	dir, err := buildTaskDirective(goal, taskFilePath)
	if err != nil {
		return "", err
	}
	return prefixUserWithTask(user, dir), nil
}
