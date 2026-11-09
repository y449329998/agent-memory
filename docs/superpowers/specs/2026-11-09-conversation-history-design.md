# 完整对话存储与检索设计

日期：2026-11-09
状态：与用户确认（粒度：消息全文+工具摘要；MCP：search+get；保留：永久；WorkBuddy 一起做；详情页回溯改读库）

## 1. 目标

把每场 agent 会话的**完整对话**存进库，随会话文件变化自动增量更新；保留现有 `task_snippet`（最近 20 条）不动；
通过 MCP 提供 `conversation_search` 与 `conversation_get`，让 agent 能检索/翻看更早的对话；
任务详情页的「加载更早」改读这张表（不再依赖源文件，源文件删了也能看）。

## 2. 表结构（schema v4）

```sql
CREATE TABLE IF NOT EXISTS session_message (
  id          INTEGER PRIMARY KEY,
  session_id  INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  seq         INTEGER NOT NULL,
  role        TEXT NOT NULL,             -- user / assistant
  kind        TEXT NOT NULL,             -- text / tool / tool_error
  text        TEXT NOT NULL DEFAULT '',
  tool        TEXT NOT NULL DEFAULT '',
  files_json  TEXT NOT NULL DEFAULT '[]',
  ts          INTEGER NOT NULL,
  truncated   INTEGER NOT NULL DEFAULT 0,
  UNIQUE(session_id, seq)
);
CREATE INDEX IF NOT EXISTS idx_session_message_session ON session_message(session_id, seq);
CREATE VIRTUAL TABLE IF NOT EXISTS session_message_fts USING fts5(text_seg, tokenize = 'unicode61');
```

`rowid` 与 `session_message.id` 对齐；写入/删除时同步维护 FTS。`schemaVersion` 3→4，迁移只建表。

## 3. 写入粒度

- 用户/助手文本：全文（单条上限 100k 字符，超了标 `truncated`）。
- 工具调用：`kind=tool`，`tool=名字`，`text` 为摘要（Bash 命令 / 文件路径；其余 input JSON 截 2000 字），`files` 记路径。
- 工具结果：只在报错时存，`kind=tool_error`，`text` 截 4000 字。
- 写入时机：沿用现有会话导入路径（文件变化 → 防抖扫描 → 重解析）→ `ReplaceMessages` 整场重写；
  `task_sync` 也会触发。会话被压缩/截断时按 seq 删除多余行。

## 4. 组件

| 单元 | 职责 |
| --- | --- |
| `internal/agent/adapter.go` | 新增 `TranscriptMessage` 与可选接口 `TranscriptParser{ ParseTranscript(path) ([]TranscriptMessage, error) }` |
| `internal/agent/qoder/transcript.go` | Qoder 会话 → 完整消息（文本全文 / 工具摘要 / 报错结果） |
| `internal/agent/workbuddy/transcript.go` | WorkBuddy 会话 → 同上（`function_call` / `function_call_result`） |
| `internal/store/message.go` | `ReplaceMessages` / `MessagesForTask` / `HasMessages` / `SearchMessages`（FTS5 + 中文二元组） |
| `internal/ingest` | 会话导入后写完整消息（含定向同步） |
| `internal/daemon/conversation.go` | 对话端点改读库（`before_seq` 分页）；库里没有时回退 tailreader |
| `internal/mcp/tools.go` + `daemon/mcp.go` | 第 13/14 个工具：`conversation_search` / `conversation_get` |
| `webui/app.js` | 「加载更早」改用 `before_seq` 游标 |

## 5. 检索与分页

- `conversation_search(query, project_path?, task_id?, limit?)`：`normalize.QueryTokens` 切词 → FTS5 MATCH →
  返回 `task_id / session_id / agent / title / role / ts / text 片段`；按 bm25 升序。
- `conversation_get(task_id, before_seq?, limit?)`：取任务主来源会话（`task.session_id`，formal 取最近关联），
  默认最近 50 条；`before_seq` 给定时取 `seq < before_seq` 的最后 limit 条；返回 `next`（继续往前翻的游标，0=到头）。

## 6. 边界

- 旧库升级后历史会话还没写入完整消息：读接口回退 tailreader 读源文件；`scan --full` 或 `task_sync` 可补齐。
- Trae 无本地会话 → 无完整对话。
- 会话删除/项目删除：`ON DELETE CASCADE` 带走消息；FTS 独立虚表，删除时显式清（事务内）。
- 单条超限截断并标记，不阻断整场解析。
- 只读边界不变：解析只读源文件。

## 7. 测试

- 解析：Qoder（文本/工具/报错/超长/坏行）与 WorkBuddy（message/function_call/非 completed 结果）fixture。
- store：整场替换、会话截断删除多余 seq、FTS 两字中文命中、项目/任务过滤、分页游标。
- MCP：两个工具的入参与返回；未知任务报错。
- 真实数据：44MB 会话写入后 `conversation_search` 命中已知关键字，`conversation_get` 翻页到会话开头；
  记录库增量大小与检索耗时（<200ms）。
- UI：任务详情「加载更早」改读库后仍能翻到更早消息；源文件临时改名不影响。
