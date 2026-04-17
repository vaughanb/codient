// Package hooks implements lifecycle hooks for the codient agent.
package hooks

// Event names match the codient hooks.json schema (aligned with Claude Code / Codex).
const (
	EventSessionStart     = "SessionStart"
	EventPreToolUse       = "PreToolUse"
	EventPostToolUse      = "PostToolUse"
	EventUserPromptSubmit = "UserPromptSubmit"
	EventStop             = "Stop"
	EventSessionEnd       = "SessionEnd"
)

// SessionStartSource is the matcher input for SessionStart hooks.
type SessionStartSource string

const (
	SessionStartup SessionStartSource = "startup"
	SessionResume  SessionStartSource = "resume"
)
