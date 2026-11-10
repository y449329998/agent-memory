# agent-memory

让 Qoder、Trae、WorkBuddy、OpenCode、Claude 共用一份记忆。切到另一个 agent 时，不必从头描述任务。

![客户端总览页](docs/images/client/01-home-top.webp)

所有数据都在本机：一个 SQLite 库、几个项目内的 `HANDOFF.md`。除了显式执行 `link`，它不写任何 agent 的原生文件。

## 3 分钟跑起来

### Windows：装桌面应用

1. 下载 [releases/agent-memory-setup-x64-0.1.0.exe](releases/agent-memory-setup-x64-0.1.0.exe)（校验和见 [releases/SHA256.txt](releases/SHA256.txt)）。免管理员权限，装进 `%LOCALAPPDATA%\Programs\agent-memory`。
2. 双击桌面图标或开始菜单里的「agent-memory 共享记忆」。它自动建配置和库、拉起后台服务（默认端口 47123）、打开主窗口。
3. 窗口里点**立即同步**，记忆与会话就进了共享库；总览、项目、记忆、来源、任务、设置各页点着看就行。关窗只是隐藏到托盘，服务继续跑。
4. 想让 agent 直接读写这份记忆：接驳台页复制配置，或在项目里跑 `agent-memory link qoder --project . --dry-run`（去掉 `--dry-run` 才真写）。

图解说明（每个页面逐区标注、设置项含义、排错与工作流）见 [docs/client-guide.md](docs/client-guide.md)。

### macOS / Linux：命令行 + 浏览器

```bash
CGO_ENABLED=0 go build -o agent-memory ./cmd/agent-memory  # 无 cgo，SQLite 驱动是纯 Go
./agent-memory init                                       # 写 ~/.agent-memory/config.toml 并建库
./agent-memory scan                                       # 导入记忆与会话，刷新各项目简报
./agent-memory serve &                                    # 常驻：HTTP + MCP + 目录监听
open "http://127.0.0.1:47123/?token=$(cat ~/.agent-memory/token)"   # Linux 用 xdg-open
```

打开页面后 token 会存进 `sessionStorage` 并从地址栏抹掉。之后不用重复手动 `scan`——`serve` 监听 agent 目录，变化时自动增量扫描并刷新简报。
接入 agent 与 Windows 相同：`./agent-memory link qoder --project "$PWD" --dry-run`。

## 该用哪个入口

| 场景 | 用法 |
| --- | --- |
| 只想有个能点的界面 | Windows 桌面应用；macOS / Linux 用 `serve` + 浏览器 |
| 让 agent 自动读共享记忆 | 项目内 `.agent-memory/HANDOFF.md`（所有 agent 都会读它） |
| 让 agent 读写、检索、建任务 | `link` 装 MCP（Qoder / OpenCode / Claude） |
| 脚本化、批量、调试 | 命令行 `scan` / `memory` / `handoff` / `doctor` / `stats` |

Trae 与 WorkBuddy 的 MCP 配置格式未确证，`link` 拒绝写入；它们照常通过 `HANDOFF.md` 读到共享记忆。

## 文档地图

| 想知道 | 看哪里 |
| --- | --- |
| 客户端每个页面怎么用 | [docs/client-guide.md](docs/client-guide.md)（图解，21 张标注截图） |
| 命令、配置项、Web UI、HTTP API、MCP 全参考 | [docs/usage.md](docs/usage.md) |
| 下载安装包、校验、从源码打包 | [releases/README.md](releases/README.md) |
| 为什么这么设计 | `docs/superpowers/specs/` |
| 怎么一步步实现的 | `docs/superpowers/plans/` |

## 它怎么工作

