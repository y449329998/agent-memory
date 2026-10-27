# agent-memory：多 Agent 共享记忆系统 · 设计文档

日期：2026-10-27
状态：已确认，待实施计划
语言/运行时：Go 1.22+，单二进制

## 1. 背景与目标

在 Qoder、Trae、WorkBuddy、OpenCode、Claude Code 之间切换时，每个 agent 各自维护一份记忆与会话历史，互不可见。切到另一个 agent 就要从零描述任务背景、已做的决策、项目的硬约束。

目标：

1. 把各 agent 的长期记忆归并成一份可检索的共享记忆，并保留来源证据。
2. 把历史会话提炼成任务卡，切换 agent 时自动给出「在做什么、做到哪、下一步、有哪些约束」。
3. 各 agent 通过统一入口（MCP + 项目内简报文件）读取和写入共享记忆。

非目标：

1. 不做多机同步，不做中心服务器；只解决单机本机。
2. 不改写任何 agent 的原生记忆文件，不做双向同步。
3. 不保存完整会话 transcript，不做逐句回放。
4. 不做语义合并/自动裁决矛盾。
5. 第一版不做向量检索、不做 embedding。

## 2. 决策记录

| 决策 | 选择 | 理由 |
|---|---|---|
| 架构定位 | 聚合 + MCP 读写中枢 | 各 agent 继续用自己的记忆机制，我们做归并与按需读写，不回写原生文件，规避同步冲突 |
| 共享粒度 | 记忆 + 任务卡/交接简报 | 满足「不用从头描述任务」，同时避免全文入库带来的体量与噪声 |
| 接入方式 | MCP + 受管上下文区块 | 只靠 agent 主动调工具会漏；项目内文件让新会话默认带上简报 |
| 简报生成 | 规则为主 + 可选 LLM | 规则可解释可测试；LLM 默认关闭 |
| 运行形态 | 本地常驻服务 + stdio 转发 | 单实例持有 SQLite，避免多 agent 并发写与重复扫描 |
| 真相源 | SQLite 为真相 + 导出生成物 | 事务、去重、增量简单；导出保住可读性 |
| 首批范围 | 四家记忆层 + Qoder/OpenCode 会话层 | Trae/WorkBuddy 会话存储格式不稳定，不阻塞主线 |

## 3. 已探明的本机存储现状

设计基于实测路径，不基于猜测：

| Agent | 长期记忆 | 会话历史 | 第一版处理 |
|---|---|---|---|
| Qoder | `~/.qoder-cn/memory/*.md`（用户级）、`~/.qoder-cn/projects/<slug>/memory/` | `~/.qoder-cn/projects/<slug>/<session-uuid>.jsonl` + `state.json` | 记忆 + 会话 |
| OpenCode | `~/.config/opencode/AGENTS.md` | `~/.local/share/opencode/opencode.db`（SQLite，含 `-wal`）、`storage/session_diff/*.json` | 记忆 + 会话 |
| Trae | `~/.trae-cn/memory/user_profile.md`、`~/.trae-cn/memory/projects/<slug>/project_memory.md` | 疑似 `AppData/Roaming/Trae CN/User/**/state.vscdb`，键结构未公开 | 仅记忆；会话待 `doctor` 探测 |
| WorkBuddy | `~/.workbuddy/MEMORY.md`、`USER.md`、`SOUL.md`、`app/sessions.json`（仅索引，含 workDir） | 无明文，疑似 IndexedDB/LevelDB 或远端 | 仅记忆；只产出 HANDOFF.md，不动其配置 |
| Claude Code | `~/.claude/CLAUDE.md` | `~/.claude/projects/`（本机当前为空） | 预留适配器 |

Trae/WorkBuddy 的 `project_memory.md` 与 `MEMORY.md` 均为带固定小节的 Markdown（如 `## Hard Constraints`、`## Engineering Conventions`、`## Lessons Learned`、`## User Preferences`），按小节标题映射记忆类型。

## 4. 总体架构

### 4.1 进程形态

单个 Go 二进制，三个入口：

- `agent-memory serve`：常驻守护进程。独占 SQLite，监听 `127.0.0.1` 的 HTTP + MCP 端点，内置扫描器与文件监听。
- `agent-memory mcp`：stdio 转发器，把 stdin/stdout 上的 JSON-RPC 转发给守护进程，供只支持 stdio 的 agent 使用。守护进程未运行时立即报错并提示 `agent-memory serve`，不做静默自启。
- 一次性命令：`init` / `scan` / `import` / `handoff` / `memory` / `link` / `unlink` / `doctor` / `export`。

### 4.2 模块划分

每个模块一个职责，通过接口通信，可独立测试：

