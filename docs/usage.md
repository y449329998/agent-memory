# agent-memory 使用说明

面向使用者的完整手册。设计取舍与实现细节见 `docs/superpowers/specs/`，实施记录见 `docs/superpowers/plans/`。

---

## 1. 它解决什么

你在 Qoder / Trae / WorkBuddy / OpenCode 之间切换时，每个 agent 各存一份记忆与历史，互不可见。agent-memory 把 Qoder 的长期记忆与会话历史、Trae 与 WorkBuddy 的记忆文件读进一个本地共享库：

- 记忆去重后统一检索（中文两字词可命中）
- 会话提炼成任务卡（目标 / 状态 / 改动文件 / 下一步 / 未决问题）
- 每个项目生成 `.agent-memory/HANDOFF.md`，新 agent 接手先读它
- 常驻服务通过 MCP 让 agent 按需读写；Web UI 供人查看与操作

它**只读**各 agent 的原生文件，从不改写。写点只有三处，见第 13 节。

---

## 2. 安装

```bash
cd F:/go/agent-memory
go build -o agent-memory.exe ./cmd/agent-memory          # 命令行与服务
go build -ldflags "-H windowsgui" -o agent-memory-app.exe ./cmd/agent-memory-app  # 桌面应用（双击用，无控制台窗口）
./agent-memory.exe init
```

不用命令行的直接看第 10.5 节：双击 `agent-memory-app.exe` 即可。

`init` 会创建 `~/.agent-memory/config.toml` 与 `memory.db`，并打印发现的 Qoder 目录。若自动发现不到：

```bash
./agent-memory.exe init --data-dir F:/my-data --force
# 或手工在 config.toml 写：
# [qoder]
# home = 'C:\Users\You\.qoder-cn'
```

环境要求：Go 1.22+、Windows/macOS/Linux；本项目在 Windows 10 + Go 1.22.6 上实测（`CGO_ENABLED=0`，使用纯 Go 的 SQLite 驱动）。桌面窗口依赖 Windows 自带的 WebView2 运行时（Win10/11 一般已安装）。

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

`scan` 按 qoder → trae → workbuddy 的固定顺序读三家：Qoder 读 `~/.qoder-cn/memory`、`projects/*/memory` 与会话 jsonl；Trae 读 `~/.trae-cn/memory`（含 `projects/<项目>/project_memory.md`）与 `user_rules/`，并用 `%APPDATA%\Trae CN\User\workspaceStorage` 把项目目录名映射回真实路径；WorkBuddy 只读 `~/.workbuddy/MEMORY.md`。Trae 与 WorkBuddy 都没有会话可导入。

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
- `--port 0` 系统分配随机端口；**换端口后要重新 `link`**（MCP 配置里写死了端口）。
- `--poll` 轮询兜底间隔（0 表示用配置 `[scan] interval`）；`--debounce` 事件去抖窗口。
- 监听目录只覆盖记忆源：qoder 的 `memory/`、`projects/`，trae 的 `memory/`、`user_rules/`（去重后交给 watcher）；WorkBuddy 不监听，只靠 `--poll` 兜底。
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

---

## 6. 数据与文件位置

| 位置 | 内容 | 谁写 |
|---|---|---|
| `~/.agent-memory/config.toml` | 配置 | `init` |
| `~/.agent-memory/memory.db` | 共享库（SQLite，WAL；含 `-wal`/`-shm`） | `serve` / CLI |
| `~/.agent-memory/token` | 访问令牌（0600） | 首次 `serve` / `link` / `mcp` |
| `~/.agent-memory/port` | 运行中 daemon 的端口 | `serve` |
| `~/.agent-memory/app.log` | 桌面应用与它拉起的 serve 的日志 | 桌面应用 |
| `<项目>/.agent-memory/HANDOFF.md` | 交接简报 | `scan` / `handoff` / Web UI |
| `<项目>/.git/info/exclude` | 加一行 `/.agent-memory/` | 首次写简报时 |
| `~/.qoder-cn/mcp.json` 等 | MCP 配置 | **仅** `link`/`unlink` |
| `<项目>/AGENTS.md` 或 `CLAUDE.md` | 受管区块 | **仅** `link`/`unlink` |

