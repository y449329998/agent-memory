# 任务详情工作台设计（对话优先 + 真 Todo + 状态覆盖层）

日期：2026-11-09
状态：已与用户逐节确认（架构 / 表结构 / 四区呈现 / 错误边界 / 测试验收）

## 1. 问题

任务详情页现在是「一长条编辑表单 + 最近对话（仅最后 20 条片段）+ 来源会话」，进来先看到表单而不是对话。
用户要的是：进来先看这场对话的最新内容与来源，再按需切到进度、文件、交接四个区。

同时暴露三个数据层缺口：

1. **Qoder 的步骤是反推的，不是它自己的 Todo。** Qoder 把进度写在会话 jsonl 的 `TaskCreate` / `TaskUpdate`
   调用里（实测一场对话 138 次建任务、231 次改状态），但解析器只认 `Edit/Write/Bash/Read`，
   步骤靠摘要卡反推（命令/改动→已完成、报错→受阻、下一步→待办，上限 12 条）。WorkBuddy 已有真清单通道。
2. **MCP 无法完整更新对话任务。** `UpdateTask` 末尾无条件写 `manual_edited_at = now`
   （`internal/store/task.go`），而扫描回写带 `WHERE manual_edited_at = 0`，所以 agent 哪怕只改一个
   `status` 也会把这条对话任务永久冻出自动同步；且 `TaskStepInput` 没有 `source_key`，
   改一次步骤就把扫描用来匹配 Todo 的键洗成空。
3. **对话滞后。** 片段只由扫描生成，要等 jsonl 落盘 + 静默 5 分钟 + 下一次扫描；agent 无法主动触发同步。

## 2. 已确认的决策

| 决策点 | 结论 |
| --- | --- |
| 最近对话数据源 | 库优先：默认渲染库里最近 20 条；往回翻时按 `task.SourcePath` 从源 jsonl 读更早；源文件不可读则回退只用库并提示 |
| 进行中的 task 来源 | 读 agent 真 Todo：Qoder 解析 `TaskCreate`/`TaskUpdate` 归并；WorkBuddy 继续读 `tasks/*.json`；无 Todo 时退回反推 |
| 人工改状态 | 状态覆盖层（方案 A）：独立表按 `(task_id, source_key)` 存人工状态，扫描照常重写步骤文本，读取时叠加 |
| 可编辑字段位置 | 顶部常驻任务头（目标/状态/owner/下一步/来源），下面四个 tab |
| 交接 Prompt | 极简接手版（五块 ≤30 行）+ 折叠完整版（现有七节） |
| 任务文件口径 | 按文件聚合：最近改动时间 + 改动次数 + 来源 agent |
| MCP 完整更新 | 写入语义分层 + `TaskStepInput.source_key` + 新增 `task_sync` 工具（三者都做） |

## 3. 架构与组件边界

沿现有分层 adapter → ingest → store → daemon → webui/compose，六个可独立测试的单元：

| 单元 | 职责 | 接口 | 新增/改动 |
| --- | --- | --- | --- |
| `internal/agent/qoder/todo.go` | 归并 `TaskCreate`/`TaskUpdate` 为这场对话的 Todo（按 Todo id 取最后状态） | 填 `agent.SessionDraft.Steps`，与 WorkBuddy 同通道 | 新增 |
| `internal/tailreader`（新包） | 按字节窗口从文件尾往前读，产出一页消息 + 下一偏移；只读不整份解析 | `Page(path string, before int64, limit int) ([]Message, next int64, err error)` | 新增 |
| `internal/store/task_override.go` | `task_step_override` 读写；`TaskDetail`/`ListTasks` 返回前叠加 | `SetStepOverride` / `PruneStaleOverrides` / 合并进 `task.Steps` | 新增 |
| `internal/daemon/conversation.go` | `GET /v1/task/conversation?task_id=&before=&limit=`：库优先，回溯调 tailreader，源不可读回退 | JSON 消息页 + `source` + `warning` | 新增 |
| `internal/compose/task_export.go` | 极简接手版与完整版两个渲染函数 | `RenderTaskHandoffBrief` / `RenderTaskExport` | 改动 |
| `webui/task.html` + `app.js` | 任务头 + 四 tab；复用 `enhanceSelect`、`renderMd`、即时生效模式 | — | 改动 |

MCP 侧改动：`internal/mcp/tools.go`（`TaskStepInput.SourceKey`、`task_sync`、工具描述）、
`internal/daemon/mcp.go`（分层 freeze、`SyncTask`）、`internal/store/task.go`（`UpdateTask` 按字段决定 freeze）。