1. **Adapter**（`qoder`、`opencode`、`trae`、`workbuddy`、`claude`）：把某个 agent 的原生存储解析为 `MemoryItem` / `SessionEvent`。只解析，不碰数据库，不知道其他 agent 的存在。接口：`Name() / Discover() / ScanMemories(ctx) / ScanSessions(ctx)`。
2. **Ingest**：fsnotify 去抖 + 30s 兜底轮询 → 调用 Adapter → 归一化项目身份 → 按内容哈希幂等 upsert。
3. **Store**：SQLite 仓储层，全进程唯一写入口。
4. **Extractor**：从 `SessionEvent` 按规则产出任务卡；可选 LLM 仅润色两个自由文本字段。
5. **Composer**：纯函数 Store → 交接简报 markdown，带 token 预算与裁剪优先级。
6. **Surface**：MCP tools、HTTP API、`.agent-memory/HANDOFF.md`、AGENTS.md/CLAUDE.md 受管区块。

### 4.3 数据流

```
agent 原生文件 ──watch/轮询──> Ingest ──> Store(SQLite, WAL)
                                    └──> Extractor ──> task_card
Store ──> Composer ──> <project>/.agent-memory/HANDOFF.md
                    └─> AGENTS.md / CLAUDE.md 受管区块（仅 link 时写）
agent 新会话 → 读 AGENTS.md 区块 → 读 HANDOFF.md 或调 MCP
agent 学到新记忆 → MCP memory_add → Store → 下次合成生效
```

### 4.4 边界约束

- 只读 agent 原生文件；对它们没有任何写路径。
- 写入只有两处：项目根 `.agent-memory/`，以及 `link` 显式安装的 MCP 配置与受管区块。
- 守护进程只刷新 `HANDOFF.md`，永不主动改动 `AGENTS.md`/`CLAUDE.md`；改动区块必须由 `link` / `unlink` 触发。
- `.agent-memory/` 默认不入库：项目是 git 仓库时只写 `.git/info/exclude`（仅本地生效，不动被提交的 `.gitignore`）；非仓库目录无需处理。

## 5. 数据模型

数据库位置：`~/.agent-memory/memory.db`，WAL 模式。

| 表 | 关键字段 | 作用 |
|---|---|---|
| `project` | key, display_name, git_remote, primary_path, created_at, updated_at | 跨 agent 的项目身份 |
| `project_path` | project_id, agent, path, slug | 各 agent 的 slug/路径 → 同一 project |
| `source` | agent, kind(memory/session), path, content_hash, mtime, size, last_error, updated_at | 增量扫描游标与失败现场 |
| `memory_item` | project_id(NULL=用户级), scope, type, text, text_norm_hash, stale, first_seen_at, last_seen_at | 归并后的记忆条目 |
| `memory_source` | memory_id, agent, ref, quote | 一条记忆的多来源证据 |
| `session` | agent, external_id, project_id, title, started_at, updated_at, event_count, path | 会话索引 |
| `session_event` | session_id, seq, role, kind, text(截断 500 字), tool, files_json, ts | 抽取信号，不存完整 transcript |
| `task_card` | session_id, project_id, goal, status, decisions_json, files_json, commands_json, errors_json, open_questions, next_steps, confidence, generated_by, created_at, updated_at | 交接最小单元 |
| `handoff` | project_id, body, content_hash, budget_tokens, generated_at | 每项目保留最近一版 |
| `agent_link` | project_id, agent, block_file, installed_hash, installed_at | 受管区块登记 |

`memory_type` 取值：`preference`、`constraint`、`convention`、`lesson`、`fact`、`goal`。Trae/Qoder 的固定小节标题按此映射。

FTS5 虚表 `memory_fts(text)` 关联 `memory_item`，bm25 排序。

### 5.1 项目身份

`project_key` 生成优先级：

1. git remote origin URL 归一化（去协议、去 `.git`、大小写与 slash 统一）。
2. 无 remote 时用规范化绝对路径（盘符大写统一、分隔符统一、尾部分隔符去除）。

各 agent 的 slug 无法直接比对，由 `project_path` 表登记映射：适配器在扫描时同时产出该 agent 记录的项目路径（Qoder `state.json` 的 workspace-directories、WorkBuddy `sessions.json` 的 `workDir`、Trae slug 反解出的 `-d-company-xxx` 形式），解析到真实目录后归入同一 `project_key`。

解析不到真实目录的项目保留 agent 原生 slug 作为 key，并在 `doctor` 中列为「未合并项目」，不猜测合并。

### 5.2 去重与冲突

