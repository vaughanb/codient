# codient

[![CI](https://github.com/vaughanb/codient/actions/workflows/ci.yml/badge.svg)](https://github.com/vaughanb/codient/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/License-MIT-informational)](https://github.com/vaughanb/codient/blob/main/LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/vaughanb/codient/main?label=Go&logo=go)](https://github.com/vaughanb/codient/blob/main/go.mod)
[![Latest release](https://img.shields.io/github/v/release/vaughanb/codient?label=release&logo=github)](https://github.com/vaughanb/codient/releases/latest)

**codient** is a command-line agent for any **OpenAI-compatible** chat API (local server, cloud provider, etc.). It runs multi-step tool use against your workspace—read and search files, run allowlisted commands, optional HTTPS fetch and web search, and write access in **build** mode. **Ask** and **plan** modes use a read-only tool set and different system prompts: ask for exploration, plan for structured implementation plans with clarifying questions.

When the API returns usage metadata, codient aggregates **prompt and completion tokens** per session and shows **estimated cost** using a built-in pricing table or your `cost_per_mtok` override. See [Token usage and cost estimates](#token-usage-and-cost-estimates).

**Repository:** [github.com/vaughanb/codient](https://github.com/vaughanb/codient)

## Requirements

- [Go](https://go.dev/dl/) 1.26+ (see `go.mod`)
- A running server exposing OpenAI-style `/v1/chat/completions` (default base URL `http://127.0.0.1:1234/v1`; typical for local stacks)

**Optional:**

- [ast-grep](https://ast-grep.github.io/) — for the `find_references` structural code search tool. Codient auto-detects or offers to download it on first interactive session.
- [Git](https://git-scm.com/) — required for undo, auto-commit, and diff features in a workspace that is a git repository.
- [GitHub CLI](https://cli.github.com/) (`gh`) — optional; required for `/pr` and the `create_pull_request` tool (push + open a PR).

## Install

**macOS / Linux:**

```bash
curl -sSfL https://raw.githubusercontent.com/vaughanb/codient/main/scripts/install.sh | sh
```

**Windows (PowerShell):**

```powershell
irm https://raw.githubusercontent.com/vaughanb/codient/main/scripts/install.ps1 | iex
```

Both scripts detect your OS and architecture, download the latest release binary, and place it on your PATH. Set `CODIENT_INSTALL_DIR` to override the install location (defaults to `~/.local/bin` on Unix, `%LOCALAPPDATA%\codient` on Windows).

**From source** (requires [Go](https://go.dev/dl/) 1.26+):

```bash
go install github.com/vaughanb/codient/cmd/codient@latest
```

Or clone and build with Make:

```bash
git clone https://github.com/vaughanb/codient.git
cd codient
make install   # installs codient to $(go env GOPATH)/bin
```

## Configuration

Most settings are stored in `~/.codient/config.json` (unless `CODIENT_STATE_DIR` points elsewhere) and managed via `/config` and `/setup` inside a session. A few environment variables override defaults (see below); everything else is config file or CLI flags.

### First run

Start codient and set your model (and optionally base URL / API key):

```
codient
/config model gpt-4o-mini
/config base_url http://127.0.0.1:1234/v1
/config api_key your-key-here
```

Or run `/setup` for a guided wizard (connection and model selection).

### Config file reference (`~/.codient/config.json`)

All settings live in a single JSON file. Use `/config` to view and edit, or edit the file directly. Omitted fields use built-in defaults. CLI flags (e.g. `-mode`, `-plain`, `-workspace`) override config file values when explicitly passed.

**Example config.json:**

```json
{
  "base_url": "http://127.0.0.1:1234/v1",
  "api_key": "codient",
  "model": "qwen3-coder",
  "mode": "build",
  "search_url": "http://localhost:8888",
  "fetch_allow_hosts": "docs.go.dev,pkg.go.dev",
  "autocheck_cmd": "go build ./...",
  "verbose": true
}
```

**Per-mode models and endpoints** — Under `models`, you can override `base_url`, `api_key`, and `model` for `plan`, `build`, and `ask`. Any field left out inherits from the top-level connection. Use this for a remote planning API and a local implementation server, for example:

```json
{
  "base_url": "http://127.0.0.1:1234/v1",
  "api_key": "codient",
  "model": "qwen3-coder-30b",
  "models": {
    "plan": {
      "base_url": "https://api.openai.com/v1",
      "api_key": "sk-...",
      "model": "gpt-4.1"
    }
  }
}
```

The interactive `/setup` wizard can also configure a separate plan server after you pick the default model. Slash commands `/config plan_base_url`, `plan_api_key`, `plan_model` (and `build_*`, `ask_*`) mirror these fields.

### `/config` reference

Run `/config` with no arguments to see all current values. `/config <key>` shows one value. `/config <key> <value>` sets and persists.

| Key | Description | Default |
|-----|-------------|---------|
| **Connection** | | |
| `base_url` | API base URL including `/v1` | `http://127.0.0.1:1234/v1` |
| `api_key` | Sent as `Authorization` bearer token | `codient` |
| `model` | Default model id (used by modes that have no override) | *(none — must be set for typical use)* |
| `plan_model`, `build_model`, `ask_model` | Model id for that mode only | inherit `model` |
| `plan_base_url`, `build_base_url`, `ask_base_url` | API base URL for that mode | inherit `base_url` |
| `plan_api_key`, `build_api_key`, `ask_api_key` | API key for that mode | inherit `api_key` |
| **Defaults** | | |
| `mode` | Default mode: `build`, `ask`, or `plan` | `build` |
| `workspace` | Root for workspace tools | *(process working directory)* |
| **Agent limits** | | |
| `max_concurrent` | Max concurrent in-flight completion requests | `3` |
| **Exec** | | |
| `exec_allowlist` | Comma-separated command names allowed as `argv[0]` | `go,git,cmd` (or `sh`) |
| `exec_timeout_sec` | Per-command timeout (max 3600) | `120` |
| `exec_max_output_bytes` | Cap on combined stdout+stderr (max 10 MiB) | `262144` |
| **Context** | | |
| `context_window` | Model context window in tokens (`0` = probe server at startup; shown on the welcome banner as **Context**) | `0` |
| `context_reserve` | Tokens reserved for the assistant reply | `4096` |
| **LLM** | | |
| `max_llm_retries` | Retries for transient LLM errors | `2` |
| `max_completion_seconds` | Per-completion timeout (max 3600) | `300` |
| `stream_with_tools` | Stream completions when tools are enabled | `false` |
| **Fetch** | | |
| `fetch_allow_hosts` | Comma-separated hostnames for `fetch_url` (subdomains match) | *(empty)* |
| `fetch_preapproved` | Built-in documentation-domain preset for `fetch_url` | `true` |
| `fetch_max_bytes` | Max response body bytes (max 10 MiB) | `1048576` |
| `fetch_timeout_sec` | Per-fetch timeout (max 300) | `30` |
| `fetch_web_rate_per_sec` | Token-bucket rate limit for `fetch_url` and `web_search` combined (`0` = off, max 100) | `0` |
| `fetch_web_rate_burst` | Burst size for that limiter (`0` with a positive rate defaults to the rate; max 50) | `0` |
| **Search** | | |
| `search_max_results` | Results per `web_search` query (max 10) | `5` |
| **Auto** | | |
| `autocompact_threshold` | Context usage % that triggers compaction (0 disables) | `75` |
| `autocheck_cmd` | Shell command after file edits (empty = auto-detect, `off` = disable) | *(auto)* |
| **Git (build mode)** | | |
| `git_auto_commit` | After each build turn that changes files, commit with message `codient: turn N` (set `false` for legacy file-restore `/undo` without commits) | `true` |
| `git_protected_branches` | Comma-separated branch names; when the first change lands on one of these, codient creates `codient/<task-slug>` and commits there | `main,master,develop` |
| `checkpoint_auto` | Automatic checkpoints: **`plan`** (after each completed plan phase group), **`all`** (after each build turn that changes files and commits), **`off`** (manual `/checkpoint` only) | `plan` |
| **UI/Output** | | |
| `plain` | Raw assistant text (no markdown/ANSI) | `false` |
| `quiet` | Suppress the welcome banner | `false` |
| `verbose` | Extra session diagnostics | `false` |
| `log` | Default JSONL log path | *(empty)* |
| `stream_reply` | Stream assistant tokens to stdout | `true` |
| `progress` | Force progress output on stderr | `false` |
| **Plan** | | |
| `design_save_dir` | Override directory for saved plans | `<workspace>/.codient/plans` |
| `design_save` | Save plan-mode plans to disk | `true` |
| **Project** | | |
| `project_context` | `off` to skip auto-injected project hints | *(empty)* |
| **Tools** | | |
| `ast_grep` | ast-grep binary path: `auto` (default), explicit path, or `off` to disable | *(auto)* |
| `embedding_model` | Model id for `/v1/embeddings` (same base URL as chat). Enables the `semantic_search` tool; leave empty to disable. The welcome banner shows **Embeddings** (model id, or `off`) | *(empty)* |
| `hooks_enabled` | Enable [lifecycle hooks](#lifecycle-hooks) (`hooks.json` under `~/.codient` and `<workspace>/.codient`) | `false` |
| `cost_per_mtok` | Optional `{"input":N,"output":N}` USD per 1M tokens — overrides built-in pricing for `/cost` and session cost estimates | *(built-in table)* |
| **Update** | | |
| `update_notify` | Show interactive update prompt on REPL startup | `true` |
| **MCP** | | |
| `mcp_servers` | Map of MCP server IDs to connection configs (see [MCP servers](#mcp-model-context-protocol-servers)) | *(empty)* |

### Token usage and cost estimates

Codient records **API-reported** token counts from chat completions when the server includes a `usage` object (OpenAI-compatible). Many local inference stacks omit this; cloud APIs usually populate it. Totals are **per REPL session** and include agent turns, `/compact`, the ask-mode verification gate, and **`delegate_task`** sub-agents.

- **`/cost`** (alias **`/tokens`**) — prompt, completion, and total tokens plus an estimated dollar amount when pricing is known.
- **`/status`** — session token totals and estimated cost when available.
- **Progress output** (`-progress` or default TTY stderr) — appends token counts to each completed model round when usage is present.
- **`-log` JSONL** — each `type: "llm"` line may include `prompt_tokens`, `completion_tokens`, and `total_tokens`.

**Pricing:** By default, codient matches your configured **`model`** id against a small built-in table (USD per million input/output tokens). Set **`cost_per_mtok`** in `config.json` to override, for example `{"input": 2.5, "output": 10}`, or in the REPL: **`/config cost_per_mtok 2.5 10`**. Use **`/config cost_per_mtok off`** to clear the override. Estimates are indicative only; use your provider’s billing for authoritative costs.

When `fetch_url` receives `Content-Type: text/html`, the body is converted to simplified markdown (headings, links, lists, code) before being returned.

### Environment variables

| Variable | Description |
|----------|-------------|
| `CODIENT_STATE_DIR` | Directory for `config.json` and related state instead of `~/.codient`. |

Run `codient -version` to print the binary version.

Test infrastructure variables (`CODIENT_INTEGRATION*`) are used by the test suite but are not user configuration.

For defaults and validation details, see [`internal/config/config.go`](internal/config/config.go).

### Web search

The `web_search` tool is always enabled. It uses an embedded metasearch engine ([searchmux](https://github.com/vaughanb/searchmux)) that fans out queries to multiple backends (Google, DuckDuckGo, StackOverflow, GitHub, pkg.go.dev, npm, PyPI, Hacker News, Wikipedia) in parallel, merges and deduplicates results, and returns a ranked list. No external server or Docker container is required.

### Semantic code search

When **`embedding_model`** is set in config, codient indexes text files in the workspace and registers the **`semantic_search`** tool (all modes). The agent can find files by meaning (e.g. “authentication”, “migrations”) instead of relying only on exact-string `grep`.

- **API:** Embeddings use the same **`base_url`** and **`api_key`** as chat completions (`POST /v1/embeddings`). Your server must expose that endpoint for the chosen model (e.g. OpenAI `text-embedding-3-small`, or a local embedding model in LM Studio / Ollama).
- **When indexing runs:** After you start an interactive session, indexing begins automatically in the background—no separate command. stderr shows progress and completion (or an error if embeddings fail).
- **Persistence:** The index is stored under **`<workspace>/.codient/index/embeddings.gob`**. On later sessions, unchanged files reuse cached vectors; only new or modified files are re-embedded. If you change **`embedding_model`**, the stored index is invalidated and rebuilt.
- **Configure:** `/config embedding_model <model-id>`, set `embedding_model` in `~/.codient/config.json`, or use **`/setup`** (optional prompt after chat model selection).

### MCP (Model Context Protocol) servers

Codient can connect to external **MCP servers** and expose their tools to the agent alongside built-in tools. This lets you extend the agent with any MCP-compatible tool provider (databases, APIs, custom workflows, etc.).

Configure MCP servers in `~/.codient/config.json` under the `mcp_servers` key. Each entry is a server ID mapped to its connection config:

```json
{
  "mcp_servers": {
    "filesystem": {
      "command": "npx",
      "args": ["-y", "@modelcontextprotocol/server-filesystem", "/home/user"],
      "env": {}
    },
    "remote-api": {
      "url": "https://api.example.com/mcp",
      "headers": {"Authorization": "Bearer sk-xxx"}
    }
  }
}
```

**Transport types:**

- **stdio** (`command` + `args`): Spawns the server as a subprocess and communicates over stdin/stdout. Use for local MCP servers (e.g. `npx @modelcontextprotocol/server-filesystem`). Optional `env` passes environment variables to the process.
- **Streamable HTTP** (`url`): Connects to a remote MCP server endpoint. Optional `headers` are sent with each request (useful for auth tokens).

**How it works:**

- On session start, codient connects to all configured servers and discovers their tools via `tools/list`.
- MCP tools are registered in the tool registry with namespaced names: `mcp__<serverID>__<toolName>` (e.g. `mcp__filesystem__read_dir`). This prevents collisions with built-in tools.
- The agent calls MCP tools the same way as built-in tools — no special handling needed.
- If a server fails to connect, a warning is printed and the session continues without that server's tools.
- Use the `/mcp` slash command to inspect connected servers and their tools.

### Lifecycle hooks

Codient can run **shell commands** at specific points in the agent lifecycle (similar to hooks in Claude Code, Cursor, and OpenAI Codex CLI). Hooks are **opt-in**: set **`hooks_enabled`** to **`true`** in `~/.codient/config.json` or via **`/config hooks_enabled true`**.

**Discovery:** Both of these files are loaded and merged (all matching hook groups run):

- `~/.codient/hooks.json` (or `$CODIENT_STATE_DIR/hooks.json`)
- `<workspace>/.codient/hooks.json`

**Schema** (nested event → matcher → handlers):

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "run_command|run_shell",
        "hooks": [
          {
            "type": "command",
            "command": "python3 .codient/hooks/check.py",
            "timeout": 30
          }
        ]
      }
    ]
  }
}
```

- **`matcher`** is a regular expression (Go RE2). Empty or omitted matches all tools for tool events; **`SessionStart`** matches against `source` (`startup` or `resume`).
- **`type`** is **`command`** only (phase 1). The command receives **JSON on stdin** (session id, cwd, hook name, model, `turn_id`, plus event-specific fields).
- **`timeout`** is in seconds (default 600 if omitted). **`failClosed`**: when `true`, a crash, timeout, or invalid JSON from that handler is treated as a failure (blocking for `PreToolUse` / `UserPromptSubmit` when applicable).
- **Exit code `2`** without JSON means “block” (stderr can carry a reason). Other non-zero exits **fail open** unless **`failClosed`** is set.

**Events:**

| Event | When | Matcher |
|-------|------|-----------|
| `SessionStart` | REPL or `-print` session begins | `startup` / `resume` |
| `PreToolUse` | Before each tool runs | Tool name (e.g. `write_file`, `mcp__…`) |
| `PostToolUse` | After each tool runs | Tool name |
| `UserPromptSubmit` | Before your message is sent to the model | *(all)* |
| `Stop` | Model returns a **text** reply (no tool calls) in a turn | *(all)* — `decision: "block"` with **`reason`** requests another model round with that text as the user message (Codex-style continuation) |
| `SessionEnd` | Session exits | *(all)* |

**Stdout JSON** (subset): `decision` (`block` to deny a prompt/tool or, for `Stop`, to continue with `reason`), `reason`, `additional_context`, `system_message`, `continue` (`false` stops `Stop` continuation).

Use **`/hooks`** in the REPL to list configured hooks. Sub-agents (`delegate_task`) do not run parent hooks.

### Auto-update

Codient checks for new releases on GitHub each time an interactive REPL session starts. If a newer version is available (and was not previously skipped), you are prompted:

```
codient: update available 0.2.0 -> 0.3.0
Install now? [Y/n]
```

- **Y** (or Enter) — downloads the release, replaces the binary in-place, and exits. Restart codient to use the new version.
- **n** — skips this version. Codient remembers the choice (in `~/.codient/update_skip`) and will not ask again until an even newer release is published.

For non-interactive or scripted updates, use the `-update` flag:

```bash
codient -update
```

To disable the startup prompt entirely, set `update_notify` to `false` in config:

```
/config update_notify false
```

The `-update` flag always works regardless of this setting.

## Usage

```bash
# Ping the server
codient -ping

# List models and tools
codient -list-models
codient -list-tools

# Start an interactive session (default when stdin is a TTY)
codient

# One-shot prompt (no interactive session)
codient -prompt "Summarize README.md"

# Start a session with an initial prompt
codient -prompt "Help me understand the repo layout"

# Start in plan mode
codient -mode plan -prompt "Design a small Go CLI for managing todos"

# Force a fresh session (skip resume)
codient -new-session

# Different workspace root
codient -workspace /path/to/repo

# Attach images (vision-capable chat models), single-shot or first REPL turn
codient -image ./screenshot.png -prompt "What error is this?"
codient -image a.png,b.png -prompt "Compare these mockups"
```

### Split-screen TUI

When stdin is a TTY and `-plain` is **not** set, codient launches a Bubble Tea split-screen interface: a scrollable output viewport on top and a fixed input line at the bottom. This keeps the user's typing completely separate from agent output — background events like the semantic index completion will never corrupt the input line.

| Area | Behaviour |
|------|-----------|
| **Viewport** (top) | Shows the full session: welcome banner, agent replies, tool progress, status messages. |
| **Status bar** | Displays "Agent is working…" during turns; a plain separator otherwise. |
| **Input line** (bottom) | Styled `[mode] > ` prompt. Type freely while the agent is streaming. Press **Enter** to submit, **Ctrl+C** to quit. |

**Scrolling:** **Up/Down** arrows scroll one line, **Alt+Up/Down** scroll three lines, **Page Up/Down** scroll half a page, and **Home/End** jump to the top or bottom.

The TUI uses the alternate screen buffer; when you exit, the terminal returns to its previous state. Pass **`-plain`** or pipe stdin to fall back to the classic inline REPL.

### Headless / CI mode (`-print`)

Use **`-print`** (alias **`-p`**) for a **single non-interactive turn**: no REPL, no welcome banner, suitable for scripts and CI. This forces the same path as piping a prompt on stdin, but makes automation explicit. Combine with **`-prompt`** or stdin.

| Flag | Meaning |
|------|---------|
| **`-output-format text`** (default) | Assistant reply on stdout; errors on stderr |
| **`-output-format json`** | One JSON object on stdout with `reply`, `tools_used`, `files_modified`, optional `tokens` / `cost_usd`, `exit_reason`, and `error` on failure |
| **`-output-format stream-json`** | JSONL on stdout: same event shapes as **`-log`** (`llm`, `tool_start`, `tool_end`), plus a final `{"type":"result",...}` line |
| **`-auto-approve off`** (default) | Same as today: non-interactive sessions deny exec/fetch prompts unless allowlisted |
| **`-auto-approve exec`** | Allow **run_command** / **run_shell** when not on the allowlist (no prompt) |
| **`-auto-approve fetch`** | Allow **fetch_url** to hosts not on the allowlist (no prompt) |
| **`-auto-approve all`** | Both exec and fetch |
| **`-max-turns N`** | Cap LLM rounds for this user turn (`0` = unlimited) |
| **`-max-cost USD`** | Stop when **estimated** session cost exceeds the limit (requires usage metadata and known pricing via **`cost_per_mtok`** or the built-in model table) |

**`-log`** still appends JSONL to a file. With **`-output-format stream-json`**, events are written to **stdout** and optionally duplicated to the log file if **`-log`** is set.

Examples:

```bash
codient -print -prompt "List top-level files" -mode ask

codient -print -mode build -auto-approve all -output-format json -prompt "Run fmt" -max-turns 25

echo "Fix the typo in README" | codient -print -output-format json
```

For long-running HTTP integration, see **`-a2a`** below.

### Images and vision

Use a **vision-capable** model (e.g. GPT-4o, Claude 3.5+, many local multimodal servers). Codient sends images as base64 **data URIs** in the standard OpenAI chat format (`image_url` parts).

- **CLI:** `-image path` or repeat `-image` for multiple paths. Comma-separated lists work (`-image a.png,b.png`). Applies to the **first user message** in a REPL session, or to a **single-shot** `-prompt` / stdin run. Combines with `-stream` (no tools).
- **REPL:** `/image path/to.png` queues an image for your **next** message (repeat to attach several). You can also embed paths in text: `@image:screenshot.png` or `@image:"C:\path\with spaces.png"` (paths are relative to the workspace when not absolute).
- **Limits:** PNG, JPEG, GIF, WebP; max **20 MiB** per file (warning above **5 MiB**). Large images still count toward context—use `/compact` if needed.

Use `-help` for all flags. Notable options:

- **`-mode`** — `build` (default), `ask`, or `plan`
- **`-workspace`** — workspace root (overrides config and cwd)
- **`-new-session`** — start fresh instead of resuming the latest session
- **`-update`** — check for a newer release and install it (see [Auto-update](#auto-update))
- **`-repl`** — explicit REPL (default when stdin is a TTY)
- **`-system`** — optional extra system prompt merged into the default tool prompt
- **`-stream` / `-stream-reply`** — streaming behavior
- **`-plain`** — raw assistant text
- **`-progress`** — agent progress on stderr
- **`-log`** — append JSONL events (LLM rounds, tools; each `llm` event may include `prompt_tokens`, `completion_tokens`, `total_tokens` when the server reports usage)
- **`-goal` / `-task-file`** — merged into the first user turn as a task directive
- **`-image`** — attach one or more image files to the first user turn (REPL) or to a one-shot prompt (`-stream` supported); see [Images and vision](#images-and-vision)
- **`-design-save-dir`** — where to save completed plans
- **`-a2a` / `-a2a-addr`** — run an [A2A](https://github.com/a2aproject/A2A) protocol server instead of the interactive CLI (default listen `:8080`)
- **`-print` / `-p`** — headless single-turn mode for CI/scripts; see [Headless / CI mode](#headless--ci-mode--print)
- **`-output-format`** — with `-print`: `text`, `json`, or `stream-json`
- **`-auto-approve`** — with `-print`: `off`, `exec`, `fetch`, or `all`
- **`-max-turns`** / **`-max-cost`** — guardrails for `-print` (see [Headless / CI mode](#headless--ci-mode--print))

### A2A server

To expose codient as an Agent-to-Agent HTTP server:

```bash
codient -a2a -a2a-addr :8080
```

Use the same config (model, base URL, API key, workspace) as the CLI. See [`internal/a2aserver/`](internal/a2aserver/) for protocol details.

### Slash commands

Inside a session you can use slash commands to control the agent:

| Command | Description |
|---------|-------------|
| `/build` (or `/b`) | Switch to build mode (full write tools) |
| `/plan` (or `/p`; also `/design`, `/d`) | Switch to plan mode (read-only, structured implementation design) |
| `/ask` (or `/a`) | Switch to ask mode (read-only Q&A) |
| `/config [key] [value]` | View or set any configuration key (no args = show all, key = show one, key value = set and save) |
| `/setup` | Guided setup wizard for API connection, chat model selection, optional plan-mode model override, and optional embedding model for semantic search |
| `/compact` | Summarize conversation history to save context space |
| `/model <name>` | Switch to a different model (shortcut for `/config model`) |
| `/workspace <path>` | Change the workspace directory |
| `/tools` | List tools available in current mode |
| `/hooks` | List configured lifecycle hooks (requires `hooks_enabled`) |
| `/mcp [server]` | List connected MCP servers and tool counts; with a server name, list that server's tools |
| `/status` | Show session state (mode, model, turns, estimated context, API token totals, auto-check, exec policy) |
| `/cost` (or `/tokens`) | Show session token counts (prompt/completion/total) and estimated cost |
| `/log [path]` | Show logging status or enable JSONL logging to a file |
| `/undo` | Undo the last build turn. With **`git_auto_commit`** (default): removes the last codient commit (`HEAD~1`). Otherwise: restores tracked files and deletes new files from that turn. `/undo all` resets the repo to the commit at session start (auto-commit) or reverts all working-tree changes (legacy). Requires a git repo. |
| `/checkpoint` (or `/cp`) | Save a **named snapshot** of the conversation, mode, model, plan state, and current git `HEAD` (default name `turn-N`). With **`git_auto_commit`** in build mode, uncommitted changes are committed first so the snapshot points at a real commit. |
| `/checkpoints` (or `/cps`) | List checkpoints for this session as a tree (`*` marks the current checkpoint id). |
| `/rollback` (or `/rb`) | Restore conversation and (with **`git_auto_commit`**) reset the working tree to a checkpoint: pass **name**, **`cp_` id prefix**, or **turn number**. Stashes uncommitted work first when needed. |
| `/fork` | Roll back to a checkpoint, then create and checkout **`codient/<slug>`** for a new git line of work; sets a new **conversation branch** label for later checkpoints. Optional second argument is the branch slug. |
| `/branches` (or `/cbranch`) | List logical conversation branches (checkpoint fork labels) and their tips. |
| `/diff [path]` | Print a colored `git diff` vs `HEAD` (optional workspace-relative file). |
| `/branch [name]` | Show current branch, or switch to an existing branch, or create and checkout `name`. |
| `/pr [draft]` | Push `HEAD` to `origin` and open a GitHub pull request with **`gh`** (base branch = protected branch left behind, or `origin` default). Pass `draft` for a draft PR. |
| `/memory` (or `/mem`) | View, edit, or clear cross-session memory files. Subcommands: `show` (default), `edit [global\|workspace]`, `clear [global\|workspace]`, `reload`. |
| `/image <path>` | Attach an image file to your **next** message (vision models). Repeat to queue multiple images. |
| `/new` (or `/n`) | Start a brand new session (fresh ID, history, and design namespace) |
| `/clear` | Reset conversation history (same session) |
| `/help` (or `/h`, `/?`) | Show available commands |
| `/exit` (or `/quit`, `/q`) | Quit the session |

### Git workflow (build mode)

In a git workspace, **build** mode can **auto-commit** each turn that changes files (`git_auto_commit`, default `true`). Each commit uses subject **`codient: turn N`** and a body copied from your user message (truncated to 200 characters). Configure **`git_protected_branches`** (default `main`, `master`, `develop`): if the first commit would land on one of those branches, codient creates and checks out **`codient/<task-slug>`** (with numeric suffixes if the name already exists) so you do not commit directly to e.g. `main`.

Set **`git_auto_commit`** to **`false`** to restore the older behavior: no commits; `/undo` restores files from the working tree snapshot instead of removing the last commit.

The **`create_pull_request`** tool (build mode only) and the **`/pr`** slash command push the current branch to **`origin`** and run **`gh pr create`**. The PR base branch is the protected branch you left when codient created `codient/...`, when applicable; otherwise it follows **`origin/HEAD`** (usually `main`).

### Session persistence

Session state (conversation history, mode, model) is saved under `<workspace>/.codient/sessions/` after each turn. Starting codient again in the same workspace resumes the latest session. Use `-new-session` to start fresh.

**Checkpoints** (named snapshots for rollback and branching) are stored under `<workspace>/.codient/checkpoints/<sessionID>/` (one JSON file per checkpoint plus a `tree.json` index). The session file records **`current_checkpoint_id`** and **`current_branch`** so resume keeps your place in the checkpoint tree.

The semantic search index (when **`embedding_model`** is set) lives under `<workspace>/.codient/index/` and is separate from chat sessions.

### Cross-session memory

Codient supports persistent memory that carries project conventions, user preferences, and past decisions across sessions. Memory is loaded into the system prompt at startup so the agent "remembers" what it learned previously.

**Two layers:**

| Scope | File | Purpose |
|-------|------|---------|
| **Global** | `~/.codient/memory.md` | User-wide preferences and conventions (applies to all projects) |
| **Workspace** | `<workspace>/.codient/memory.md` | Project-specific conventions, architecture decisions, patterns |

Both files are Markdown. Global memory is loaded first, workspace memory second, so project-specific notes can override global ones. Each file is capped at 16 KiB to avoid bloating the system prompt.

**How it works:**

- **Automatic:** In build mode, the agent has a `memory_update` tool. It can proactively record conventions it discovers (build commands, naming patterns, architecture decisions) and user preferences it learns (style, verbosity, workflow).
- **Manual:** Use the `/memory` slash command to view (`/memory show`), edit in `$EDITOR` (`/memory edit workspace`), or clear (`/memory clear global`) memory files. `/memory reload` re-reads files after external edits.
- **Tool actions:** `memory_update` supports `append` (add to end) and `replace_section` (update a `## Heading` section in-place, or create it if missing).

**Repository instruction files** are also loaded into the system prompt alongside memory:

| File | Description |
|------|-------------|
| `AGENTS.md` | Workspace-root conventions file (compatible with common agent tooling) |
| `.codient/instructions.md` | Codient-specific project instructions |

These are read-only from the agent's perspective (capped at 32 KiB total) and complement the read-write memory files.

### Plan mode and saved plans

In **plan** mode, when the assistant's reply includes a **Ready to implement** section, codient saves the markdown under the workspace (by default `.codient/plans/<sessionID>/`). Plans are scoped to the session that created them. Filenames are `{task-slug}_{date-time}_{nanoseconds}.md` so runs never collide. The task slug comes from `-goal`, else `-task-file` basename, else the first line of your first message.

### Sub-agents (task delegation)

The agent has a **`delegate_task`** tool that spawns an isolated sub-agent to handle a self-contained task. This is always available — the model decides when delegation is useful (e.g. parallelizing codebase exploration across multiple areas).

**How it works:**

- The parent agent calls `delegate_task` with a **mode** (`build`, `ask`, or `plan`), a **task** description, and optional **context** snippets.
- A fresh `agent.Runner` is created for the sub-agent with its own conversation history, tool registry matching the requested mode, and (optionally) a different model via [per-mode configuration](#config-file-reference-codientconfigjson).
- The sub-agent runs to completion and its reply is returned to the parent as the tool result.
- Sub-agents cannot spawn further sub-agents (recursion guard).

**Mode restrictions (privilege escalation prevention):**

| Parent mode | Allowed sub-agent modes |
|-------------|------------------------|
| **build** | `build`, `ask`, `plan` |
| **ask** | `ask` only |
| **plan** | `ask` only |

Read-only parent modes (ask, plan) can only delegate to read-only sub-agents, preventing a plan/ask session from gaining write access through delegation.

**Per-mode models** let you route sub-agents to different LLM backends. For example, a local model for build-mode edits and a remote API for ask-mode research — see the `models` key in config.

### Streaming

Assistant text can stream to the terminal as it is generated (`-stream-reply`, default on for TTYs). In plan mode with styled markdown, the turn that produces the full design after a blocking question is buffered once so the reply can be rendered with full markdown formatting.

## Development

```bash
make check       # vet + unit tests only (no live LLM; safe for CI)
make test-unit   # same tests as check, without vet
make test-race   # race detector (also run in GitHub Actions CI after check + build)
make test        # full suite: unit tests + live integration (needs ~/.codient/config.json model + API)
```

`make test` sets `CODIENT_INTEGRATION=1`, `CODIENT_INTEGRATION_STRICT_TOOLS=1`, and `CODIENT_INTEGRATION_RUN_COMMAND=1`, and runs `go test -tags=integration` with a 90-minute timeout so workspace tools, strict tool-calling, and `run_command` are all exercised.

Lighter integration runs (see `make help`):

```bash
make test-integration         # live API only (CODIENT_INTEGRATION=1)
make test-integration-strict  # + strict tool tests (no run_command test unless you set CODIENT_INTEGRATION_RUN_COMMAND=1 yourself)
```

## License

This project is licensed under the MIT License — see [LICENSE](LICENSE) for details.