**核心不变量**：扫描只负责「源 → 步骤文本与源状态」，人工只负责「状态覆盖」，两者在 store 的读取视图汇合。
UI、MCP `task_get`、交接简报、Prompt 全走同一份已叠加视图，不出现页面与 Prompt 状态不一致。

## 4. 表结构与数据流

`schema_version` 2 → 3；迁移只建表、不动数据、可重复执行：

```sql
CREATE TABLE IF NOT EXISTS task_step_override (
  task_id     INTEGER NOT NULL,
  source_key  TEXT    NOT NULL,
  status      TEXT    NOT NULL,   -- pending / doing / done / blocked / skipped
  updated_at  INTEGER NOT NULL,
  PRIMARY KEY (task_id, source_key)
);
```

**稳定 key 规则**：覆盖层只对稳定 `source_key` 生效。Qoder 用 `qt:<Todo id>`，WorkBuddy 用清单文件名。
从摘要卡反推的步骤 key 是 `cmd:0` 这类序号，内容一变就错位，因此**反推步骤不接受单独改状态**
（UI 禁用其状态控件；只能改整条任务状态，或编辑步骤文本走 freeze）。

数据流：

1. **扫描写入**：`scan` → Qoder 适配器解析 Todo → `SessionDraft.Steps` → 现有 `pickSteps` 优先适配器 →
   重写 `task_step`（文本与源状态以源为准，不碰 `manual_edited_at`）。
2. **读取汇合**：`TaskDetail` / `ListTasks` 按 `(task_id, source_key)` 叠加覆盖后返回。
3. **人工改状态**：`POST /v1/tasks/step/status` 与 MCP `task_update` 的步骤状态 → 只写覆盖表。
   `status/owner/next_steps` → 现有 `UpdateTask` 但不置 `manual_edited_at`；
   改目标/步骤文本 → 置 `manual_edited_at`（语义=人工接管）。
4. **覆盖清理**：一条任务同步完后删除该 task 下已不存在于 `task_step` 的覆盖行。
5. **对话区**：只针对**主来源会话**——对话任务取 `task.session_id` 那条，合并任务取最近关联的那条；
   其余关联会话在来源列表里可点击切换主会话。默认展示主会话的 `task_snippet` 最近 20 条，
   响应附带该会话文件当前大小 `file_size`；第一次回溯 `before=file_size`（从尾往前），
   之后用返回的 `next`。tailreader 开 2 MB 窗口（44 MB 文件也只读几 MB），`next=0` 表示到头；
   源不可读 → `{source:"store_only", warning:"源文件不可读，只显示库里最近 20 条"}`。
6. **任务文件区**：按 `session_event` 的 `tool_edit` 文件聚合（最近时间 + 次数，跨该任务关联的多场会话合并）；
   无关联会话的手工任务退回 `task.Files`。

## 5. 详情页呈现

**任务头（常驻）**：目标（点击即编辑）· 状态徽章（下拉改，走覆盖层）· owner · 类型徽章（对话/合并）·
来源会话（agent · 标题 · 最近对话时间 · 路径可复制）· 下一步一行摘要 + 展开编辑 · 归档/合并/返回。
保存按钮仅在有改动时出现。默认 tab = 最近对话。

**① 最近对话**：只展示主来源会话（对话任务=它自己那场；合并任务=最近关联那场，可在来源列表切换）。
role 徽章 + 时间 + md 正文；工具调用折叠成一行（`Bash · 命令前 80 字`）点开看全文；
截断条目尾部标「…源文件里更长」。底部「加载更早」回溯；到头显示「已到会话开头」；源不可读顶部黄条。
空态（手工合并任务）：「没有对话来源，可用『合并到…』把相关对话并进来」。

**② 进行中的 task**：顶部进度条 `done/total`；每行状态控件 + 名称 + 进行中时的 activeForm +
来源徽章（`agent Todo` / `对话整理`）。稳定 key 可改状态（即时写覆盖层）；反推步骤状态控件禁用并 tooltip 说明。
「添加步骤」与改步骤文本弹确认「之后这条任务不再由扫描自动同步」→ 走 `UpdateTask`。

**③ 任务文件**：表格 路径（可复制）· 最近改动时间 · 改动次数 · 来源 agent，按最近时间倒序；
顶部「复制全部路径」。手工任务退回 `task.Files` 并标注来源；空态「这场对话没有文件改动」。

