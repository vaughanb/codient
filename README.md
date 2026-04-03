# codient

**codient** is a command-line agent for [LM Studio](https://lmstudio.ai/) (or any OpenAI-compatible chat API). It runs multi-step tool use against your workspace—read and search files, run allowlisted commands, and optional write access in **agent** mode. **Ask** and **plan** modes use a read-only tool set and different system prompts: ask for exploration, plan for structured implementation plans with clarifying questions.

**Repository:** [github.com/vaughanb/codient](https://github.com/vaughanb/codient)

## Requirements

- [Go](https://go.dev/dl/) 1.26+ (see `go.mod`)
- A running LM Studio (or compatible) server exposing OpenAI-style `/v1/chat/completions` (default base URL `http://127.0.0.1:1234/v1`)

## Install

```bash
git clone https://github.com/vaughanb/codient.git
cd codient
go install ./cmd/codient
```

Or build with Make:

```bash
make install   # installs codient to $(go env GOPATH)/bin
# or
make build     # outputs ./bin/codient
```

## Configuration

Set at least the model id LM Studio exposes:

| Variable | Description |
|----------|-------------|
| `LMSTUDIO_MODEL` | **Required** for chat (e.g. `openai/gpt-oss-20b`) |
| `LMSTUDIO_BASE_URL` | API base (default `http://127.0.0.1:1234/v1`) |
| `LMSTUDIO_API_KEY` | Sent as `Authorization` bearer (default `lm-studio`) |
| `CODIENT_WORKSPACE` | Root for workspace tools (files, search, commands) |
| `CODIENT_MODE` | Default mode if `-mode` omitted: `agent`, `ask`, or `plan` |

See `internal/config/config.go` for tool limits, exec allowlist, and other options.

## Usage

```bash
# Ping the server
codient -ping

# List models and tools
codient -list-models
codient -list-tools

# One-shot prompt (stdin if -prompt empty)
codient -prompt "Summarize README.md"

# Multi-turn REPL (type exit to quit)
codient -repl -prompt "Help me understand the repo layout"

# Plan mode: interactive planning, read-only tools
codient -repl -mode plan -prompt "Plan a small Go CLI for managing todos"
```

Use `-help` for all flags. Notable options:

- **`-mode`** — `agent` (default), `ask`, or `plan`
- **`-repl`** — session history in one process (not persisted across runs)
- **`-log` / `CODIENT_LOG`** — append JSONL events (LLM rounds, tools)
- **`-goal` / `-task-file`** — merged into the first user turn as a task directive
- **`-plan-save-dir` / `CODIENT_PLAN_SAVE_DIR`** — where to save completed plans (default `<workspace>/.codient/plans`)
- **`CODIENT_PLAN_SAVE=0`** — disable writing plan files

### Plan mode and saved plans

In **plan** mode, when the assistant’s reply includes a **Ready to implement** section, codient saves the markdown under the workspace (by default `.codient/plans/`). Filenames are `{task-slug}_{date-time}_{nanoseconds}.md` so runs never collide. The task slug comes from `-goal`, else `-task-file` basename, else the first line of your first message.

### Streaming

Assistant text can stream to the terminal as it is generated (`-stream-reply`, default on for TTYs). In plan mode with styled markdown, the turn that produces the full plan after a blocking question is buffered once so the reply can be rendered with full markdown formatting.

## Development

```bash
make check    # vet + test
make test-integration   # optional; requires live API (see Makefile)
```

## License

No license file is set in this repository yet; add one if you intend to open-source under specific terms.