- 文本归一化：NFKC → 去 Markdown 列表符号与前缀 → 折叠空白 → 小写 → 计算 `text_norm_hash`。
- 唯一约束：`(scope, project_id, type, text_norm_hash)`。命中已存在条目时只追加 `memory_source`，并更新 `last_seen_at`，不复制条目。
- 近似重复不落库、不建关系表：只在 `doctor` 和 `memory_search` 结果里按词面重叠率即时计算并列展示，标出来源，由人判断是否合并。
- 源文件中条目消失：置 `stale=1`，不硬删。agent 重写记忆文件时可能只是换措辞，硬删会抹掉结论。仅 `doctor --purge-stale` 或 `memory purge` 真删。
- 矛盾内容不自动裁决，由来源标注交给用户判断。
- MCP `memory_add` 写入的条目 `agent="mcp"`，参与同一套去重。

## 6. 会话抽取与任务卡

### 6.1 规则（以 Qoder JSONL 为例）

- 首条非 slash-command 的 `user` 消息 → `goal`；后续 user 消息 → 目标修正/追加。
- `assistant` 的 Edit/Write 类 tool_use → `files_touched`；Bash → `commands`。
- `tool_result` 含错误 → `errors`；其后同轮成功调用 → `fix_applied`。
- 丢弃 `isSidechain=true`（子 agent）与技能注入的长 prompt；否则技能正文会被当成任务目标。
- 会话静默 >5 分钟视为收尾（以会话源文件的 mtime 与当前时间之差判定），生成或刷新任务卡；末条 assistant 文本进 `next_steps` 候选。
- `status` 判定：末段连续失败且无成功 → `blocked`；出现提交或验证通过类结论 → `done`；其余 → `doing`；无法判定 → `unknown`。

OpenCode 走 SQLite 的 message/part 表，字段映射到同一套 `SessionEvent`；适配实现前先用 `doctor --dump opencode` 输出真实 schema 确认，不按猜测编码。

### 6.2 可选 LLM 增强（默认关闭）

- 只允许重写 `goal` 与 `next_steps` 两个自由文本字段；结构化字段一律由规则产出，避免模型编造「改动了哪些文件」。
- 入库前脱敏：正则匹配 API key/token/密码/JWT，跳过 `credentials`、`~/.ssh`、`*.key` 类路径内容，替换为 `[REDACTED]`。导出时同样过滤。
- LLM 端点走 OpenAI 兼容配置（base_url / model / api_key），失败只降级为规则结果，不阻塞扫描。

## 7. 交接简报

Composer 是纯函数：输入 store 快照 + project_key，输出 markdown。固定六段、顺序稳定以便 diff：

1. 项目概况：project_key、路径、技术栈
2. 用户偏好（全局 scope，永不裁剪）
3. 项目硬约束与约定（`stale=0`，按 type 分组，每条标来源 agent）
4. 最近 3 张任务卡：目标 / 状态 / 关键决策 / 改动文件 / 下一步
5. 未决问题汇总
6. 可用 MCP tools 提示

预算默认 1500 token（约 2500 中文字符）。超限按优先级裁剪：任务卡细节 → 旧任务卡 → 未决问题截断；第 2、3 段不参与裁剪。排序规则固定为 `type` → `last_seen_at desc` → `id`，保证同输入同输出。

输出位置：`<project>/.agent-memory/HANDOFF.md`，同时存入 `handoff` 表。`export` 命令可把库导出为可读 markdown 目录，作为透明性与备份手段。

## 8. 对外接口

### 8.1 MCP tools

| tool | 作用 |
|---|---|
| `memory_search(query, project?, type?, scope?, limit)` | FTS 检索，返回条目 + 来源 + 首次/末次时间 |
| `memory_add(text, type, scope?, supersedes?)` | 写入共享记忆，走同一套去重；`supersedes` 把旧条目标 stale |
| `memory_list(project?, include_stale?, limit)` | 浏览 |
| `handoff_get(project?, format?)` | 取当前交接简报 |
| `task_list(project?, status?, limit)` / `task_get(id)` | 任务卡 |
| `project_resolve(path)` | 任意路径 → project_key |
| `stats()` | 各来源条目数、上次扫描时间、失败项 |

Resources 暴露 `agent-memory://project/<key>/handoff`。

### 8.2 HTTP 与鉴权

`serve` 在 `127.0.0.1` 提供与 MCP 同语义的端点供 CLI 使用。启动时生成随机 token 写入 `~/.agent-memory/token`（权限 600），所有请求校验 `Authorization: Bearer`，仅监听回环地址。限制请求体大小与单请求超时。

### 8.3 agent 接入（`link`）

| Agent | MCP 配置 | 受管区块 |
|---|---|---|
| Qoder | `~/.qoder-cn/mcp.json` | 项目 `AGENTS.md` |
| OpenCode | `~/.config/opencode/opencode.json` 的 mcp 段 | `AGENTS.md` |
| Trae | `~/.trae-cn/mcps` | 项目规则文件 |
| Claude Code | `~/.claude.json` | `CLAUDE.md` |
| WorkBuddy | 不写配置 | 仅产出 `HANDOFF.md` |