**④ 任务交接**：极简接手版预览（md ≤30 行）+ 复制；折叠「完整版（命令/报错/片段）」+ 复制；
一行说明「粘给下一个 agent，它会先 task_get 再用同一个 task_id 更新」；已归档提示先恢复。

tab 切换写进 URL（`?tab=conversation|steps|files|handoff`），刷新与前进后退保持。

**极简接手版五块**：一句话目标 / 现在到哪与卡在哪 / 未完成步骤清单 / 关键文件（≤8 个）/
给下一个 agent 的三行指令（先 `task_get`、同一 `task_id` 更新、done 后写验证结果）。

## 6. MCP 写入语义与 task_sync

- **分层 freeze**：`UpdateTask` 按本次实际改动的字段决定——`status/owner/next_steps/步骤状态` 不置
  `manual_edited_at`；`goal/步骤文本` 才置。这是行为变更：老调用方只传 status 从「冻结」变「不冻结」，
  usage.md 与工具描述同步说明。
- **`TaskStepInput.SourceKey`（可选）**：给了按它匹配/新增；没给按 subject 匹配现有步骤；匹配不上才追加。
  保证 agent 改步骤不洗掉键。
- **`task_sync(project_path, session_id?)`**（第 12 个工具）：给了 `session_id` 定向同步那条；
  没给取该项目下最近修改的会话文件，并在返回里带所选会话标题与路径供核对。
  定向同步绕过静默窗口与哈希门控，强制解析一次，走同一条 `syncTaskFromSession`（幂等），
  返回 `task_id` + 快照；正在写的会话快照标 `settled:false`。项目下无会话 → 明确报错。

## 7. 错误处理与边界

- **tailreader**：文件不存在/不可读 → 回退 `store_only` + 提示；偏移失效（会话被压缩/重写）→ 丢弃窗口内
  不完整首行，整窗无消息则空页 + `next=0` + 「源文件已变化，请刷新」；单行超 1 MB 截断正文并标「过长已截断」。
  只 `Open/Read/Stat`，绝不写 agent 目录。
- **Todo 解析**：缺字段/结构变化跳过该条不中断整场，计入现有 source error；同 id 多次 update 取最后；
  只改 status 的 update 保留 create 的 subject；整场无 Todo → Steps 空 → 退回反推。
- **覆盖层**：孤儿不叠加、同步后 prune；非法状态拒绝（复用 `validStepStatus`）；
  合并任务步骤无稳定 key → 覆盖层不适用，改状态走 `UpdateTask`（formal 不被扫描写，freeze 无副作用）。
- **前端**：四 tab 独立加载，一个失败不影响其他；回溯偏移只存内存/URL，不写库。

## 8. 测试与验收

单元：
- tailreader：多字节 UTF-8 行、几 MB 超长行、截断尾行；断言不丢行、读取字节数有上限、偏移失效返回空页。
- Todo 解析：create / 多次 update / 只改 status / 缺字段 / 无 Todo 五类 fixture。
- 覆盖层：叠加命中、孤儿不叠加、prune、非法值拒绝。
- `UpdateTask` 分层：只改 status/owner/next_steps/步骤状态 → `manual_edited_at` 不变；改 goal/步骤文本 → 置位。
- MCP：`source_key` 匹配/追加；`task_sync` 绕过静默、`settled:false`、无会话报错。
- 交接渲染：极简版 ≤30 行五块齐全；完整版保留七节。

集成 / 真实数据：
- 用 44.4 MB、138 次 TaskCreate 的真实会话跑 scan：步骤数与 Todo 数一致、`source_key` 形如 `qt:<id>`；
  手改一个步骤状态后重扫 → 状态保留、文本仍被源刷新。
- 真实 44 MB 会话上「加载更早」三次，单次 < 1s。
- 只读边界：scan / task_sync / 回溯读前后对 agent 原生 `*.jsonl` / `*.md` 做 md5 对比无差异。
- 浏览器实测四 tab：默认最近对话、tab 写 URL、源文件改名后黄条回退、反推步骤控件禁用、两版复制内容正确。

文档同步：usage.md（12 工具、分层语义、task_sync）、client-guide §11/§12 四区图解与重拍截图、
README 工具数 11 → 12。

## 9. 非目标

- 不保存完整对话原文进库（回溯读是按需只读源文件，不入库）。
- 不给 Trae 造对话任务（它的会话在云端）。
- 不做多机同步、不做 agent 原生文件写入。
- 不引入前端框架；tab 与下拉沿用现有原生组件模式。