Trae 与 WorkBuddy 的原生目录（`~/.trae-cn`、`%APPDATA%\Trae CN\User\workspaceStorage`、`~/.workbuddy`）**只读**：`scan` 只做 `os.ReadDir` / `os.ReadFile` / `os.Stat`，不会在其中创建、修改或删除任何文件。

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

## 9. 任务卡与交接简报

**任务卡**由规则从会话事件抽取（不调用 LLM）：
- 目标：首条真实用户消息；后续用户消息追加
- 改动文件：Edit/Write 类工具调用的文件路径
- 执行命令：Bash 类调用
- 未解决报错：错误的工具结果
- 状态：末尾连续 ≥2 条错误 → `blocked`；末条 assistant 文本在最后一次命令之后出现「测试通过/验证通过/PASS」等且无否定与失败词 → `done`；否则 `doing`
- 会话文件静默超过 `settle_idle`（默认 5 分钟）才出卡；先被扫到但还没写完的会话，等静默后的下一次扫描会补出卡，不会覆盖已有卡

**简报**六节固定顺序，内容只来自库里已有的记忆与任务卡：
1. 项目概况（project_key、路径、记忆与任务数）
2. 用户偏好（全局，永不裁剪）
3. 项目硬约束与约定（永不裁剪）
4. 最近 3 张任务卡（目标 / 状态 / 关键决策 / 改动文件 / 命令 / 下一步 / 报错）
5. 未决问题
6. 共享记忆入口（CLI 与 MCP 用法提示）

预算默认 4000 token。超预算时依次：卡片降级为精简 → 卡片数 3→2→1 → 省略卡片与未决问题整段；偏好、硬约束、入口提示保留。

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
| 项目档案 | `/p/<项目key>` | 概况、项目记忆、用户记忆、任务卡、简报、来源、会话 |
| 记忆档案 | `/memories` | 全部记忆，按关键词/范围/项目/类型/过时筛选，可展开更多 |
| 来源 | `/sources` | 所有记忆文件与会话文件，可只看失败 |
| 编辑记忆 | `/m/<记忆id>` | 独立编辑页，保存/取消后回到来源页 |

- token 会存进 `sessionStorage` 并从地址栏抹掉；同一标签页里点页面链接不需要重复带令牌。直接打开某个地址（没有令牌）会显示「需要访问令牌」。
- 首页五个数字都可下钻：项目跳到项目索引；记忆/过时进「记忆档案」；源/失败源进「来源」。
- 项目档案是「左栏目录 + 右栏正文」：左栏是项目名、路径、记忆/任务/来源统计和七个区块的锚点；右栏按 01–07 编号展示，项目记忆与用户级记忆分开。
- 记忆条目是账本行：左侧细线按类型着色，点整行进独立编辑页；项目页只展示前 10 条，点「查看全部」进 `/memories?project=...`。
- 记忆编辑：独立页面，可改正文/类型、保存、取消、标过时/恢复；编辑成与已有条目相同的内容会自动合并并提示。来自 Qoder/Trae 文件的记忆提示「源文件变化后编辑可能被覆盖」。保存后回到进入编辑页时的列表/项目页。
- 来源页列出 agent、文件路径、最后扫描时间、大小与失败原因；项目页只列给该项目贡献过记忆的来源。
- 同步：自动扫描默认开启（serve 启动先扫一次，之后 fsnotify + 30 秒轮询兜底）；顶部「立即同步」手动触发，完成后刷新当前页。MCP 不是同步前置条件，而是 AI 主动读写共享库的通道。
- 页面使用真实的页面跳转（浏览器前进/后退可用），不再用弹层/抽屉；支持跨文档 View Transition（Chromium 126+，其它浏览器自动退化为普通跳转）。
- 视觉：暖纸/墨色/朱砂的档案馆风格；衬线标题、等宽元数据、细分隔线、轻噪点；浅色/深色跟随系统；窄屏单列；系统开启「减少动态效果」时关闭动效。
- 安全：页面壳本身不含数据与令牌，可以公开；所有数据接口仍需 Bearer，写接口额外校验 `Origin`；动态内容全部走 `textContent`。
- 截图见 `docs/superpowers/specs/assets/project-console/`。

