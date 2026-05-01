# learn-claude-code-go

![Go](https://img.shields.io/badge/Go-1.23+-00ADD8?logo=go&logoColor=white)
![SDK](https://img.shields.io/badge/Anthropic%20SDK-Go-191919)
![Progress](https://img.shields.io/badge/progress-s01--s11_done,_s12_todo-blue)
![License](https://img.shields.io/badge/license-MIT-green)

**Language**: 中文 | [English](README_EN.md)

`learn-claude-code-go` 是 [shareAI-lab/learn-claude-code](https://github.com/shareAI-lab/learn-claude-code) 的 Go rewrite 版本。它按课程的 `s01` 到 `s12` 逐步复现 Claude Code 风格 coding agent 的核心机制：agent loop、tool use、todo 管理、subagent、skills、context compact、任务板、后台任务、团队协作协议和自主 agent。


## 设计目标

- 用 Go 重新实现课程里的 agent 演进路径，每个 session 都是一个可独立运行的 `main.go`。
- API 请求、响应和 tool definition 尽量使用 `anthropic-sdk-go` 提供的结构化类型。
- 工具 schema 与工具参数保留 JSON 风格的 `map[string]any`，因为这部分本质上是动态结构。
- 保持实现显式、可读，方便对照 Python 版本理解每一步新增机制。

## 快速开始

准备配置文件：

```bash
cp .env.example .env
```

编辑 `.env`：

```dotenv
ANTHROPIC_API_KEY=your-api-key-here
ANTHROPIC_BASE_URL=https://api.anthropic.com/v1
# Required. Fill in a model ID available to your account/provider.
MODEL=
SKILLS_DIR=./skills
```

`MODEL` 必须由用户显式指定，项目不会内置默认模型。`SKILLS_DIR` 只在 `s05` 和 `s06` 使用；相对路径会按当前进程工作目录解析。

推荐从仓库根目录运行，方便 `.env`、`.tasks/`、`.team/` 和 `SKILLS_DIR` 的路径都落在同一个位置：

```bash
go run ./agents/s01
go run ./agents/s11
```

也可以进入某个 session 目录运行：

```bash
cd agents/s01
go run .
```

## 实现进度

| Session | 状态 | 核心机制 | 新增内容 |
|---------|------|----------|----------|
| s01 | Done | Agent Loop | 基础 `for` 循环，按 `stop_reason` 执行 bash 工具 |
| s02 | Done | Tool Use | 多工具路由：`bash`、`read_file`、`write_file`、`edit_file` |
| s03 | Done | TodoWrite | in-memory Todo 管理，每 3 轮提醒更新 |
| s04 | Done | Subagents | `task` 工具，使用 fresh context 调用子 agent |
| s05 | Done | Skills | YAML frontmatter 解析，`load_skill` 工具 |
| s06 | Done | Context Compact | `microCompact` 清理旧 tool result，`autoCompact` 生成摘要 |
| s07 | Done | Tasks | 文件系统任务板，状态流转与依赖管理 |
| s08 | Done | Background Tasks | goroutine 异步命令执行，通知队列轮询 |
| s09 | Done | Agent Teams | 多 agent 团队，JSONL inbox 通信 |
| s10 | Done | Team Protocols | Shutdown 协议与 Plan Approval 协议 |
| s11 | Done | Autonomous Agents | WORK/IDLE 双阶段循环，自动认领任务 |
| s12 | TODO | Worktree Isolation | TODO: 尚未实现 worktree 隔离 |

## 项目结构

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
├── background/  后台任务管理器
├── skills/      Skill 文件加载与解析
├── tasks/       任务板 CRUD 与依赖管理
├── team/        MessageBus 与 TeammateManager
└── todo/        Todo 管理器
```

## 运行时文件

部分 session 会在当前工作目录下生成状态文件：

| 路径 | 来源 | 用途 |
|------|------|------|
| `.tasks/` | s07, s11 | 保存任务板 JSON 文件 |
| `.team/` | s09, s10, s11 | 保存团队配置与 inbox 消息 |
| `SKILLS_DIR` | s05, s06 | 加载本地 skill markdown 文件 |

如果你希望这些文件生成在仓库根目录，请从根目录执行 `go run ./agents/sXX`。

## 开发与验证

```bash
go test ./...
```

当前仓库没有独立测试用例，`go test ./...` 主要用于编译检查所有 package。

## 项目边界

- 这是课程对照实现，不包含完整 CLI 封装、权限确认、沙箱隔离或生产级错误恢复。
- `bash` 等工具会在本机当前工作目录执行，请只在可信目录中实验。
- `s12` 的 worktree isolation 尚未实现，README 和进度表会明确标记为 TODO。
- 模型由用户通过 `MODEL` 指定；代码不会硬编码任何默认模型。

## 依赖

- [anthropic-sdk-go](https://github.com/anthropics/anthropic-sdk-go) — Anthropic Go SDK
- [godotenv](https://github.com/joho/godotenv) — `.env` 文件加载
- [logrus](https://github.com/sirupsen/logrus) — 结构化日志
- [uuid](https://github.com/google/uuid) — 后台任务 ID
- [yaml.v3](https://gopkg.in/yaml.v3) — Skill frontmatter 解析

## 支持

如果这个仓库帮你理解 Claude Code 风格 agent 的实现方式，欢迎给一个 Star。它能帮助更多人找到这个 Go rewrite 项目。

## License

MIT
