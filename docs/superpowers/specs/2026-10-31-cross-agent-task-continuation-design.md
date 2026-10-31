# 跨 Agent 任务接力设计

- 日期：2026-10-31
- 状态：待评审
- 范围：正式任务模型、WorkBuddy Todo 导入、任务更新接口、对话片段、交接导出、归档与合并

## 1. 背景

当前 `task_card` 是一场会话的规则摘要，存在三个限制：

1. 任务卡与会话一一绑定，无法跨会话持续更新。
2. WorkBuddy 的 TodoWrite 清单只用于判断是否扫描，没有导入具体步骤。
3. 没有任务写入接口，换 agent 后只能读取摘要，不能更新同一任务。

结果是：换 agent 时能知道“大概做到哪”，但无法可靠接着同一组步骤继续，也无法把新进展写回。

## 2. 目标

- A：新 agent 能看到下一步做什么。
- B：新 agent 能看到完整步骤、每项状态和当前进行到第几项。
- C：新 agent 能继续更新同一个任务、步骤和状态。
- D：新 agent 能查看最近关键对话片段；片段不足时能回到原会话文件。
- 所有来源自动建立正式任务；完成满 30 天自动归档。
- 桌面端拥有与 MCP 对等的编辑能力，并提供交接 Prompt 复制与 `HANDOFF.md` 更新。

## 3. 非目标

- 不做多 agent 并发抢占锁；用户不会同时用多个 agent 跑同一任务。
- 不保存完整 transcript，不把原始会话全文复制进共享库。
- 不接入 Trae 云端会话；不新增 Cursor、Codex、DeepSeek 等本地扫描适配器。
- 不引入 LLM 生成或改写任务；字段仍来自规则、用户输入或 agent 主动写入。
- 不做跨机器/多人同步。

## 4. 术语

- **会话摘要卡**：现有 `task_card`，一场会话一张，扫描生成，作为正式任务的同步来源。
- **正式任务**：新的 `task`，可跨会话持续存在，可被桌面端和 MCP 编辑。
- **步骤**：正式任务下的 `task_step`，来自 WorkBuddy Todo 或人工添加。
- **片段**：`task_snippet`，从会话事件中挑选的最近关键消息，上限 20 条。

## 5. 总体结构

```
会话文件
  -> 适配器解析（Qoder / WorkBuddy）
  -> session / session_event
  -> task_card（会话摘要，保持现状）
  -> task（正式任务）+ task_step + task_snippet + task_session
  -> Handoff / MCP / Web UI / 桌面端
```

`task_card` 继续承担“这场会话发生了什么”，`task` 承担“这项工作现在是什么状态、下一步由谁继续”。

## 6. 数据模型

新增表通过 `CREATE TABLE IF NOT EXISTS` 建在现有 schema 中，不修改 `task_card`，不提升 `schemaVersion`。

### 6.1 task

```sql
CREATE TABLE IF NOT EXISTS task (
  id                  INTEGER PRIMARY KEY,
  project_id          INTEGER NOT NULL DEFAULT 0,
  goal                TEXT NOT NULL DEFAULT '',
  status              TEXT NOT NULL DEFAULT 'doing',
  owner_agent         TEXT NOT NULL DEFAULT '',
  claimed_at          INTEGER NOT NULL DEFAULT 0,
  next_steps          TEXT NOT NULL DEFAULT '',
  decisions_json      TEXT NOT NULL DEFAULT '[]',
  files_json          TEXT NOT NULL DEFAULT '[]',
  commands_json       TEXT NOT NULL DEFAULT '[]',
  errors_json         TEXT NOT NULL DEFAULT '[]',
  open_questions      TEXT NOT NULL DEFAULT '',
  source_agent        TEXT NOT NULL DEFAULT '',
  source_path         TEXT NOT NULL DEFAULT '',
  manual_edited_at    INTEGER NOT NULL DEFAULT 0,
  completed_at        INTEGER NOT NULL DEFAULT 0,
  archived_at         INTEGER NOT NULL DEFAULT 0,
  merged_into_task_id INTEGER NOT NULL DEFAULT 0,
  created_at          INTEGER NOT NULL,
  updated_at          INTEGER NOT NULL
);
```

- `status`：`doing / done / blocked / unknown`，沿用现有任务卡状态。
- `owner_agent`：认领者，仅展示与交接提示，不做并发锁。
- `manual_edited_at`：最近一次通过 MCP/桌面端修改的时间；大于 0 时扫描不再覆盖任务字段、步骤和片段。
- `completed_at`：状态进入 `done` 的时间，用于 30 天归档。
- `archived_at`：归档时间；0 表示未归档。
- `merged_into_task_id`：被合并到哪条任务；0 表示未合并。
- `source_agent` / `source_path`：第一场来源会话的 agent 与文件路径，便于界面展示与回源。

