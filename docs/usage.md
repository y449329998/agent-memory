# agent-memory 使用说明

面向使用者的完整手册。设计取舍与实现细节见 `docs/superpowers/specs/`，实施记录见 `docs/superpowers/plans/`。

---

## 1. 它解决什么

你在 Qoder / Trae / WorkBuddy / OpenCode 之间切换时，每个 agent 各存一份记忆与历史，互不可见。agent-memory 把 Qoder 的长期记忆与会话历史、WorkBuddy 带任务清单的本地会话、Trae 与 WorkBuddy 的记忆文件读进一个本地共享库：

- 记忆去重后统一检索（中文两字词可命中）
- 会话提炼成任务，任务可跨会话继续更新（目标 / 状态 / 步骤 / 改动文件 / 下一步 / 未决问题）
- 每个项目生成 `.agent-memory/HANDOFF.md`，新 agent 接手先读它
- 常驻服务通过 MCP 让 agent 按需读写；Web UI 供人查看与操作

它**只读**各 agent 的原生文件，从不改写。写点只有三处，见第 13 节。

---

## 2. 安装

SQLite 驱动是纯 Go 的 `modernc.org/sqlite`，没有 cgo 依赖，Windows / macOS / Linux 都能直接构建。

### Windows

```bash
cd F:/go/agent-memory
go build -o agent-memory.exe ./cmd/agent-memory          # 命令行与服务
go build -ldflags "-H windowsgui" -o agent-memory-app.exe ./cmd/agent-memory-app  # 桌面应用（双击用，无控制台窗口）
./agent-memory.exe init
```

不用命令行的直接看第 10.5 节：双击 `agent-memory-app.exe` 即可，或用 [releases/agent-memory-setup-x64-0.1.0.exe](../releases/README.md) 安装。

### macOS / Linux

```bash
CGO_ENABLED=0 go build -o agent-memory ./cmd/agent-memory
./agent-memory init
./agent-memory scan
./agent-memory serve &          # 常驻，见第 2.5 节的托管方式
```

macOS / Linux 没有桌面窗口应用：WebView2、托盘、开机自启、快捷方式、单实例保护都只在 Windows 构建里。
这几个功能在非 Windows 上会降级（详见第 2.5 节），命令行、Web UI、HTTP API 与 MCP 完全一致。

`init` 会创建 `~/.agent-memory/config.toml` 与 `memory.db`，并打印发现的 Qoder 目录。若自动发现不到：

```bash
./agent-memory init --data-dir /data/my-memory --force     # Windows 换成 F:/my-data
# 或手工在 config.toml 写：
# [qoder]
# home = 'C:\Users\You\.qoder-cn'
```

配置里 `--data-dir` 只在配置不存在时生效；**已存在时必须再加 `--force`**，否则提示忽略（防止配置与实际库不一致）。

环境要求：Go 1.26+（`go.mod` 声明 `go 1.26.0`）。本项目在 Windows 10 + Go 1.26.0 上实测，`linux/amd64`、`linux/arm64`、`darwin/amd64`、`darwin/arm64`、`windows/amd64` 五个目标均可交叉编译（见第 16 节）。桌面窗口依赖 Windows 自带的 WebView2 运行时（Win10/11 一般已安装），缺失时自动回退 Edge/Chrome 的应用窗口。

---

## 2.5 在 macOS / Linux 上用

功能差别只在「界面怎么打开」和「谁把服务常驻」：

```bash
# 一次性导入
./agent-memory scan
./agent-memory doctor                       # 库状态、发现结果、写入目标清单

# 常驻服务（HTTP + MCP + 目录监听）
./agent-memory serve

# 另开终端，打开 Web UI
open "http://127.0.0.1:47123/?token=$(cat ~/.agent-memory/token)"      # macOS
xdg-open "http://127.0.0.1:47123/?token=$(cat ~/.agent-memory/token)"  # Linux

# 接入 agent（先看计划再写）
./agent-memory link qoder --project "$PWD" --dry-run
./agent-memory link qoder --project "$PWD"
```

