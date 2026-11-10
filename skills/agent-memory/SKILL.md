---
name: agent-memory
description: 使用 agent-memory 的 MCP 工具（memory_search/memory_add/task_*/conversation_*/handoff_get/project_resolve/stats）。当用户提到跨 agent 共享记忆、接手项目、换 agent 继续任务、查某项目之前的对话或进度、记录长期约定/踩坑、生成交接 Prompt 时使用；也用于判断 14 个工具该调哪一个、按什么顺序调。
---

# agent-memory 工具选择指南

## Overview

agent-memory 把多个 agent（Qoder / Trae / WorkBuddy / OpenCode / Claude）的记忆、对话与任务汇到本地一个库，并通过 MCP 暴露 14 个工具。本技能只回答两件事：**什么时候该用哪个工具**、**按什么顺序用**。不要凭猜测填 task_id，也不要重复建任务。

前置：本机 `agent-memory serve` 在运行（默认端口 47123），MCP 已通过 `agent-memory link` 接到客户端。工具报连接/令牌错误时，先让用户启动桌面应用或 `agent-memory serve`。

## 工具速查

| 工具 | 什么时候用 | 不要用在 |
| --- | --- | --- |
| `handoff_get` | **接手项目第一步**：读本项目的交接简报（目标/偏好/硬约束/进行中任务/未决问题） | 想深入细节时——简报只是索引，接着用 `task_get` / `conversation_get` |
| `task_list` | 找“现在有哪些任务”：可按 `kind=session\|formal`、`status`、`project_path`、`limit` 过滤 | 想读某条任务的完整内容——用 `task_get` |
| `task_get` | 读一条任务的全文：目标、状态、步骤（含 source_key）、最近 20 条片段、来源会话 | 想翻更早的对话——用 `conversation_get` |
| `task_sync` | **把当前会话立刻入库**：`project_path` 必填，`session_id` 可填会话文件路径；不传则取该项目最近修改的会话 | 跨项目同步（一次只同步一个项目的会话） |
| `task_update` | 更新进度：`status` / `owner_agent` / `next_steps` / `step_status`（按 `source_key` 改单步） | 改步骤文本或目标前先想清楚——它们会冻结扫描同步 |
| `task_add` | **只用于跨对话的合并任务**（收尾/汇总），可带步骤 | 不要用它表示“当前这场对话”——对话任务由扫描/`task_sync` 自动生成 |
| `task_export` | 生成可直接粘给下一个 agent 的完整交接 Prompt（markdown） | 只是自己想看进度——用 `task_get` |
| `conversation_search` | 按关键词找**历史对话**（中文两字词可命中），可按 `project_path` / `task_id` 收窄 | 找任务列表/进度——用 `task_list` |
| `conversation_get` | 按顺序读某任务的完整对话；`before_seq` 往更早翻，`next=0` 到头 | 只想看最近几条——`task_get` 的片段就够 |
| `memory_search` | 找跨 agent 的长期记忆：偏好、约定、约束、教训、事实；中文检索 | 找对话原文——用 `conversation_search` |
| `memory_add` | 记下**值得长期复用**的约定/约束/教训/踩坑；`--scope project` 需 `project_path` | 不要记录临时进度、一次性命令输出 |
| `memory_list` | 浏览记忆条目（默认只列用户级；给 `project_path` 则含该项目级） | 有明确关键词时——`memory_search` 更准 |
| `project_resolve` | 确认路径对应哪个 `project_key`（同一仓库多 checkout、路径重命名时） | 已知 project_key 时不必调 |
| `stats` | 诊断：项目数、记忆数、来源与失败项 | 日常任务操作 |

## 典型工作流

### 1. 接手项目（用户说“继续 / 接着做 / 换了个 agent”）

1. `handoff_get {project_path}` —— 先读简报，别让用户重述背景。
2. `task_list {project_path, status:"doing"}` —— 找进行中的任务；对话任务看 `kind:"session"`，跨对话收尾看 `"formal"`。
3. `task_get {id}` —— 读目标、步骤、下一步、最近片段。
4. 需要更早上下文：`conversation_get {task_id, limit:50}`，必要时带 `before_seq` 往前翻；或 `conversation_search {query, project_path}` 直接找关键结论。
5. 接手同一个任务：`task_update {id, status:"doing", owner_agent:"<你的名字>"}`，**不要新建任务**。

### 2. 干活中记录进度

1. 先拿到 task_id：`task_sync {project_path}`（返回 `task_id`；`settled:false` 表示会话还在写）。
2. 勾进度：`task_update {id, step_status:{"qt:157":"done"}}` —— 只改状态，不冻结扫描。
3. 出现长期有效的规则/坑：`memory_add {text, type:"convention"|"constraint"|"lesson", project_path?}`。一次一条，写清“怎么做/为什么”，不要贴大段日志。
4. 阶段性结论写 `next_steps`：`task_update {id, next_steps:"..."}`。

### 3. 收尾 / 交接给下一个 agent

1. `task_update {id, status:"done"|"blocked", next_steps:"..."}`。
2. `task_export {id}` 拿完整交接 Prompt，交给用户或下一个 agent。
3. 需要把进展并进一件跨对话的事：`task_add` 建合并任务后用 `task_update` 补步骤，或让用户在桌面端「合并到…」。

### 4. 找“之前那次对话里说过什么”

1. `conversation_search {query:"关键词", project_path?}` —— 先搜。
2. 命中后 `conversation_get {task_id, before_seq:<命中 seq>}` 读上下文。
3. 搜不到且确认是新会话：先 `task_sync {project_path}`（或等扫描），再搜；旧库尚未补写完整对话时 `conversation_get` 可能为空，此时需 `agent-memory scan --full`。

### 5. 判断“是不是同一个项目”

`project_resolve {path}` → 比对 `handoff_get` 里的 `project_key`。不要按目录名猜。

## 关键规则

- **一个任务只更新一次 id**：对话任务由 `scan`/`task_sync` 生成，`task_add` 仅用于合并任务。
- **`task_update` 的冻结语义**：`status` / `owner_agent` / `next_steps` / `step_status` 不冻结扫描；只有 `goal` 或整组 `steps` 文本才会让这条任务脱离自动同步。改文本前先确认用户要“人工接管”。
- **`step_status` 的 key**：用 `task_get` 里的 `SourceKey`（Qoder 形如 `qt:157`，WorkBuddy 用清单文件名）；`cmd:`/`err:` 开头的反推步骤不接受覆盖。
- **对话分页**：`conversation_get` 返回时间正序；`next` 是继续往前翻的游标，`0` 表示到头。不要自己算 offset。
- **记忆类型**：`preference` / `constraint` / `convention` / `lesson` / `fact` / `goal`；`scope` 默认 `user`，项目级必须给 `project_path`。
- **省 token**：能 `memory_search` / `conversation_search` 定位就不要整份读简报或整场读对话；搜索结果足够回答就停。
- **失败处理**：`runner not configured` 表示 serve 未通过 `link` 接好或版本旧；`task not found` 先 `task_list` 确认真实 id，不要编造。

## Resources

- 本技能的判断依据来自项目内 `README.md` 与 `docs/usage.md`（MCP 章节）。工具入参的权威定义以服务端 `tools/list` 为准；两者冲突时信 `tools/list`。