---

## 10.5 桌面应用（不想用命令行就看这节）

双击 `agent-memory-app.exe`，会打开一个标题为「agent-memory 共享记忆」的窗口，内容与 Web UI 相同。不需要浏览器、不需要记 token、不需要输命令。

启动时它自己完成这些事：

1. 没有 `~/.agent-memory/config.toml` 或库就自动创建；
2. 检查后台服务是否已在运行——在运行就复用，没有就用固定端口 `47123` 拉起一个；
3. 等健康检查通过后打开窗口，token 自动注入。

生命周期：

- **关掉窗口不会退出应用**：窗口隐藏到系统托盘，后台服务继续跑，Qoder 等 agent 的 MCP 调用照常工作。
- **再次双击 exe 不会起第二个实例**：窗口开着就提到前台，窗口已关闭就重新打开。后启动的进程发个通知就自己退出，不会再弹「已在运行」。
- 窗口位置和大小会自动记住（`~/.agent-memory/window.json`），下次打开还原到上次的位置。
- 如果 47123 被别的程序占用，会弹窗说明（不会静默换端口，否则 MCP 配置会对不上）。

托盘操作：

- **左键单击**托盘图标：打开/唤起主窗口（双击同理）。
- **右键单击**托盘图标：弹出菜单。

| 菜单项 | 作用 |
| --- | --- |
| 打开界面 | 显示/唤起主窗口 |
| 立即同步 | 立即扫描一次所有 agent 的源（等价于页面上的「立即同步」） |
| 打开日志 | 用记事本打开 `~/.agent-memory/app.log` |
| 开机自动运行 | 勾选后写入 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`，登录后以托盘模式启动（不弹窗） |
| 停止服务并退出 | 真正退出：关闭窗口、停掉后台服务、释放 47123、移除托盘图标 |

页面右上角的「设置」里也有「开机自动运行」开关，与托盘菜单是同一份状态。

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
- 改完要**重启 agent** 才会加载 MCP 配置。
- 撤销：`agent-memory unlink qoder --project .`（区块与配置键都会移除，其余键不动）。

提供的 MCP tools：

| tool | 用途 |
|---|---|
| `memory_search` | 检索（`query` 必填；可 `project_path`/`type`/`scope`/`include_stale`/`limit`） |
| `memory_add` | 写入（`text`/`type` 必填；可 `scope`/`project_path`/`supersedes`） |
| `memory_list` | 浏览条目 |
| `handoff_get` | 取项目交接简报 |
| `task_list` / `task_get` | 任务卡列表 / 单张详情 |
| `project_resolve` | 路径 → project_key |
| `stats` | 库统计 |

工具错误（含未知工具、参数错、后端失败）以 `result.isError` 返回，便于 agent 自行修正。

---

## 12. HTTP API

全部需要 `Authorization: Bearer <token>`；写接口额外校验 `Origin`。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/`、`/p/<key>`、`/m/<id>`、`/memories`、`/sources` | Web UI 页面壳（公开，不含数据与令牌） |
| GET | `/v1/healthz` | 健康检查（免鉴权） |
| GET | `/v1/stats` | 统计 |
| GET | `/v1/projects` | 项目列表 |
| POST | `/v1/search` | `{query, project_path?, project_key?, type?, scope?, include_stale?, limit?}` |
| POST | `/v1/memory` | `{text, type, scope?, project_path?, agent?}` |
| GET | `/v1/memory` | `?project_path=&include_stale=` |
| POST | `/v1/memory/stale` | `{id, stale}` |
| GET | `/v1/handoff` | `?project_path=|project_key=&format=json\|markdown` |
| POST | `/v1/handoff` | `{project_path, write}` |
| GET | `/v1/task` | `?project_path=&status=&limit=` |
| POST | `/v1/project/resolve` | `{path}` |
| POST | `/v1/scan` | 触发一次扫描 |
| POST | `/mcp` | MCP JSON-RPC（stdio 桥的目标） |