索引：

```sql
CREATE INDEX IF NOT EXISTS idx_task_project ON task(project_id, archived_at, status);
CREATE INDEX IF NOT EXISTS idx_task_owner ON task(owner_agent, status);
```

### 6.2 task_step

```sql
CREATE TABLE IF NOT EXISTS task_step (
  id          INTEGER PRIMARY KEY,
  task_id     INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  subject     TEXT NOT NULL DEFAULT '',
  description TEXT NOT NULL DEFAULT '',
  active_form TEXT NOT NULL DEFAULT '',
  status      TEXT NOT NULL DEFAULT 'pending',
  source_key  TEXT NOT NULL DEFAULT '',
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE(task_id, seq)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_task_step_source
  ON task_step(task_id, source_key) WHERE source_key <> '';
```

- `status`：`pending / doing / done / blocked / skipped`。
- `source_key`：WorkBuddy 任务文件名（如 `1`）；人工步骤为空。
- WorkBuddy 状态映射：`pending -> pending`，`in_progress -> doing`，`completed -> done`。
- 每个任务最多 100 个步骤，超出部分不导入。

### 6.3 task_snippet

```sql
CREATE TABLE IF NOT EXISTS task_snippet (
  id      INTEGER PRIMARY KEY,
  task_id INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  seq     INTEGER NOT NULL,
  role    TEXT NOT NULL DEFAULT '',
  kind    TEXT NOT NULL DEFAULT '',
  text    TEXT NOT NULL DEFAULT '',
  ts      INTEGER NOT NULL DEFAULT 0,
  UNIQUE(task_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_task_snippet_task ON task_snippet(task_id, seq);
```

- 每条任务最多 20 条，保留最近的用户目标、assistant 结论、命令、报错和文件改动。
- 单条文本沿用会话事件的 500 字截断。
- 片段只读展示，不提供单独编辑工具；人工任务默认没有片段。

### 6.4 task_session

```sql
CREATE TABLE IF NOT EXISTS task_session (
  task_id    INTEGER NOT NULL REFERENCES task(id) ON DELETE CASCADE,
  session_id INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  role       TEXT NOT NULL DEFAULT 'source',
  created_at INTEGER NOT NULL,
  PRIMARY KEY(task_id, session_id)
);
```

- 一条任务可以关联多场会话；`role` 为 `source` 或 `continuation`。
- 会话删除时只删关联，不删正式任务。

## 7. 扫描与同步

### 7.1 WorkBuddy Todo 导入

- WorkBuddy 适配器在扫描会话时读取 `~/.workbuddy/tasks/<session-id>/*.json`。
- 按文件名数字升序导入，`source_key` 为去掉 `.json` 的文件名。
- 解析 `subject`、`description`、`activeForm`、`status`；坏 JSON 跳过，不影响会话导入。
- `agent.SessionUnit` 增加步骤字段，由 ingest 写入正式任务。

### 7.2 会话关联正式任务

 ingest 完成会话与事件写入后，按以下顺序处理：

1. 通过 `task_session` 查找该会话是否已关联任务；有则同步该任务。
2. 没有关联时，检查同项目、未归档、状态为 `doing/blocked`、`owner_agent` 等于本会话 agent 的任务：
   - 恰好一条：把会话挂到该任务，作为 `continuation`。
   - 零条或多条：不自动挂接。
3. 仍无任务时：
   - 会话已有摘要卡，或已有 WorkBuddy 步骤：创建正式任务并关联本会话。
   - Qoder 会话没有摘要卡：等下一次扫描生成摘要卡后再创建。

创建任务时：

- `goal` 优先取摘要卡目标；没有卡时取会话标题。
- `source_agent`、`source_path` 取会话字段。
- 状态取摘要卡状态；没有卡时按步骤推导。
- 片段从已导入的 `session_event` 取最近 20 条关键事件。

### 7.3 自动同步规则

- `manual_edited_at = 0` 时，扫描可更新任务目标、状态、下一步、决策、文件、命令、报错、未决问题与步骤。
- `manual_edited_at > 0` 时，只新增 `task_session` 关联，不覆盖任务字段与步骤；片段属于只读派生数据，仍按所有关联会话刷新最近 20 条。
- 状态推导：
  - 有摘要卡：使用摘要卡状态。
  - 无摘要卡但有步骤：任一 `blocked` -> `blocked`；全部为 `done` 或 `skipped` 且至少一步 -> `done`；否则 `doing`。
  - 既无卡也无步骤：`doing`。