- **收**：读取 Qoder 的长期记忆与会话、Trae 的用户记忆与 `project_memory.md` / `user_rules/`、WorkBuddy 的 `MEMORY.md`，归一化后按内容哈希去重，进同一份 SQLite 库。
- **理**：一次 agent 对话 = 一条**对话任务**（`kind=session`），步骤取自那个 agent 的 Todo，没有就从会话记录整理；跨对话收尾时手动**合并**成一条**合并任务**（`kind=formal`）。冲突的记忆并存，不自动裁决。
- **给**：给每个项目生成 `.agent-memory/HANDOFF.md`（项目概况 / 用户偏好 / 硬约束与约定 / 最近任务 / 未决问题 / 共享记忆入口），并让 agent 通过 MCP 检索与写回。
- 检索用 FTS5 + 自切分中文二元组，两字中文词（如「偏好」「命名」）也能命中。

### 核心概念

- **记忆**：`scope` = `user`（跨项目）/ `project`；`type` = `preference` `constraint` `convention` `lesson` `fact` `goal`。
- **任务排序**：默认按**最近对话**——关联会话的最新时间（合并任务再算上对话片段的事件时间）。任务本体的更新时间会被扫描刷成扫描时间，所以不参与排序。另有「最近创建」「最久没动」。
- **简报预算**：默认 4000 token，超了依次压任务细节、减卡片数；用户偏好以摘要保留（每条截断、最多 8 条，事实类交给 `memory_search`），项目硬约束不砍。
- **幂等**：`scan` 重复跑只处理内容变化过的源；`handoff` 内容相同则报「未变化」，不碰文件。

## 常用命令

```bash
agent-memory scan [--agent qoder] [--full]        # 导入；--full 忽略哈希全量重解析
agent-memory handoff --project . [--print]        # 生成简报；--print 只打印不落盘
agent-memory memory search 偏好 [--limit 5]       # 中文检索
agent-memory memory add "提交前先跑 go test" --type convention
agent-memory doctor [--since 24h]                 # 库状态、计数、agent 发现结果、写入目标清单
agent-memory serve [--port 47123] [--no-watch]    # 常驻服务；--port 0 由系统分配
agent-memory link <agent> --project <路径> [--dry-run]
agent-memory stats [--json]
```

退出码：`0` 成功，`1` 运行错误，`2` 用法错误。位置参数以 `-` 开头时，前面单独写 `--`。
完整参数、配置文件的每个键、HTTP 接口与 MCP 工具入参见 [docs/usage.md](docs/usage.md)。

## MCP 接入要点

MCP 暴露 14 个工具：`memory_search` / `memory_add` / `memory_list` / `handoff_get` / `task_list` / `task_get` / `task_add` / `task_update` / `task_sync` / `task_export` / `conversation_search` / `conversation_get` / `project_resolve` / `stats`。

`link` 在 agent 的 MCP 配置里启动的是 `agent-memory mcp --port <实际端口>`——一个 stdio 桥，把每行 JSON-RPC 转发给 `serve` 的 `/mcp`，token 通过环境变量 `AGENT_MEMORY_TOKEN` 传入。写入位置：

| agent | MCP 配置 | 项目区块 |
| --- | --- | --- |
| `qoder` | `~/.qoder-cn/mcp.json` | `AGENTS.md` |
| `opencode` | `~/.config/opencode/opencode.json` | `AGENTS.md` |
| `claude` | `~/.claude.json` | `CLAUDE.md` |

**`link` 会改 agent 的配置，务必先 `--dry-run`。** 写前自动备份为 `<文件>.bak-<时间戳>`，项目内只改它自己装过的那段带 marker 的区块，`unlink` 可撤销。改了服务端口需要重新 `link`；MCP 客户端通常要重启才加载新配置。

### 配套技能（让 agent 知道何时用哪个工具）

`skills/agent-memory/SKILL.md` 是一份 Agent Skill：14 个工具各自「什么时候用 / 不要用在」、接手/干活/交接/找历史对话的调用顺序，以及 `step_status` 冻结语义、`SourceKey` 等硬规则。

```bash
cp -r skills/agent-memory ~/.qoder-cn/skills/    # 装到 Qoder 用户级技能目录
```

之后用 `/agent-memory` 手动调用，或按技能描述在相关对话里自动触发。

## 平台差异

Windows 是主力平台，桌面应用带原生窗口、托盘与自启。macOS / Linux 上同一份代码能编译运行，但界面交给浏览器：

