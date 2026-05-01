# learn-claude-code-go

![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)
![SDK](https://img.shields.io/badge/Anthropic%20SDK-Go-191919)
![Progress](https://img.shields.io/badge/progress-s01--s11_done,_s12_todo-blue)
![License](https://img.shields.io/badge/license-MIT-green)

**Language**: English | [中文](README.md)

`learn-claude-code-go` is a Go rewrite of [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code). It follows the course path from `s01` to `s12` and progressively rebuilds the core mechanisms of a Claude Code-style coding agent: agent loop, tool use, todo management, subagents, skills, context compacting, task board, background tasks, team collaboration protocols, and autonomous agents.

## Design Goals

- Rebuild the course's agent evolution path in Go. Each session is an independently runnable `main.go`.
- Use the structured types provided by `anthropic-sdk-go` for API requests, responses, and tool definitions where practical.
- Keep tool schemas and tool arguments as JSON-style `map[string]any`, because these parts are inherently dynamic.
- Keep the implementation explicit and readable so it is easy to compare each step with the Python version.

## Quick Start

Prepare the configuration file:

```bash
cp .env.example .env
```

Edit `.env`:

```dotenv
ANTHROPIC_API_KEY=your-api-key-here
ANTHROPIC_BASE_URL=https://api.anthropic.com/v1
# Required. Fill in a model ID available to your account/provider.
MODEL=
SKILLS_DIR=./skills
```

`MODEL` must be explicitly provided by the user. The project does not include a built-in default model. `SKILLS_DIR` is only used by `s05` and `s06`; relative paths are resolved from the current process working directory.

Running from the repository root is recommended so `.env`, `.tasks/`, `.team/`, and `SKILLS_DIR` all resolve to the same place:

```bash
go run ./agents/s01
go run ./agents/s11
```

You can also run a session from its own directory:

```bash
cd agents/s01
go run .
```

## Implementation Progress

| Session | Status | Core Mechanism | Additions |
|---------|--------|----------------|-----------|
| s01 | Done | Agent Loop | Basic `for` loop, executing the bash tool based on `stop_reason` |
| s02 | Done | Tool Use | Multi-tool routing: `bash`, `read_file`, `write_file`, `edit_file` |
| s03 | Done | TodoWrite | In-memory todo management, with an update reminder every 3 turns |
| s04 | Done | Subagents | `task` tool, invoking a subagent with fresh context |
| s05 | Done | Skills | YAML frontmatter parsing and the `load_skill` tool |
| s06 | Done | Context Compact | `microCompact` clears old tool results, `autoCompact` generates summaries |
| s07 | Done | Tasks | File-based task board with status transitions and dependency management |
| s08 | Done | Background Tasks | Asynchronous command execution with goroutines and notification polling |
| s09 | Done | Agent Teams | Multi-agent teams with JSONL inbox communication |
| s10 | Done | Team Protocols | Shutdown protocol and Plan Approval protocol |
| s11 | Done | Autonomous Agents | WORK/IDLE two-phase loop with automatic task claiming |
| s12 | TODO | Worktree Isolation | TODO: worktree isolation is not implemented yet |

## Project Structure

```text
agents/
├── s01/   Agent Loop
├── s02/   Tool Use
├── s03/   TodoWrite
├── s04/   Subagents
├── s05/   Skills
├── s06/   Context Compact
├── s07/   Tasks
├── s08/   Background Tasks
├── s09/   Agent Teams
├── s10/   Team Protocols
├── s11/   Autonomous Agents
└── s12/   Worktree Isolation placeholder

internal/
├── background/  Background task manager
├── skills/      Skill file loading and parsing
├── tasks/       Task board CRUD and dependency management
├── team/        MessageBus and TeammateManager
└── todo/        Todo manager
```

## Runtime Files

Some sessions create state files under the current working directory:

| Path | Source | Purpose |
|------|--------|---------|
| `.tasks/` | s07, s11 | Stores task board JSON files |
| `.team/` | s09, s10, s11 | Stores team configuration and inbox messages |
| `SKILLS_DIR` | s05, s06 | Loads local skill markdown files |

If you want these files to be created in the repository root, run `go run ./agents/sXX` from the root directory.

## Development and Verification

```bash
go test ./...
```

The repository currently has no standalone test cases. `go test ./...` is mainly used as a compile check for all packages.

## Project Scope

- This is a course reference implementation. It does not include a complete CLI wrapper, permission confirmation, sandbox isolation, or production-grade error recovery.
- Tools such as `bash` execute on your local machine in the current working directory. Experiment only in trusted directories.
- Worktree isolation in `s12` is not implemented yet, and both the README and the progress table mark it as TODO.
- The model is specified by the user through `MODEL`; the code does not hardcode any default model.

## Dependencies

- [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) — Anthropic Go SDK
- [godotenv](https://github.com/joho/godotenv) — `.env` file loading
- [logrus](https://github.com/sirupsen/logrus) — structured logging
- [uuid](https://github.com/google/uuid) — background task IDs
- [yaml.v3](https://gopkg.in/yaml.v3) — skill frontmatter parsing

## Support

If this repository helps you understand how Claude Code-style agents are implemented, a Star would be appreciated. It helps more people find this Go rewrite examples.

## License

MIT