- 状态从非 `done` 进入 `done` 时写 `completed_at`；已经是 `done` 的重复同步不重置完成时间。离开 `done` 时清空 `completed_at` 与 `archived_at`。

### 7.4 片段选择

从任务关联的所有会话事件中按时间取最近 20 条，保留类型：

- `user_text`：用户目标与补充要求。
- `assistant_text`：结论与下一步。
- `tool_command`：执行的命令。
- `tool_error`：报错。
- `tool_edit`：合并为“改动: <文件列表>”。

忽略读取类事件与普通 other 事件。

## 8. 任务生命周期

- 新建自动任务：`status=doing`，`owner_agent` 为空。
- 认领：通过 `task_update` 设置 `owner_agent`；非空且 `claimed_at=0` 时写入当前时间。
- 完成：状态改为 `done`，写 `completed_at`。
- 归档：`status=done` 且 `completed_at` 距当前超过 30 天，扫描时写 `archived_at`。
- 恢复：`archived_at=0`；如果状态仍是 `done`，把 `completed_at` 重置为当前时间，重新获得 30 天窗口。
- 归档任务不出现在默认任务列表、项目活动任务区和 Handoff 中，只在归档视图显示。
- 合并：来源任务的步骤、片段、会话关联并入目标任务；步骤按 `source_key`（人工步骤按标题）去重，最多保留 100 条；片段按时间合并后保留最近 20 条；目标任务写 `manual_edited_at`。来源任务写 `merged_into_task_id` 并归档。

## 9. 迁移与回填

- 新表随现有 schema 一起 `CREATE TABLE IF NOT EXISTS`，不提升 `schemaVersion`，旧库直接启动。
- `meta` 增加一次性标记 `task_backfill_v1`。
- 首次打开新版本时，在事务中把现有 `task_card` 回填为正式任务：
  - 任务 `id` 直接使用对应 `task_card.id`，保证旧 `task_get` id 继续可用；后续新任务使用自增 id。
  - 复制目标、状态、决策、文件、命令、报错、未决问题、下一步。
  - 通过 `task_card.session_id` 创建 `task_session`。
  - 从 `session_event` 回填最近 20 条片段。
  - 步骤暂为空，下一次扫描时由 WorkBuddy 数据补齐。
- 回填失败则 `Open` 返回错误，不带着半套任务运行。

## 10. MCP

保留现有 8 个工具；`task_list`/`task_get` 改为正式任务语义，新增 3 个工具，共 11 个：

| Tool | 变更 |
|---|---|
| `task_list` | 列正式任务；参数增加 `include_archived`，默认排除归档 |
| `task_get` | 返回任务、步骤、片段、来源会话 |
| `task_add` | 新建正式任务，可带步骤 |
| `task_update` | 更新目标/状态/下一步/owner/归档，步骤整组替换 |
| `task_export` | 返回交接 Markdown |
| `memory_*` / `handoff_get` / `project_resolve` / `stats` | 不变 |

`task_list` / `task_get` 兼容旧字段：`goal`、`status`、`next_steps`、`files`、`commands`、`errors` 保持原名；列表结果同时保留 `cards` 字段（内容为同一批正式任务）并新增 `tasks` 字段，避免旧客户端立刻失效。

`task_update` 参数：

- `id` 必填。
- `goal`、`status`、`next_steps`、`owner_agent`、`archived` 可选。
- `steps` 可选；提供时整组替换现有步骤，每项为 `{subject, description, active_form, status}`，不要求 id。
- 任何成功的 `task_update` 都写 `manual_edited_at`；清空 `owner_agent` 时同时清空 `claimed_at`。

`task_add` 参数：

- `project_path`、`goal` 必填；`project_path` 走与记忆写入相同的 `ResolveOrCreateProject`。
- `status`、`next_steps`、`owner_agent`、`steps` 可选。
- 新建即写 `manual_edited_at`，避免后续扫描覆盖人工任务。

`task_export` 输出：

- 任务 ID、项目、目标、状态、owner。
- 步骤清单（`[x] / [~] / [ ]`）与当前步骤。
- 下一步、改动文件、命令、报错。
- 最近最多 10 条对话片段。
- 明确要求：先用 `task_get` 读取，再用 `task_update` 更新同一个 `task_id`，不要新建任务。

## 11. HTTP

新增：

```text
GET  /v1/tasks
GET  /v1/tasks/item?id=
POST /v1/tasks
POST /v1/tasks/update
POST /v1/tasks/merge
GET  /v1/tasks/export?id=
```