| 能力 | Windows | macOS / Linux |
| --- | --- | --- |
| `serve` / `mcp` / `link` / CLI | ✅ | ✅ |
| Web UI（浏览器打开） | ✅ | ✅ |
| 桌面窗口 | WebView2 | 回退 Edge / Chrome / Chromium 的 `--app` 窗口 |
| 系统托盘、关窗驻留 | ✅ | ❌ 窗口关掉即退出桌面进程（后台 `serve` 仍在跑） |
| 开机自启、开始菜单快捷方式 | ✅ | ❌ 自己写 systemd user unit / LaunchAgent |
| 单实例保护 | 命名互斥量 | ❌ 别重复启动 |
| LLM 密钥存 DPAPI | ✅ | 明文写进配置，注意文件权限 |

macOS / Linux 想常驻，建议交给服务管理器。Linux systemd user unit：

```ini
# ~/.config/systemd/user/agent-memory.service
[Unit]
Description=agent-memory shared memory service

[Service]
ExecStart=%h/.local/bin/agent-memory serve

[Install]
WantedBy=default.target
```

```bash
systemctl --user daemon-reload && systemctl --user enable --now agent-memory
loginctl enable-linger "$USER"          # 未登录也常驻
```

macOS 用 LaunchAgent：`~/Library/LaunchAgents/com.agent-memory.serve.plist`，`ProgramArguments` 指向 `agent-memory serve`，`RunAtLoad` 与 `KeepAlive` 设 `true`。只想临时跑一次：`nohup agent-memory serve >~/.agent-memory/serve.log 2>&1 &`。

Windows 桌面应用会自己拉起 `serve`；macOS / Linux 请自己常驻 `serve`，其余功能完全一致。

## 构建

需要 Go 1.26+（`go.mod` 声明 `go 1.26.0`），无其他构建依赖。

```bash
go build -o agent-memory ./cmd/agent-memory                     # CLI + 服务 + Web UI + MCP 桥
go build -ldflags "-H windowsgui" -o agent-memory-app.exe \
    ./cmd/agent-memory-app                                      # Windows 桌面应用（不弹控制台）
go test ./... && go vet ./...
```

SQLite 驱动是纯 Go 的 `modernc.org/sqlite`，没有 cgo 依赖，交叉编译只要 `CGO_ENABLED=0`：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o dist/agent-memory-windows-amd64.exe ./cmd/agent-memory
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "-s -w" -o dist/agent-memory-linux-amd64       ./cmd/agent-memory
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "-s -w" -o dist/agent-memory-linux-arm64       ./cmd/agent-memory
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "-s -w" -o dist/agent-memory-darwin-amd64      ./cmd/agent-memory
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "-s -w" -o dist/agent-memory-darwin-arm64      ./cmd/agent-memory
```

Windows 安装包（需 NSIS 3，免管理员权限，卸载保留数据）：

```powershell
powershell -ExecutionPolicy Bypass -File scripts\package.ps1     # 产出 out\agent-memory-setup-x64.exe
```

## 数据位置与写入边界

- 配置：`~/.agent-memory/config.toml`（Windows 为 `%USERPROFILE%\.agent-memory\`）
- 记忆库：`~/.agent-memory/memory.db`
- 运行期：`token`（首次 `serve` 生成，0600）、`port`（`serve` 运行期间存在，退出时删除）
- 项目简报：`<项目>/.agent-memory/HANDOFF.md`，同时把 `/.agent-memory/` 加进 `.git/info/exclude`（只影响本机，不改 `.gitignore`）

读取 agent 目录只做 `ReadDir` / `ReadFile` / `Stat`。全部写入点就这几处：数据目录、项目内 `.agent-memory/`、`link` 显式执行时的 MCP 配置与项目区块（写前备份）。会话不留完整对话原文，只保存裁剪后的事件（单条最多 500 字符）与提炼出的任务。`doctor` 会打印这份封闭的写入目标清单。

## 仓库与许可

- Gitee：<https://gitee.com/muyi99/agent-memory>
- GitHub：<https://github.com/y449329998/agent-memory>
- v0.1.0，MIT，见 [LICENSE](LICENSE)