`link` 是唯一改动既有文件的命令，约束：JSON 合并而非覆盖；改前落 `.bak-<时间戳>`；重复执行结果相同（幂等）；区块用 `<!-- agent-memory:begin -->` / `end` 包裹，内容只有 project_key、指针和刷新时间，正文留在 `HANDOFF.md`；`unlink` 精确还原自身写入内容。

### 8.4 配置

`~/.agent-memory/config.toml`：各 agent 路径与开关、扫描间隔、简报 token 预算、可选 LLM、监听端口、区块目标文件列表。缺省时按第 3 节实测路径自动发现。

CLI 的 `memory` 子命令为 `search` / `add` / `list` / `purge`（`purge` 删除 `stale=1` 条目）。

## 9. 错误处理

- **适配器隔离**：单文件解析失败写 `source.last_error` 并跳过，坏 JSONL 行计数丢弃，不中断整轮扫描；`doctor` 汇总失败项。
- **不阻塞运行中的 agent**：读 OpenCode/Trae 的 SQLite 一律只读 + busy timeout；被写锁挡住时把 `db` + `-wal` 复制到临时目录再读。我们绝不写这些库。
- **可重建**：库损坏或 schema 变更时 `scan --full` 全量重放，`content_hash` 门控使其便宜。schema 变更用带版本号的迁移，失败即回退并要求重建，不做静默兼容。
- **Windows 监听不可靠**：递归 fsnotify + 30s 兜底轮询，避免静默漏更。
- **写入原子性**：简报临时文件 + rename；区块替换只重写标记区间，区间外字节原样保留。
- **并发**：全进程单一 store writer（串行事务），扫描与 MCP 写入共用同一队列。
- **守护进程未运行**：`mcp` 与 CLI 明确报「serve 未运行」并给出启动命令，不自动拉起，避免端口与库被多进程争抢。

## 10. 测试策略

- **单元测试**：文本归一化与哈希、去重命中、status 判定规则、裁剪优先级、`project_key` 归一化。表驱动。
- **适配器契约测试**：四个适配器共用一套测试骨架，fixtures 由本机真实文件脱敏拷贝而来，断言产出的 `MemoryItem` / `SessionEvent` 集合。
- **Composer golden test**：给定 store 快照断言 markdown 完全一致；额外断言不超预算、偏好与硬约束段永不被裁。
- **集成测试**：临时 HOME + 真实 SQLite，跑 import → search → add → handoff 全链路；连续两次 import 必须零 diff（幂等）。
- **并发测试**：扫描进行中写入 MCP，断言无死锁且结果确定。
- **MCP 测试**：tools 协议级测试 + stdio bridge 端到端。
- **人工验收**：真实 agent 内切换体验走查一次，不写自动化。

## 11. 验收标准

1. 在 Qoder 完成一个任务后，OpenCode 或 Trae 新会话无需追问即可回答：在做什么、做到哪一步、有哪些硬约束。
2. 每条共享记忆可追溯到来源 agent 与来源位置。
3. 重复扫描幂等，且 `doctor` 能证明所有 agent 原生记忆文件的哈希未被本程序改动。
4. 交接简报稳定落在 token 预算内。

## 12. 里程碑

- **M1 骨架**：Store + 配置 + CLI + Qoder 记忆适配器 → `memory_search` 可用。
- **M2 交接**：Qoder 会话导入 + Extractor + Composer → `HANDOFF.md`。
- **M3 接入**：MCP server + stdio bridge + `link`（Qoder、OpenCode）。
- **M4 扩展**：Trae/WorkBuddy 记忆、OpenCode 会话、`doctor --dump` 探测 Trae 会话存储。
- **M5 增强（可选）**：LLM 润色、向量检索、导出与备份完善。

M1–M3 即达成第 11 节全部四条验收标准（跨 Qoder 与 OpenCode 两个 agent）；M4/M5 各自独立成计划。

## 13. 已知不确定项及处理方式

| 不确定项 | 处理 |
|---|---|
| Trae 会话在 `state.vscdb` 中的键结构 | M4 前由 `doctor --dump` 探测；探不到则继续只支持记忆层，不影响主线 |
| WorkBuddy 对话内容存储位置 | 第一版不接入会话；仅用 `MEMORY.md`/`USER.md` 与 `sessions.json` 的项目路径映射 |
| OpenCode SQLite 表名与字段版本差异 | 实现前先 dump schema，适配器按检测到的版本分支 |
| Trae slug 反解真实路径的可靠性 | 反解失败时回退为独立 project_key，并由 `doctor` 列为未合并项 |
