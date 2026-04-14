# codient

**codient** is a command-line agent for any **OpenAI-compatible** chat API (local server, cloud provider, etc.). It runs multi-step tool use against your workspace—read and search files, run allowlisted commands, optional HTTPS fetch and web search, and write access in **build** mode. **Ask** and **plan** modes use a read-only tool set and different system prompts: ask for exploration, plan for structured implementation plans with clarifying questions.

**Repository:** [github.com/vaughanb/codient](https://github.com/vaughanb/codient)

## Requirements

- [Go](https://go.dev/dl/) 1.26+ (see `go.mod`)
- A running server exposing OpenAI-style `/v1/chat/completions` (default base URL `http://127.0.0.1:1234/v1`; typical for local stacks)

**Optional:**

- [ast-grep](https://ast-grep.github.io/) — for the `find_references` structural code search tool. Codient auto-detects or offers to download it on first interactive session.

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

All settings are stored in `~/.codient/config.json` (unless `CODIENT_STATE_DIR` points elsewhere) and managed via `/config` and `/setup` inside a session. Environment variables are **not** used for configuration (see below for the one exception).

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
| `context_window` | Model context window in tokens (`0` = probe server) | `0` |
| `context_reserve` | Tokens reserved for the assistant reply | `4096` |
| **LLM** | | |
| `max_llm_retries` | Retries for transient LLM errors | `2` |
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
| `embedding_model` | Model id for `/v1/embeddings` (same base URL as chat). Enables the `semantic_search` tool; leave empty to disable | *(empty)* |

When `fetch_url` receives `Content-Type: text/html`, the body is converted to simplified markdown (headings, links, lists, code) before being returned.

### Environment variables

| Variable | Description |
|----------|-------------|
| `CODIENT_STATE_DIR` | Directory for `config.json` instead of `~/.codient`. This is the **only** environment variable used for configuration (it locates the config file itself). |

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
```

Use `-help` for all flags. Notable options:

- **`-mode`** — `build` (default), `ask`, or `plan`
- **`-workspace`** — workspace root (overrides config and cwd)
- **`-new-session`** — start fresh instead of resuming the latest session
- **`-repl`** — explicit REPL (default when stdin is a TTY)
- **`-system`** — optional extra system prompt merged into the default tool prompt
- **`-stream` / `-stream-reply`** — streaming behavior
- **`-plain`** — raw assistant text
- **`-progress`** — agent progress on stderr
- **`-log`** — append JSONL events (LLM rounds, tools)
- **`-goal` / `-task-file`** — merged into the first user turn as a task directive
- **`-design-save-dir`** — where to save completed plans
- **`-a2a` / `-a2a-addr`** — run an [A2A](https://github.com/a2aproject/A2A) protocol server instead of the interactive CLI (default listen `:8080`)

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
| `/setup` | Guided setup wizard for API connection, chat model selection, and optional embedding model for semantic search |
| `/compact` | Summarize conversation history to save context space |
| `/model <name>` | Switch to a different model (shortcut for `/config model`) |
| `/workspace <path>` | Change the workspace directory |
| `/tools` | List tools available in current mode |
| `/status` | Show session state (mode, model, turns, tokens, auto-check, exec policy) |
| `/log [path]` | Show logging status or enable JSONL logging to a file |
| `/undo` | Undo the last build turn using git (restore modified files, remove new files from that turn). Requires a git repo. Use `/undo all` to revert every tracked turn in the stack. |
| `/new` (or `/n`) | Start a brand new session (fresh ID, history, and design namespace) |
| `/clear` | Reset conversation history (same session) |
| `/help` (or `/h`, `/?`) | Show available commands |
| `/exit` (or `/quit`, `/q`) | Quit the session |

### Session persistence

Session state (conversation history, mode, model) is saved under `<workspace>/.codient/sessions/` after each turn. Starting codient again in the same workspace resumes the latest session. Use `-new-session` to start fresh.

The semantic search index (when **`embedding_model`** is set) lives under `<workspace>/.codient/index/` and is separate from chat sessions.

### Plan mode and saved plans

In **plan** mode, when the assistant's reply includes a **Ready to implement** section, codient saves the markdown under the workspace (by default `.codient/plans/<sessionID>/`). Plans are scoped to the session that created them. Filenames are `{task-slug}_{date-time}_{nanoseconds}.md` so runs never collide. The task slug comes from `-goal`, else `-task-file` basename, else the first line of your first message.

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