Linux 交给 systemd 用户服务常驻：

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
loginctl enable-linger "$USER"      # 未登录也保持运行
journalctl --user -u agent-memory -f
```

macOS 用 LaunchAgent（`~/Library/LaunchAgents/com.agent-memory.serve.plist`，`ProgramArguments` 指向 `agent-memory serve`，`RunAtLoad` 与 `KeepAlive` 设 `true`，`launchctl load` 加载）；只想跑一次就 `nohup ./agent-memory serve >~/.agent-memory/serve.log 2>&1 &`。

平台差异一览：

| 能力 | Windows | macOS / Linux |
| --- | --- | --- |
| CLI、`serve`、Web UI、HTTP API、`mcp`、`link` | ✅ | ✅ |
| 桌面窗口 | WebView2 原生窗口 | 交给浏览器；`agent-memory-app` 会退化（见下） |
| 系统托盘、关窗驻留 | ✅ | ❌ 托盘不可用，窗口关掉即退出桌面进程（`serve` 子进程仍在跑） |
| 开机自启、快捷方式 | ✅ | ❌ 用 systemd / LaunchAgent |
| 单实例保护 | 命名互斥量 | ❌ 别重复启动 `serve`（端口会冲突并报错） |
| LLM API Key 存储 | DPAPI（加密） | 明文写进 `config.toml`，自己控制文件权限（建议 `chmod 600`） |

`agent-memory-app` 在非 Windows 上仍可构建运行：配置/库/`serve` 照常准备，但窗口用浏览器的 `--app` 模式打开（需要本机装了 Edge / Chrome / Chromium 并在 PATH 里），且没有托盘与自启。日常使用建议直接 `serve` + 浏览器。

源目录发现只有一处受平台影响：Trae 的 `workspaceStorage` 索引默认按 Windows 的 `%APPDATA%` 推导，非 Windows 需要手工配 `[trae] workspace_roots`（Qoder `~/.qoder-cn`、WorkBuddy `~/.workbuddy` 在各平台一致）。

---

## 3. 五分钟上手

```bash
./agent-memory.exe scan                 # 导入记忆与会话（可重复执行）
./agent-memory.exe memory search 偏好    # 检索
./agent-memory.exe handoff --project . # 生成当前项目简报（写入 .agent-memory/HANDOFF.md）
./agent-memory.exe doctor              # 自检：统计、发现结果、写入目标、失败源
./agent-memory.exe serve --port 47123  # 常驻服务（另开终端）
./agent-memory.exe link qoder --project . --dry-run   # 先看会改什么
./agent-memory.exe link qoder --project .             # 真正接入
```

macOS / Linux 把 `./agent-memory.exe` 换成 `./agent-memory`，其余完全相同；常驻方式见第 2.5 节。

`scan` 按 qoder → trae → workbuddy 的固定顺序读三家：Qoder 读 `~/.qoder-cn/memory`、`projects/*/memory` 与会话 jsonl；Trae 读 `~/.trae-cn/memory`（含 `projects/<项目>/project_memory.md`）与 `user_rules/`，并用 `%APPDATA%\Trae CN\User\workspaceStorage` 把项目目录名映射回真实路径；WorkBuddy 读 `~/.workbuddy/MEMORY.md`，并导入 `projects/<项目>/<会话>.jsonl`：带任务清单（`tasks/<会话>/`）的会话导入完整步骤，没有清单的只导入最近 30 天，用来覆盖 WorkBuddy 侧栏的近期任务。Trae 的会话正文在云端，`state.vscdb` 只有界面状态，因此只导入记忆。

浏览器打开 `http://127.0.0.1:47123/?token=<~/.agent-memory/token 的内容>` 即 Web UI。

---

## 4. 命令参考

全局约定：退出码 `0` 成功、`1` 运行错误（IO/库/未知项目）、`2` 用法错误（参数不对、未知类型/agent）；正常输出到 stdout，错误与 usage 到 stderr。

### init

```
agent-memory init [--data-dir PATH] [--force]
```

创建数据目录、默认配置与数据库。配置文件已存在时保持不变（打印提示）；`--force` 才覆盖。`--data-dir` 只在真正写配置时生效。

### scan

```
agent-memory scan [--agent qoder] [--full]
```

扫描所有已启用 agent 的记忆文件与会话，结束前为「有项目级记忆或任务卡」的项目刷新 `HANDOFF.md`。

- 幂等：内容哈希没变的源不会重写任何记忆行；第二次执行通常显示 `unchanged=N`。
- `--full`：清空该 agent 的扫描游标（只删 `source` 行，不删记忆），强制重新解析。
- `--agent`：只扫一个 agent（调试用）。注意只扫一个 agent 时，其他 agent 支撑的条目不会被误标过时。
- 输出示例：`scan: units=16 created=0 merged=0 unchanged=16 staled=0 sessions=3 sessions_unchanged=2 cards=0 unsettled=1 errors=0`

各字段：`units/created/merged/unchanged/staled` 是记忆单元；`sessions/sessions_unchanged` 是会话；`cards` 本轮新生成的任务卡；`unsettled` 还没写完（文件 mtime 在阈值内）而暂不出卡的会话。

### memory

```
agent-memory memory search <关键词> [--project PATH|KEY] [--type T] [--limit N] [--include-stale]
agent-memory memory add <文本> [--type T] [--scope user|project] [--project PATH] [--agent NAME]
agent-memory memory list [--project PATH|KEY] [--limit N] [--include-stale]
agent-memory memory purge [--yes]
```

- `search`：FTS5 + 中文二元组。**不带 `--project` 时只检索用户级记忆**；带项目时返回「该项目 + 用户级」。结果含分数、类型/范围、来源 agent、最后见到时间。
- `add`：默认 `--type fact --scope user`。项目级必须给 `--project`；路径不存在也会建项目（先记一笔，等真正出现在扫描里再合并）。文本会归一化后去重：同内容不同排版/大小写/agent 合并成一条，只追加来源。
- `list`：不带 `--project` 只列用户级；带项目时含用户级。用于浏览，不做相关性排序。
- `purge`：默认只报过时条数；加 `--yes` 真删（同时清 FTS 行与来源行）。
- 文本以 `-` 开头时用 `--` 转义：`memory add -- "-5% 阈值"`。

支持的记忆类型：`preference`（偏好）、`constraint`（约束）、`convention`（约定）、`lesson`（教训）、`fact`（事实）、`goal`（目标）。

### handoff

```
agent-memory handoff [--project PATH] [--print] [--budget N]
```

生成项目交接简报（六节：项目概况、用户偏好、项目硬约束与约定、最近任务、未决问题、共享记忆入口）。

- 默认写入 `<项目>/.agent-memory/HANDOFF.md`；内容没变时打印「未变化」且**不刷新文件 mtime**。
- `--print` 只打印，不写任何文件。
- `--budget` 覆盖配置里的 token 预算；裁剪顺序为「卡片细节 → 卡片数量 → 未决问题」，用户偏好与硬约束永不裁剪。
- 首次写入时会维护项目的 `.git/info/exclude`（只影响本机，不动提交的 `.gitignore`）。
- 拒绝写入 agent 原生目录（`~/.qoder-cn`、`~/.trae-cn`、`~/.workbuddy`、`~/.config/opencode`、`~/.claude`），输出 `warn:` 并跳过。

### doctor

```
agent-memory doctor [--since 720h]
```

自检报告：库路径、项目/记忆/过时/源/失败源统计、按 agent 与按类型分布、会话/任务卡/简报计数、**写入目标清单**（数据目录、DB、每个 link 登记过的区块文件）、agent 发现结果（qoder / trae / workbuddy，Trae 附 workspace 根数量）、最近变更的源文件、解析失败的源、未合并项目（低置信 slug）、同一路径映射到多个项目的脏数据告警。

排查时先跑它。

### serve

```
agent-memory serve [--port 47123] [--poll 30] [--debounce 2s] [--no-watch]
```

常驻服务：扫描 + 文件监听 + HTTP/MCP。

- 只监听 `127.0.0.1`，用 `~/.agent-memory/token` 做 Bearer 鉴权；端口写入 `~/.agent-memory/port`，退出时删除。
- 不写 `--port` 时使用配置 `[server] port`（默认 47123）；显式 `--port 0` 才让系统分配随机端口。桌面端「设置」改端口会自动重写已登记的 MCP 配置；手工改配置文件后仍要重新 `link`。
- `--poll` 轮询兜底间隔（0 表示用配置 `[scan] interval`）；`--debounce` 事件去抖窗口。
- 监听目录只覆盖记忆源：qoder 的 `memory/`、`projects/`，trae 的 `memory/`、`user_rules/`（去重后交给 watcher）；WorkBuddy 不监听，会话与任务清单的变化只靠 `--poll` 兜底。
- `--no-watch` 只开服务不扫描（调试用），此时 `POST /v1/scan` 仍可用。
- 已在运行（同端口健康检查通过）时拒绝启动；但用不同 `--port` 可以起第二个实例——**不要这样用**，两个进程会重复扫描同一库。

### stats

```
agent-memory stats [--json]
```

打印统计；`--json` 便于脚本消费。

### mcp

```
agent-memory mcp [--port 47123]
```

stdio 桥：把 agent 发到 stdin 的每行 JSON-RPC 转发给 daemon 的 `/mcp`。token 优先取环境变量 `AGENT_MEMORY_TOKEN`，否则读 token 文件。daemon 未运行时会对每条请求回一条 JSON-RPC 错误（带「先执行 agent-memory serve」），并保留请求 id。

一般不需要手动运行，`link` 会把它写进 agent 的 MCP 配置。

### link / unlink

```
agent-memory link   <qoder|opencode|claude> [--project PATH] [--dry-run]
agent-memory unlink <qoder|opencode|claude> [--project PATH]
```

- `--dry-run` 一个字节都不写，也不需要 serve 在跑；不指定时默认用当前目录作为项目。
- 真正 `link` 需要 serve 在运行（要读实际端口）。写入前把目标配置备份为 `<文件>.bak-<时间戳>`；只增删 `agent-memory` 这一个 key；重复执行是幂等的（内容相同不写、不留备份）。
- 项目内安装的区块用 `<!-- agent-memory:begin -->` / `end` 包裹，只放 project_key 与指针；`unlink` 精确移除自己写的内容。
- `trae`、`workbuddy` 的配置格式未确证，`link` 明确拒绝；它们通过 `.agent-memory/HANDOFF.md` 读取共享记忆。

### version / help

```
agent-memory version | --version
agent-memory help | -h | --help
```

---

## 5. 配置文件

位置：`~/.agent-memory/config.toml`（`init` 生成，权限 0600）。完整示例：

```toml
data_dir = 'C:\Users\You\.agent-memory'

[server]
port = 47123              # 桌面版与 MCP 配置使用的端口

[desktop]
close_to_tray = true      # 关窗后隐藏到托盘；false 表示关窗即停止服务并退出

[mcp]
binary_path = ''          # 留空自动找 exe 同目录的 agent-memory.exe

[llm]
enabled = false
base_url = ''             # 例：https://api.openai.com/v1 或 http://127.0.0.1:11434/v1
model = ''
timeout = "30s"
max_input_chars = 12000

[qoder]
disabled = false
home = 'C:\Users\You\.qoder-cn'

[trae]
disabled = false
home = 'C:\Users\You\.trae-cn'
workspace_roots = [
  'C:\Users\You\AppData\Roaming\Trae CN\User\workspaceStorage',
  'C:\Users\You\AppData\Roaming\Trae\User\workspaceStorage',
]

[workbuddy]
disabled = false
home = 'C:\Users\You\.workbuddy'

[scan]
interval = "30s"          # 轮询兜底间隔

[handoff]
budget_tokens = 4000      # 简报预算；实测真实项目全量约 3.5k
recent_cards = 3
settle_idle = "5m"        # 会话文件静默多久算写完、可以出任务卡
```

说明：
- 未知键会**报错**（拼错的键不会静默忽略），错误里列出全部未知键。
- 时长为 TOML 字符串（`"30s"`、`"5m"`）；写成整数表示纳秒。
- `qoder.home` 不填则自动发现 `~/.qoder-cn` → `~/.qoder`（判据是存在 `memory` 或 `projects` 子目录）。
- `trae.home` 不填则自动发现 `~/.trae-cn` → `~/.trae`（判据是存在 `memory` 或 `user_rules` 子目录）。
- `trae.workspace_roots` 不填则取 `%APPDATA%\Trae CN\User\workspaceStorage` → `%APPDATA%\Trae\User\workspaceStorage`；`APPDATA` 为空时退回 `<用户目录>\AppData\Roaming` 下的同名路径。
- `workbuddy.home` 不填则自动发现 `~/.workbuddy`（判据是存在 `MEMORY.md` 或 `app` 子目录）。
- 三家的 `disabled = true` 都会让对应适配器既不扫描、也不出现在 `doctor` 的发现结果里。
- `llm.enabled` 打开且 `base_url`/`model`/API Key 齐全，才会启动摘要 worker；缺任何一项都不会创建摘要任务、也不会发起网络请求。
- API Key 单独存 `llm.key`（Windows 用 DPAPI 加密，接口只返回是否已配置，不回显明文），不写进 `config.toml`。
- 启用摘要会把记忆正文、任务目标/步骤/片段发送到 `base_url` 指向的模型服务；不配置 LLM 时不会发出任何模型请求，原文始终保存在本机。
- 这些分组都可以在桌面端「设置」页修改，保存后自动生效（端口会热切换并重写已登记的 MCP 配置）。

---

## 6. 数据与文件位置

| 位置 | 内容 | 谁写 |
|---|---|---|
| `~/.agent-memory/config.toml` | 配置 | `init` |
| `~/.agent-memory/memory.db` | 共享库（SQLite，WAL；含 `-wal`/`-shm`） | `serve` / CLI |
| `~/.agent-memory/token` | 访问令牌（0600） | 首次 `serve` / `link` / `mcp` |
| `~/.agent-memory/port` | 运行中 daemon 的端口 | `serve` |
| `~/.agent-memory/app.log` | 桌面应用与它拉起的 serve 的日志 | 桌面应用 |
| `~/.agent-memory/llm.key` | LLM API Key（Windows DPAPI 加密，0600） | 设置页 |
| `~/.agent-memory/backups/` | `memory-*.db` 一致性备份，保留最近 10 份 | 设置页/导入前自动备份 |
| `~/.agent-memory/imports/` | 导入预览的临时 JSON，10 分钟过期 | 数据导入 |
| `<项目>/.agent-memory/HANDOFF.md` | 交接简报 | `scan` / `handoff` / Web UI |
| `<项目>/.git/info/exclude` | 加一行 `/.agent-memory/` | 首次写简报时 |
| `~/.qoder-cn/mcp.json` 等 | MCP 配置 | **仅** `link`/`unlink` |
| `<项目>/AGENTS.md` 或 `CLAUDE.md` | 受管区块 | **仅** `link`/`unlink` |

Trae 与 WorkBuddy 的原生目录（`~/.trae-cn`、`%APPDATA%\Trae CN\User\workspaceStorage`、`~/.workbuddy`，含 WorkBuddy 的 `projects/`、`tasks/`）**只读**：`scan` 只做 `os.ReadDir` / `os.ReadFile` / `os.Stat`，不会在其中创建、修改或删除任何文件。

`.agent-memory/` 用 `.git/info/exclude` 而不是 `.gitignore`：只影响本机，不会出现在提交里。

---

## 7. 记忆模型

**一条记忆** = `(scope, project, type, 归一化文本)`。归一化 = NFKC → 去代码围栏 → 去列表符号 → 折叠空白；去重哈希再转小写，所以大小写与排版不同算同一条。**展示文本保留大小写与全角标点**。

**来源**：每条记忆可有多条来源（agent + 文件路径 + 引用片段）。跨 agent 的同一句话合并为一条，展示时列出全部来源。

**过时（stale）规则**：
- 源文件里不再出现的条目 → 标过时（不硬删）；同一文件被改写后，旧文本也会被标过时
- 只要还有任何一个来源本轮扫到，条目就保持新鲜
- 有过解析错误的源恢复后，它支撑的条目会复活
- 手动标过时的条目不会被扫描自动复活（除上述「错误恢复」路径）
- `memory purge --yes` 才真正删除

**项目身份**：优先用 git remote（`github.com/owner/repo`），没有 remote 才用规范化绝对路径。所以同一个仓库的不同克隆/写法会合并；只靠 slug 反解（`E--company-xxx`）的低置信项目**不会**自动合并，doctor 会列出来。

---

## 8. 中文检索说明

SQLite FTS5 默认分词搜不到中文（实测：「用户偏好」里搜「偏好」命中 0 条），`trigram` 分词器对两字词召回也是 0。因此索引时把连续汉字切成「单字 + 相邻二元组」，查询用同样的规则只取二元组。

- 能搜：`偏好`、`命名`、`中文`、`matchGoodsSku`、`snake_case`、`nuonuo-fresh3-api`
- 搜不到：被引号/括号断开的跨段短语（如正文是 `“蛇形”字段`，搜 `蛇形字段` 会漏）；加空格分开或搜其中一段即可
- 大小写不敏感（`MatchGoodsSku` 与 `matchGoodsSku` 等价），但展示保留原文大小写

---

## 9. 任务与交接简报

**会话摘要卡**仍由规则从 Qoder / WorkBuddy 的会话事件抽取（不调用 LLM），它是内部同步来源：

- 目标：首条真实用户消息；后续用户消息追加。WorkBuddy 的续聊摘要会还原其中的 `<user_query>`，只有截图的会话用会话标题兜底
- 改动文件：Edit/Write 类工具调用的文件路径
- 执行命令：Bash 类调用
- 下一步：保留 Markdown 段落换行，Web UI 按标题 / 列表 / 表格 / 代码块渲染；写进 `HANDOFF.md` 时仍压成一行
- 未解决报错：错误的工具结果
- 状态：末尾连续 ≥2 条错误 → `blocked`；末条 assistant 文本在最后一次命令之后出现「测试通过/验证通过/PASS」等且无否定与失败词 → `done`；否则 `doing`
- 会话文件静默超过 `settle_idle`（默认 5 分钟）才出卡；先被扫到但还没写完的会话，等静默后的下一次扫描会补出卡，不会覆盖已有卡

**任务分两类**（`task.kind`），一次 agent 对话就是一条**对话任务**：

- `kind=session` 对话任务：扫描按会话幂等生成，一条会话一条，`task.session_id` 是唯一权威绑定（部分唯一索引保证不会重复）。目标取会话标题或摘要卡的目标；步骤优先用 agent 自己的 Todo——Qoder 解析会话里的 `TaskCreate`/`TaskUpdate`（按 id 取最后状态，`source_key=qt:<id>`），WorkBuddy 读 `tasks/<会话>/*.json`；两边都没有才从摘要卡反推（命令/改动 → 已完成，下一步/未决问题 → 待办，报错 → 受阻，上限 12 条）。
- `kind=formal` 合并任务：桌面端或 MCP 显式新建，或用「合并到…」把几条对话任务并成一条。跨对话收尾看这个。
- 扫描不再把新会话折叠进「同项目同 owner 的进行中任务」（旧 `FindAttachableTask` 行为已删除）；想关联到合并任务请显式合并。
- **人工状态覆盖层**：在详情页把某个**稳定 key** 的步骤改成 done/blocked 等，会写进 `task_step_override`（按 `task_id + source_key`），扫描照常刷新步骤文本、展示时叠加人工状态。反推步骤的 key 是 `cmd:0` 这类序号，不接受单独改状态（只能改整条任务状态或编辑步骤文本）。
- **分层 freeze**：改状态 / owner / 下一步 / 归档 **不再**冻结扫描同步；只有改目标或整组步骤文本才置 `manual_edited_at`，之后扫描只更新片段。MCP `task_update` 同此语义，另支持 `step_status`（按 `source_key` 改单个步骤）与步骤里的 `source_key`。
- 任务可以关联多场会话（`task_session`）；完成满 30 天自动归档，归档任务从活动列表和简报隐藏，可在任务页恢复。
- 任务页排序默认「最近对话」：有会话或片段时只看对话时间 = `MAX(关联会话最新 updated_at, 片段事件最新 ts)`；两者都没有（手工新建的合并任务）才退回 `task.updated_at`。本体的更新时间会被扫描刷成扫描时间，所以不参与排序，否则等于按扫描时间排。可选 `created`（按创建）/ `idle`（最久没动排在最前）。筛选条件一改就重新查询并写进 URL（`kind`、`q`、`project_key`、`status`、`owner_agent`、`source_agent`、`updated_after`、`sort`、`include_archived`），前进后退都保持。
- 配置 LLM 后，记忆与任务会在写入/同步后异步生成中文摘要；原文始终保留。列表优先显示摘要并标「AI 摘要」，任务详情同时能看到摘要与原始目标/下一步，失败时自动退回规则字段。

**从 schema_version 1 升级**：`Open` 会自动跑一次迁移（给 `task` 加 `kind`/`session_id` 并建索引），
把「只关联一场会话且从未手工编辑」的老任务转成对话任务，其余保持合并任务；不删任何数据，重复打开安全。

**简报**固定顺序，内容来自库里的记忆与任务（优先合并任务）：
1. 项目概况（project_key、路径、记忆与任务数）
2. 用户偏好（全局，永不裁剪）
3. 项目硬约束与约定（永不裁剪）
4. 进行中的任务（优先合并任务；一条合并任务都没有才退到最近的对话任务。目标 / 状态 / owner / 步骤进度 / 当前步骤 / AI 摘要或下一步 / 文件 / 命令 / 报错）
5. 未决问题
6. 共享记忆入口（CLI 与 MCP 用法提示）

没有合并任务的旧库会回退显示最近任务卡，保证兼容。

预算默认 4000 token。超预算时依次：任务详情降级 → 任务数减少 → 省略任务与未决问题整段；偏好、硬约束、入口提示保留。

---

## 10. Web UI

```bash
agent-memory serve --port 47123      # 先启动
# 浏览器打开：
http://127.0.0.1:47123/?token=<~/.agent-memory/token 的内容>
```

现在是小而完整的多页面结构，地址栏里直接能看到自己在哪：

| 页面 | 地址 | 内容 |
| --- | --- | --- |
| 总览 | `/` | 统计、项目索引、用户级记忆与新增 |
| 项目档案 | `/p/<项目key>` | 概况、项目记忆、用户记忆、任务（对话 + 合并）、简报、来源、会话 |
| 任务 | `/tasks` | 对话任务与合并任务，可切换视图、排序与归档 |
| 任务详情 | `/t/<任务id>` | 编辑目标/状态/owner/下一步/步骤，查看对话片段与来源会话 |
| 新建任务 | `/t/new` | 选择项目并创建一条合并任务 |
| 记忆档案 | `/memories` | 全部记忆，按关键词/范围/项目/类型/过时筛选，可展开更多 |
| 来源 | `/sources` | 所有记忆文件与会话文件，可只看失败 |
| 编辑记忆 | `/m/<记忆id>` | 独立编辑页，保存/取消后回到来源页 |
| MCP 接入 | `/mcp` | 当前端口/令牌、Qoder/OpenCode/Claude/通用 stdio 配置与一键复制 |
| 设置 | `/settings` | 服务与托盘、LLM 摘要、扫描与简报、数据备份/导出/导入，左侧分区切换 |

- token 会存进 `sessionStorage` 并从地址栏抹掉；同一标签页里点页面链接不需要重复带令牌。直接打开某个地址（没有令牌）会显示「需要访问令牌」。
- 首页五个数字都可下钻：项目跳到项目索引；记忆/过时进「记忆档案」；来源进「来源」；任务进「任务」。
- 项目档案是「左栏目录 + 右栏正文」：左栏是项目名、路径、记忆/任务/来源统计和七个区块的锚点；右栏按 01–07 编号展示，项目记忆与用户级记忆分开。
- 记忆条目是账本行：左侧细线按类型着色，点整行进独立编辑页；条目正文按 Markdown 渲染并折叠预览，长内容点进编辑页看全文；项目页只展示前 10 条，点「查看全部」进 `/memories?project=...`。
- 记忆编辑：独立页面，左侧改正文/类型、右侧实时 Markdown 预览；可保存、取消、标过时/恢复；编辑成与已有条目相同的内容会自动合并并提示。来自 Qoder/Trae 文件的记忆提示「源文件变化后编辑可能被覆盖」。保存后回到进入编辑页时的列表/项目页。
- 任务页 `/tasks`：**视图**（全部【默认】/ 对话任务 / 合并任务）+ 关键词 + 项目 + 状态 + owner + 来源 + 时间范围 + **排序**（最近对话 / 最近创建 / 最久没动）筛选，可切换显示归档；条件一改就出结果并写进 URL，刷新/前进后退保持，「重置」清空条件。列表标题旁会显示命中条数与当前排序口径。默认「全部」是因为 agent 通过 MCP `task_add` 建的是合并任务，只给对话任务视图会把它们藏起来。项目页的任务区只显示当前项目的活动任务。
- 任务详情 `/t/<id>`：顶部常驻任务头（目标/状态/owner/下一步，可编辑）+ 四个 tab：**最近对话**（库优先最近 20 条，`加载更早` 按字节窗口回溯源 jsonl，源不可读回退黄条）、**进行中的 task**（进度条 + 步骤列表，稳定 key 可改状态）、**任务文件**（按文件聚合最近时间/次数/来源）、**任务交接**（极简接手版 + 折叠完整版，各带复制）。tab 写进 `?tab=`，刷新与前进后退保持。可归档/恢复、合并重复任务。
- 任务与交接简报：正文按 Markdown 渲染（标题、列表、表格、代码块）；任务「下一步」过长会折叠，可点「展开全文」。
- 来源页列出 agent、文件路径、最后扫描时间、大小与失败原因；项目页只列给该项目贡献过记忆的来源。
- 同步：自动扫描默认开启（serve 启动先扫一次，之后 fsnotify + 30 秒轮询兜底）；顶部「立即同步」手动触发，完成后刷新当前页。MCP 不是同步前置条件，而是 AI 主动读写共享库的通道。
- 页面使用真实的页面跳转（浏览器前进/后退可用），不再用弹层/抽屉；支持跨文档 View Transition（Chromium 126+，其它浏览器自动退化为普通跳转）。
- 视觉：深色极光 + 玻璃面板 + 渐变大字；默认深色，右上角可切浅色（会记住选择）；窄屏单列；系统开启「减少动态效果」时关闭动效。
- 安全：页面壳本身不含数据与令牌，可以公开；所有数据接口仍需 Bearer，写接口额外校验 `Origin`；普通动态文本走 `textContent`，Markdown 预览先转义再交给 `DOMParser`，不执行原始 HTML 或脚本。
- MCP 页 `/mcp`：显示当前实际端口、启动程序路径与掩码令牌；四个标签页给出 Qoder、OpenCode、Claude Code 与通用 stdio 的完整配置，「一键复制当前配置」复制的内容含真实 token。改端口后要回该页重新复制。
- 设置页 `/settings` 分四区，各自独立保存：
  - 服务与托盘：端口、托盘关闭行为、开机自启、MCP 启动程序路径。改端口会先重写已登记 agent 的 MCP 配置，再在进程内切换监听；新端口绑定失败会回滚配置与 MCP 文件。页面会自动跳到新端口。
  - LLM 摘要：启用开关、OpenAI 兼容 `base_url`/模型、超时、最大输入、API Key（DPAPI 保存，不回显）；可测试连接、重试失败摘要，并显示 pending/ready/failed 计数。
  - 扫描与简报：扫描间隔、三家来源开关、简报预算/卡片数/静默阈值；保存后热重载扫描器。
  - 数据：数据目录、库大小、最近备份；一键备份、打开数据目录、导出 JSON、导入预览与合并。导入前自动备份。
- 截图见 `docs/superpowers/specs/assets/project-console/`。

---

## 10.5 桌面应用（不想用命令行就看这节）

> 想要**逐屏图解**的版本（每个页面截图 + 编号标注、每个设置项改了会发生什么、典型工作流与排错对照表），
> 看 [docs/client-guide.md](client-guide.md)。本节是要点摘录。

双击 `agent-memory-app.exe`，会打开一个标题为「agent-memory 共享记忆」的窗口，内容与 Web UI 相同。不需要浏览器、不需要记 token、不需要输命令。

本节描述的是 Windows 行为；macOS / Linux 的桌面能力差异见第 2.5 节。

启动时它自己完成这些事：

1. 没有 `~/.agent-memory/config.toml` 或库就自动创建；
2. 检查后台服务是否已在运行——在运行就复用，没有就用配置 `[server] port`（默认 47123）拉起一个；
3. 等健康检查通过后打开窗口，token 自动注入。

生命周期：

- **关掉窗口是否退出由设置决定**：设置 → 服务与托盘里 `close_to_tray` 默认开启，关窗后隐藏到系统托盘，后台服务继续跑，Qoder 等 agent 的 MCP 调用照常工作；关闭后关窗即停止 serve 并退出桌面进程。每次关窗都会重新读取配置，改完不需要重启应用。
- **再次双击 exe 不会起第二个实例**：窗口开着就提到前台，窗口已关闭就重新打开。后启动的进程发个通知就自己退出，不会再弹「已在运行」。
- 窗口位置和大小会自动记住（`~/.agent-memory/window.json`），下次打开还原到上次的位置。
- 如果配置端口（默认 47123）被别的程序占用，会弹窗说明（不会静默换端口，否则 MCP 配置会对不上）。

托盘操作：

- **左键单击**托盘图标：打开/唤起主窗口（双击同理）。
- **右键单击**托盘图标：弹出菜单。

| 菜单项 | 作用 |
| --- | --- |
| 打开界面 | 显示/唤起主窗口 |
| 立即同步 | 立即扫描一次所有 agent 的源（等价于页面上的「立即同步」） |
| 打开日志 | 用记事本打开 `~/.agent-memory/app.log` |
| 开机自动运行 | 勾选后写入 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`，登录后以托盘模式启动（不弹窗） |
| 停止服务并退出 | 真正退出：关闭窗口、停掉后台服务、释放当前端口、移除托盘图标 |

页面右上角的「设置」里也有「开机自动运行」开关，与托盘菜单是同一份状态；托盘动作与重新开窗会从 `port` 文件读取当前端口，设置页换端口后无需重启桌面应用。

快捷方式：`agent-memory-app.exe --install-shortcuts` 会在桌面和开始菜单各创建一个 `agent-memory.lnk`；`--uninstall-shortcuts` 删除它们。快捷方式图标取自 exe 内嵌图标。

如果机器没有 WebView2 运行时，会自动改用 Edge/Chrome 的「应用窗口」模式，功能一致，只是任务栏图标显示为浏览器。

出错时：

- 弹窗给出原因；
- 详细日志在 `~/.agent-memory/app.log`。

升级 exe：

1. 托盘菜单选「停止服务并退出」（或任务管理器结束所有 `agent-memory-app.exe`）；
2. 覆盖 `agent-memory-app.exe`；
3. 重新双击。配置、记忆库、托盘与开机自启设置都不受影响。

## 11. MCP 接入

```bash
./agent-memory.exe serve --port 47123                 # 常驻
./agent-memory.exe link qoder --project . --dry-run   # 先看计划
./agent-memory.exe link qoder --project .             # 写入
```

写入的配置（Qoder 示例，`~/.qoder-cn/mcp.json`）：

```json
{
  "mcpServers": {
    "agent-memory": {
      "command": "F:\\go\\agent-memory\\agent-memory.exe",
      "args": ["mcp", "--port", "47123"],
      "env": { "AGENT_MEMORY_TOKEN": "<token>" }
    }
  }
}
```

- OpenCode 用 `type: "local"` + `command` 数组 + `environment`；Claude Code 写 `~/.claude.json` 的 `mcpServers`。
- 不想敲命令：打开桌面端左侧「MCP 接入」，选对应客户端标签，点「一键复制当前配置」，粘贴进客户端配置文件即可。页面生成的是当前实际端口与真实 token；Cursor、Codex、DeepSeek 等没有内置模板的客户端用「通用 stdio」示例改键名。
- 改完要**重启 agent** 才会加载 MCP 配置。
- 撤销：`agent-memory unlink qoder --project .`（区块与配置键都会移除，其余键不动）。

提供的 MCP tools：

| tool | 用途 |
|---|---|
| `memory_search` | 检索（`query` 必填；可 `project_path`/`type`/`scope`/`include_stale`/`limit`） |
| `memory_add` | 写入（`text`/`type` 必填；可 `scope`/`project_path`/`supersedes`） |
| `memory_list` | 浏览条目 |
| `handoff_get` | 取项目交接简报 |
| `task_list` / `task_get` | 任务列表 / 详情（含步骤、片段、来源会话）；`task_list` 可带 `kind=session|formal` |
| `task_add` | 新建合并任务（`kind=formal`），可带步骤 |
| `task_update` | 更新目标/状态/下一步/owner/归档；`step_status`（按 `source_key` 改单步状态，不冻结）；`steps` 整组替换文本（会冻结扫描同步） |
| `task_sync` | 把指定会话立刻同步成对话任务（不等静默窗口）；`session_id` 可省，缺省取该项目最近修改的会话 |
| `task_export` | 导出交接 Prompt（带同一个 task_id） |
| `project_resolve` | 路径 → project_key |
| `stats` | 库统计 |

工具错误（含未知工具、参数错、后端失败）以 `result.isError` 返回，便于 agent 自行修正。

---

## 12. HTTP API

全部需要 `Authorization: Bearer <token>`；写接口额外校验 `Origin`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/`、`/p/<key>`、`/m/<id>`、`/tasks`、`/t/<id>`、`/memories`、`/sources`、`/mcp`、`/settings` | Web UI 页面壳（公开，不含数据与令牌） |
| GET | `/v1/healthz` | 健康检查（免鉴权） |
| GET | `/v1/stats` | 统计 |
| GET | `/v1/projects` | 项目列表 |
| POST | `/v1/search` | `{query, project_path?, project_key?, type?, scope?, include_stale?, limit?}` |
| POST | `/v1/memory` | `{text, type, scope?, project_path?, agent?}` |
| GET | `/v1/memory` | `?project_path=&include_stale=` |
| POST | `/v1/memory/stale` | `{id, stale}` |
| GET | `/v1/handoff` | `?project_path=|project_key=&format=json\|markdown` |
| POST | `/v1/handoff` | `{project_path, write}` |
| GET | `/v1/task` | 会话摘要卡：`?project_path=&status=&limit=` |
| GET | `/v1/tasks` | 任务：`?kind=session\|formal=&project_key=&status=&q=&owner_agent=&source_agent=&updated_after=&updated_before=&include_archived=&sort=created\|idle=&limit=&offset=`（不传 `kind` 返回两类；不传 `sort` 按「最近对话」） |
| GET | `/v1/tasks/item` | `?id=`，任务、步骤、片段、来源会话 |
| GET | `/v1/task/conversation` | `?task_id=&before=&limit=`：`before=0` 返回库里最近片段；`before=文件偏移` 按字节窗口回溯源 jsonl，源不可读回 `source=store_only` |
| GET | `/v1/task/files` | `?task_id=`，按文件聚合的改动（路径/次数/最近时间/来源 agent） |
| POST | `/v1/tasks/step/status` | `{task_id, source_key, status}`，写步骤状态覆盖，不冻结扫描 |
| POST | `/v1/tasks` | `{project_path, goal, status?, next_steps?, owner_agent?, steps?}`，建出的任务 `kind=formal` |
| POST | `/v1/tasks/update` | `{id, goal?, status?, next_steps?, owner_agent?, archived?, steps?}` |
| POST | `/v1/tasks/merge` | `{source_id, target_id}` |
| GET | `/v1/tasks/export` | `?id=`，交接 Prompt Markdown |
| POST | `/v1/project/resolve` | `{path}` |
| POST | `/v1/scan` | 触发一次扫描 |
| GET | `/v1/mcp/snippets` | 当前端口/token 掩码与四种 MCP 配置 |
| GET | `/v1/settings` | 服务、托盘、扫描、LLM、数据概览（不含密钥） |
| POST | `/v1/settings/service` | `{port, autostart?, close_to_tray?, mcp_binary_path?}` |
| POST | `/v1/settings/scan` | `{interval_seconds, budget_tokens, recent_cards, settle_idle_seconds, *_disabled?}` |
| POST | `/v1/settings/llm` | `{enabled, base_url, model, timeout_seconds?, max_input_chars?, api_key?, clear_api_key?}` |
| POST | `/v1/settings/llm/test` | 调用一次模型测试连接 |
| POST | `/v1/settings/llm/retry` | 把 failed 摘要重新排队 |
| POST | `/v1/data/backup` | `VACUUM INTO` 备份，保留最近 10 份 |
| GET | `/v1/data/export` | 下载项目/记忆/合并任务（含步骤与就绪摘要）JSON；对话任务不导出，目标机扫描会重建 |
| POST | `/v1/data/import` | multipart `file`，返回 `import_token` 与预览 |
| POST | `/v1/data/import/apply` | `{import_token}`，导入前自动备份 |
| POST | `/v1/data/open` | 打开数据目录 |
| POST | `/mcp` | MCP JSON-RPC（stdio 桥的目标） |

请求体上限 1MB；`/v1/search` 省略项目时只查用户级记忆。

---

## 13. 只读边界与安全

**只读**：Qoder 的记忆 md 与会话 jsonl、Trae 的记忆/规则 md 与 workspaceStorage、WorkBuddy 的 `MEMORY.md`、`projects/` 会话与 `tasks/` 任务清单一律只读；没有任何写路径指向 `~/.qoder-cn`、`~/.trae-cn`、`~/.workbuddy`、`~/.config/opencode`、`~/.claude`（`handoff` 还会显式拒绝这些目录）。

**写点只有**：
1. 数据目录：`config.toml`、`memory.db`、`token`、`port`
2. 项目内 `.agent-memory/HANDOFF.md` 与 `.git/info/exclude`
3. `link`/`unlink` 显式改动的 MCP 配置与项目内受管区块（写前备份）

接入时 `mcp.json` 里的 token 是明文（本机文件，权限受限于目录）；`serve` 只监听回环且要求 Bearer。若本机多用户，注意数据目录与库里含你的偏好与项目约束（库文件权限 0600，目录 0700）。

---

## 14. 故障排查

| 现象 | 原因与处理 |
|---|---|
| `agent-memory serve 未运行` | 先起 `serve`；`link` 需要它读端口。`~/.agent-memory/port` 只在运行期存在 |
| Web UI 提示缺少令牌/401 | token 不对或该标签页没带过令牌：`cat ~/.agent-memory/token`，用 `/?token=` 重开一次 |
| MCP 调用全部失败 / 401 | 配置里的 token 与 `~/.agent-memory/token` 不一致（重置过 token 或换机器），重跑 `link`；改完重启 agent |
| MCP 工具列表为空 | agent 没重启，或桥的端口不是当前 serve 端口；核对 `mcp.json` 的 `--port`（设置页改端口后应自动重写；失败会有提示） |
| 设置页改端口后页面打不开 | 新端口被占用时会回滚到旧端口并提示；确认没有别的程序占用，或换一个 1024–65535 的端口 |
| LLM 摘要一直 pending | 检查启用开关、`base_url`/`model` 与 API Key 是否齐全（缺一项不会启动 worker）；在设置页点「测试连接」定位网络或鉴权问题 |
| 摘要 failed | 失败最多自动重试 3 次；修好模型配置后点「重试失败摘要」重新排队。原文与规则字段始终可用 |
| `serve` 启动报「已在运行」 | 已有实例占用该端口；先停止它，或换端口后重新 `link` |
| 扫描后记忆没变 | 很正常：内容哈希未变即幂等跳过。需要强制重解析用 `scan --full` |
| 某条记忆不出现 | 可能已 stale（`memory search --include-stale` 查看）；或落在低置信项目里（`doctor` 的未合并项目） |
| 简报被裁成四节、没有任务卡 | 预算不够。调大 `[handoff] budget_tokens` 或用 `--budget` |
| 任务卡迟迟不出 | 会话文件未静默满 `settle_idle`；或会话没有真实用户文本（只有工具调用） |
| 同一个文件出现新旧两版记忆 | 旧版应已被标过时；若没有，用 `memory search --include-stale` 确认，必要时 `memory purge --yes` |
| `.git status` 里出现 `AGENTS.md` | 这是 `link` 安装的受管区块，是否提交由你决定 |
| 想看某个源为什么失败 | `doctor` 的「解析失败的源」；修好后 `scan --full` |
| 提示「拒绝写入 agent 原生目录」 | 该项目的 `primary_path` 落在 agent 配置目录里，属于保护行为，跳过即可 |
| 双击桌面应用窗口没出现 | 看 `~/.agent-memory/app.log` 末尾；常见原因是 47123 被占用（会弹窗），或 WebView2 运行时缺失（会自动改用 Edge/Chrome 应用窗口） |
| 关掉桌面窗口后 agent 用不了记忆 | 默认关窗口只是隐藏到托盘，服务应继续在后台跑（`netstat -ano | findstr 47123` 应能看到 LISTENING）；若在设置里关了 `close_to_tray`，关窗会停服务并退出，这是预期行为 |
| 托盘图标不见了 | 在任务栏通知区展开「隐藏的图标」里找；Windows 有时会把新图标默认收起来 |
| 桌面应用与命令行 serve 谁在跑 | 两者是同一个服务，优先复用 `port` 文件里的现有实例；桌面端启动时使用配置 `[server] port` |
| 开机自启没生效 | 托盘菜单勾选后，`HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 里应有 `agent-memory` 值（`reg query "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v agent-memory`）；安全软件可能拦截注册表写入 |
| 想彻底卸载 | 托盘 → 停止服务并退出；`--uninstall-shortcuts`；取消开机自启；删除 exe 与 `~/.agent-memory`（库和配置都在里面，删前先备份） |
| 升级 exe 时提示「文件被占用」 | 桌面应用拉起的服务就是 `agent-memory-app.exe --serve`，它锁着自身二进制。托盘菜单选「停止服务并退出」（或任务管理器结束所有 `agent-memory-app.exe`），再替换 exe |

---

## 15. 已知限制

- 会话事件文本入库截断到 500 字符；不保存完整 transcript
- 任务状态与摘要由规则抽取或人工维护，不理解语义；30 天归档只看完成时间，不判断任务是否真的结束
- 任务片段最多保留最近 20 条、单条 500 字；不保存完整 transcript，需要全文时回源会话文件
- 检索是字面二元组，跨标点短语会漏（见第 8 节）
- 纯「删除文件 → 用完全相同内容恢复」若从未观察到读错误，条目会一直 stale（`scan --full` 或 `memory purge` 可处理）
- `last_seen_at` 语义是「最后变更」而非「最后见到」
- Trae 只有记忆、没有本地会话：Trae 读 `memory/`、`memory/projects/*/project_memory.md` 与 `user_rules/`，会话正文在云端，`state.vscdb` 只有界面状态，所以不产生任务；WorkBuddy 除 `~/.workbuddy/MEMORY.md` 外，还导入 `projects/*.jsonl` 会话：带 `tasks/` 的导入完整步骤和全部历史，没有清单的只导入最近 30 天；两者不接 MCP，靠项目里的 `HANDOFF.md` 读取共享记忆
- WorkBuddy 不参与文件监听（`~/.workbuddy` 根目录有大量应用缓存，监听会放大事件量），记忆、会话与任务清单的变化由 30 秒轮询兜底；Trae 只监听 `memory/` 与 `user_rules/`，`workspaceStorage` 的变化不触发扫描
- OpenCode 的会话/记忆与 Trae 的云端会话尚未接入
- `git worktree` / submodule 的 `.git` 是文件，目前读不到 remote，会退化为路径 key
- 单机单库；多机同步与全文 transcript 检索属于非目标
- 桌面体验是 Windows 优先：macOS / Linux 上没有 WebView2 窗口、系统托盘、开机自启、快捷方式与单实例保护，LLM 密钥也不再加密存储（见第 2.5 节）。CLI、`serve`、Web UI、HTTP API 与 MCP 三家平台一致
- Trae 的 `workspaceStorage` 索引按 Windows 的 `%APPDATA%` 推导；macOS / Linux 上需要显式配 `[trae] workspace_roots`，否则项目记忆只能按目录名猜项目，映射不回真实路径
- LLM 摘要依赖外部 OpenAI 兼容服务；未配置时所有功能照常可用，只是没有「AI 摘要」标记。摘要不替代原文，硬约束/偏好在简报里仍使用原文
- 导入会按项目 key、记忆归一化文本、任务目标合并；导入前自动备份，但写库过程中若遇到磁盘/SQLite 故障，需用备份手动恢复（接口会返回错误）

---

## 16. 开发与测试

```bash
go test ./... -count=1     # 449 个测试函数
go vet ./...
go build ./cmd/agent-memory
```

Windows 上仓库配置了 `core.autocrlf=true`，工作区是 CRLF，`gofmt -l .` 会把所有文件标成差异（假阳性）。检查格式用索引内容：

```bash
git show :internal/store/memory.go | gofmt -d
```

数据库 schema 版本记录在 `meta.schema_version`（当前 `3`）。版本低于二进制时 `Open` 会自动迁移（`1→2` 给 `task` 补 `kind`/`session_id` 并建索引；`2→3` 新增 `task_step_override` 表，只建表不动数据）；高于二进制时拒绝打开并提示升级程序，不会静默降级。

### 交叉编译

驱动是纯 Go 的 `modernc.org/sqlite`，`CGO_ENABLED=0` 即可，无目标机依赖：

```bash
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "-s -w" -o dist/agent-memory-windows-amd64.exe ./cmd/agent-memory
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "-s -w" -o dist/agent-memory-linux-amd64       ./cmd/agent-memory
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "-s -w" -o dist/agent-memory-linux-arm64       ./cmd/agent-memory
CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "-s -w" -o dist/agent-memory-darwin-amd64      ./cmd/agent-memory
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "-s -w" -o dist/agent-memory-darwin-arm64      ./cmd/agent-memory
```

`internal/desktop` 的 Windows 专属能力（WebView2、托盘、命名互斥量单实例、DPAPI、快捷方式、开机自启、窗口位置记忆）都在 `*_windows.go` 里，`*_other.go` 给出降级实现，所以非 Windows 也能编译并跑 CLI/服务。
改完 `internal/desktop` 建议顺手交叉验证，静态检查按目标平台跑：

```bash
GOOS=darwin CGO_ENABLED=0 go vet ./...
GOOS=linux  CGO_ENABLED=0 go vet ./...
```

### 出 Windows 安装包

```powershell
powershell -ExecutionPolicy Bypass -File scripts\package.ps1
# 可选：-SkipTests 跳过测试，-SkipBuild 复用已编译的 out\*.exe
```

脚本依次做：`go test ./...` → 以 `CGO_ENABLED=0` 静态编译 `out\agent-memory.exe`（CLI）与
`out\agent-memory-app.exe`（GUI，`-H windowsgui`）→ 从 `internal/cli/root.go` 读版本号 →
把两个 exe、`README.md`、`docs\usage.md`、`docs\client-guide.md` 与图片摆进 `out\pkg\` →
调 `makensis`（自动找 Program Files 或 electron-builder 缓存）编译 `installer\agent-memory.nsi` →
输出 `out\agent-memory-setup-x64.exe` 及其大小与 SHA-256。

要点：安装包是 per-user（`RequestExecutionLevel user`），装到 `%LOCALAPPDATA%\Programs\agent-memory`，
不需要管理员权限；安装/卸载前都会 `taskkill` 两个进程名；卸载只删程序与快捷方式，**保留 `~\.agent-memory`**；
`installer\agent-memory.nsi` 必须保留 UTF-8 BOM，否则 NSIS 3.0.4 编译中文会报 `Bad text encoding`。