- 列表参数：`project_key`、`status`、`include_archived`、`limit`。
- 写接口沿用现有 Bearer 与 Origin 校验。
- 新建/更新请求中的 `steps` 都是整组替换，每项为 `{subject, description, active_form, status}`。
- `POST /v1/tasks/merge`：`source_id`、`target_id`；禁止相同或目标任务已归档。
- 现有 `GET /v1/task` 保留给会话摘要卡，桌面端任务区改用 `/v1/tasks`。

## 12. Handoff 与导出

### 12.1 HANDOFF.md

- 新增“进行中的任务”区，列 `doing/blocked` 且未归档任务。
- 每条显示目标、状态、owner、步骤进度、当前步骤、下一步、改动文件、命令、报错、来源会话。
- 新增“最近完成”区，最多 3 条未归档 `done` 任务。
- 归档任务不进入简报。
- 预算紧张时优先压缩任务详情，用户偏好与硬约束不裁剪。
- 没有正式任务的项目回退到现有任务卡渲染，保证兼容。

### 12.2 交接 Prompt

- 由 `task_export` 生成，包含任务 ID 和更新指令。
- 桌面端“复制交接 Prompt”优先写剪贴板；失败时展示可选中的文本框。

### 12.3 自动刷新

- MCP/HTTP 任务写入成功后，重新生成该项目 `HANDOFF.md`。
- 写盘失败不回滚任务，只在响应中返回 warning 并在界面提示。
- 扫描结束后仍按现有逻辑刷新全部简报。

## 13. 桌面端

### 13.1 项目页

- 04 区块改为“任务”：显示正式任务、状态、owner、步骤进度、下一步、来源 agent。
- 增加“新建任务”“查看全部”“已归档”入口。
- 导航统计中的任务数量改为正式任务数量。

### 13.2 任务列表页 `/tasks`

- 按项目、状态筛选；默认只显示未归档。
- 支持切换到归档视图。
- 每条任务显示目标、状态、owner、步骤进度、最近活动。

### 13.3 任务详情页 `/t/<id>`

- 编辑目标、状态、owner、下一步。
- 步骤编辑：勾选状态、修改标题、增删步骤、调整顺序。
- 展示最近对话片段与来源会话；片段按 Markdown 渲染。
- 操作：保存、认领、归档/恢复、合并到已有任务、复制交接 Prompt。
- 合并入口选择目标任务后确认，成功后跳到目标任务。

### 13.4 新建任务页 `/t/new`

- 选择项目，填写目标、状态、owner、下一步与初始步骤。
- 创建成功后跳转任务详情。

## 14. 校验与错误处理

- 任务目标必填，去空白后不能为空。
- 状态必须在枚举内；步骤状态必须在枚举内。
- 步骤最多 100 条，每条标题必填。
- 合并禁止 source 与 target 相同；目标任务不能已归档。
- 单个任务同步失败写入扫描错误，不阻断会话与记忆导入。
- 任务写接口失败返回明确错误；Handoff 写盘失败只返回 warning。
- 任务更新与合并复用 Store 单连接顺序执行，不使用并发事务。

## 15. 测试

- Store：
  - task/task_step/task_snippet/task_session CRUD。
  - 人工编辑保护、完成/恢复、30 天归档、合并、回填。
- Ingest：
  - WorkBuddy Todo 导入为步骤。
  - 同项目同 owner 唯一活动任务自动挂接。
  - 已人工编辑的任务不被扫描覆盖。
- Compose：
  - Handoff 含进行中任务、步骤进度、当前步骤、下一步。
  - 预算压缩不回退硬约束。
- Daemon/MCP：
  - `/v1/tasks` 与 MCP 工具鉴权、校验、导出内容。
  - `task_list`/`task_get` 兼容既有字段。
- 桌面端：
  - 实际走一遍新建、编辑步骤、认领、归档/恢复、合并、复制 Prompt。
  - 检查项目 `HANDOFF.md` 已更新。
- 迁移：
  - 构造旧库（只有 task_card），打开后任务回填成功且数据不丢。

## 16. 成功标准

- 旧数据库无需重建即可启动，现有任务卡自动回填为正式任务。
- WorkBuddy 带 Todo 的会话在项目页显示完整步骤和当前进度。
- 换 agent 后可通过 `task_export` 拿到的 Prompt 继续同一 `task_id`，新会话自动挂到该任务。
- MCP 与桌面端都能新建、认领、更新步骤/状态、归档/恢复、合并。
- Handoff 包含进行中任务与步骤；归档任务自动隐藏。
- 全部现有测试通过，并新增上述测试；桌面端实际验收截图。