请求体上限 1MB；`/v1/search` 省略项目时只查用户级记忆。

---

## 13. 只读边界与安全

**只读**：Qoder 的记忆 md 与会话 jsonl、Trae 的记忆/规则 md 与 workspaceStorage、WorkBuddy 的 `MEMORY.md` 一律只读；没有任何写路径指向 `~/.qoder-cn`、`~/.trae-cn`、`~/.workbuddy`、`~/.config/opencode`、`~/.claude`（`handoff` 还会显式拒绝这些目录）。

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
| MCP 工具列表为空 | agent 没重启，或桥的端口不是当前 serve 端口；核对 `mcp.json` 的 `--port` |
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
| 关掉桌面窗口后 agent 用不了记忆 | 关窗口只是隐藏到托盘，服务应继续在后台跑（`netstat -ano | findstr 47123` 应能看到 LISTENING）；若托盘里也没有图标，看 `app.log` 是否崩溃，再重新双击应用 |
| 托盘图标不见了 | 在任务栏通知区展开「隐藏的图标」里找；Windows 有时会把新图标默认收起来 |
| 桌面应用与命令行 serve 谁在跑 | 两者是同一个服务，端口都是 47123；谁先起就复用谁，不会重复启动 |
| 开机自启没生效 | 托盘菜单勾选后，`HKCU\Software\Microsoft\Windows\CurrentVersion\Run` 里应有 `agent-memory` 值（`reg query "HKCU\Software\Microsoft\Windows\CurrentVersion\Run" /v agent-memory`）；安全软件可能拦截注册表写入 |
| 想彻底卸载 | 托盘 → 停止服务并退出；`--uninstall-shortcuts`；取消开机自启；删除 exe 与 `~/.agent-memory`（库和配置都在里面，删前先备份） |
| 升级 exe 时提示「文件被占用」 | 桌面应用拉起的服务就是 `agent-memory-app.exe --serve`，它锁着自身二进制。托盘菜单选「停止服务并退出」（或任务管理器结束所有 `agent-memory-app.exe`），再替换 exe |

---

## 15. 已知限制

- 会话事件文本入库截断到 500 字符；不保存完整 transcript
- 任务卡是规则抽取，不理解语义；`done` 判定依赖关键词（已排除否定与部分通过）
- 检索是字面二元组，跨标点短语会漏（见第 8 节）
- 纯「删除文件 → 用完全相同内容恢复」若从未观察到读错误，条目会一直 stale（`scan --full` 或 `memory purge` 可处理）
- `last_seen_at` 语义是「最后变更」而非「最后见到」
- Trae / WorkBuddy 只有记忆、没有会话：Trae 读 `memory/`、`memory/projects/*/project_memory.md` 与 `user_rules/`，WorkBuddy 只读 `~/.workbuddy/MEMORY.md`；两者不产生任务卡、不接 MCP，靠项目里的 `HANDOFF.md` 读取共享记忆
- WorkBuddy 不参与文件监听（`~/.workbuddy` 根目录有大量应用缓存，监听会放大事件量），改动由 30 秒轮询兜底；Trae 只监听 `memory/` 与 `user_rules/`，`workspaceStorage` 的变化不触发扫描
- OpenCode 的会话/记忆与 Trae 的会话（`state.vscdb`，结构未公开）尚未接入
- `git worktree` / submodule 的 `.git` 是文件，目前读不到 remote，会退化为路径 key
- 单机单库；多机同步与全文 transcript 检索属于非目标

---

## 16. 开发与测试

```bash
go test ./... -count=1     # 356 个测试函数
go vet ./...
go build ./cmd/agent-memory
```

Windows 上仓库配置了 `core.autocrlf=true`，工作区是 CRLF，`gofmt -l .` 会把所有文件标成差异（假阳性）。检查格式用索引内容：

```bash
git show :internal/store/memory.go | gofmt -d
```

数据库 schema 版本记录在 `meta.schema_version`（当前 `1`）。版本高于二进制时会拒绝打开并提示重建，不会静默降级。
