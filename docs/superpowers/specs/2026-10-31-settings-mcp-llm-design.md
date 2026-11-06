# 任务筛选、MCP 教程与设置中心设计

- 日期：2026-10-31
- 状态：待评审
- 范围：任务搜索过滤、MCP 配置教程、统一设置页、端口/托盘、LLM 异步摘要、数据备份与 JSON 导入导出

## 1. 背景

当前缺口：

1. 任务页只有归档开关，项目、状态、owner、来源和时间都无法筛选。
2. MCP 配置只能通过 CLI `link` 写入，桌面用户没有可视化教程，也无法一键复制。
3. 端口固定 `47123`，设置只有托盘里的开机自启；端口、扫描参数、托盘行为没有统一入口。
4. LLM 只在早期设计中保留为 M5 草案，没有配置界面、密钥存储和摘要队列。

## 2. 目标

- 任务页支持关键词、项目、状态、owner、来源 agent、更新时间范围和归档筛选，筛选条件写入 URL。
- 新增 `/mcp` 教程页，按 Qoder/OpenCode/Claude/通用 stdio 生成当前配置并一键复制。
- 新增 `/settings` 单页 + 左侧功能分区：
  - 服务与托盘
  - LLM 摘要
  - 扫描与简报
  - 数据与备份
- 端口、扫描参数修改后自动重启内置服务；端口变化自动更新已登记 agent 的 MCP 配置，失败回滚。
- LLM 可选，OpenAI 兼容接口；未配置时现有规则功能完全可用。
- 摘要异步生成，原文保留；失败不影响写入、扫描和页面打开。
- 支持整库备份、JSON 导出与合并式导入。

## 3. 非目标

- 不把设置改造成账号体系或多用户中心。
- 不在设置页迁移数据目录；数据目录只读展示。
- 不自动改写 Trae/WorkBuddy 的原生配置。
- 不实现多机同步、云端备份。
- 不为 Cursor/Codex/DeepSeek 承诺已验证配置；只提供通用 MCP 示例。
- 不做流式 LLM 输出，不做对话式摘要。

## 4. 任务搜索过滤

### 4.1 后端筛选

`store.TaskFilter` 增加：

- `Query string`：匹配目标、下一步、步骤标题/描述、改动文件 JSON。
- `OwnerAgent string`
- `SourceAgent string`
- `UpdatedAfter time.Time`
- `UpdatedBefore time.Time`
- `Offset int`

`owner_agent` 与 `source_agent` 使用精确匹配；关键词才做模糊匹配。

查询使用 SQLite `LIKE` + `EXISTS` 子查询匹配步骤；任务量级在千条以内，不新增 FTS 索引。参数全部走绑定变量，不拼接用户输入。

`GET /v1/tasks` 增加：

```text
q, project_key, status, owner_agent, source_agent,
updated_after, updated_before, include_archived, limit, offset
```

时间参数接受 RFC3339 或 `YYYY-MM-DD`；结束日期按当天 23:59:59 处理。

### 4.2 任务页 UI

`/tasks` 顶部增加筛选条：

- 关键词输入
- 项目下拉
- 状态下拉
- owner 输入
- 来源 agent 下拉
- 时间范围：7 天 / 30 天 / 90 天 / 全部 / 自定义起止日期
- 显示归档复选框
- 筛选 / 重置按钮

筛选条件同步到 URL 查询参数；刷新、前进/后退保持。任务列表显示“共 N 条”，无结果给出明确空状态。

## 5. MCP 教程页

### 5.1 页面

新增 `/mcp`，主导航增加“MCP”。

顶部显示：

- 当前端口、数据目录、服务状态。
- MCP 启动程序路径；自动取 `agent-memory.exe`（与桌面程序同目录），找不到时提示配置 `mcp.binary_path`。
- token 默认掩码；复制内容使用真实 token。

四个标签页：

1. Qoder：`mcpServers` JSON，标注“本机已验证”。
2. OpenCode：`mcp` + `type: "local"`，标注“本机已验证”。
3. Claude Code：`~/.claude.json` 的 `mcpServers`，标注“本机已验证”。
4. 通用 stdio：命令、args、环境变量示例，标注“格式未在本机逐一验证”。

每个标签页有“一键复制”；失败时展示可手动复制的文本框和明确原因。

### 5.2 配置生成

新增鉴权接口：

```text
GET /v1/mcp/snippets
```

返回：

```json
{
  "port": 47123,
  "binary": "F:\\go\\agent-memory\\agent-memory.exe",
  "token_masked": "abcd…wxyz",
  "verified": ["qoder", "opencode", "claude"],
  "snippets": {
    "qoder": "…",
    "opencode": "…",
    "claude": "…",
    "generic": "…"
  }
}
```

配置内容由后端生成，端口/token/程序路径只有一个来源；页面不自行拼接协议细节。

## 6. 设置中心

### 6.1 布局

`/settings` 单页 + 左侧分区导航：

- 服务与托盘
- LLM 摘要
- 扫描与简报
- 数据与备份

右侧一次只显示当前分区；每个分区独立“保存”。保存中禁用按钮，成功后显示状态，失败保留用户输入并展示原因。

### 6.2 服务与托盘

字段：

- 服务端口（1024–65535，默认 47123）
- 开机自启（现有 HKCU Run）
- 关闭窗口后保持托盘运行（默认开启）
- MCP 启动程序路径（默认自动检测 `agent-memory.exe`）

对应配置：

```toml
[mcp]
binary_path = ""

[desktop]
close_to_tray = true
```

端口变更流程：

1. 校验端口可用。
2. 备份 `config.toml` 与将要修改的 agent MCP 配置。
3. 通过 `agent_link` 记录找到已接入的 Qoder/OpenCode/Claude，使用新端口重写 MCP 配置。
4. 优先在同一进程内重新绑定监听器并重载扫描器；无法热切换时退化为重启 serve，并由桌面应用重新打开页面。
5. 新端口启动失败：恢复旧配置和 agent 文件，重新绑定旧端口。

设置 API 返回新端口和 Web 地址；桌面端自动导航到新地址。无法自动重启时，页面保留旧服务并显示回滚原因。

### 6.3 扫描与简报

字段：

- 扫描间隔（秒，最小 5）
- Qoder / Trae / WorkBuddy 启停
- 简报预算 token
- 最近任务数
- 会话静默时长

保存后自动重新绑定监听器并重载扫描器使配置生效；只有热切换无法完成时才退化为重启 serve。扫描间隔和 agent 开关复用现有 config.toml 字段，不新增重复配置来源。

### 6.4 托盘行为

- “关闭窗口后保持托盘运行”写入 `[desktop] close_to_tray = true/false`。
- 桌面窗口关闭时读取最新配置：true 则隐藏窗口保留托盘；false 则停止 serve 并退出桌面进程。
- 托盘菜单的“开机自启”与设置页共享同一注册表状态。

## 7. LLM 摘要

### 7.1 配置

`config.toml`：

```toml
[llm]
enabled = false
base_url = ""          # 例：https://api.openai.com/v1 或 http://127.0.0.1:11434/v1
model = ""
timeout = "30s"
max_input_chars = 12000
```

API Key：

- 单独存 `~/.agent-memory/llm.key`。
- Windows 使用 DPAPI 加密；非 Windows 回退为 0600 文件。
- 设置接口只返回 `api_key_configured: true/false`，不返回明文。
- 日志、错误信息、导出 JSON 都不包含 Key。

设置页提供“测试连接”：调用一次固定短文本的 `/chat/completions`，显示成功/失败、模型名和耗时；测试不写入摘要。

### 7.2 摘要表

新增：

```sql
CREATE TABLE IF NOT EXISTS summary (
  entity_kind  TEXT NOT NULL,          -- memory | task
  entity_id    INTEGER NOT NULL,
  source_hash  TEXT NOT NULL,
  summary      TEXT NOT NULL DEFAULT '',
  state        TEXT NOT NULL DEFAULT 'pending', -- pending/processing/ready/failed
  model        TEXT NOT NULL DEFAULT '',
  error        TEXT NOT NULL DEFAULT '',
  attempts     INTEGER NOT NULL DEFAULT 0,
  requested_at INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL,
  PRIMARY KEY(entity_kind, entity_id)
);
```

- 不修改 memory/task 表；摘要通过 join 读取。summary 表随现有 schema 以 `CREATE TABLE IF NOT EXISTS` 新建，不提升 schema 版本，旧库无需重建。
- `source_hash` 是参与摘要的源内容哈希；源内容变化才重新排队。
- 启动时把遗留 `processing` 重置为 `pending`。

### 7.3 触发与 worker

- MCP `memory_add` 成功写入原文后，调用 `MarkSummaryPending("memory", id, hash)`；MCP 响应立即返回，附 `summary_state`。
- 任务卡/正式任务同步后，对 task 调用 `MarkSummaryPending`；人工更新任务后也重新排队。
- 后台 worker 每 5 秒取一条 pending/可重试 failed，标记 processing，调用模型，成功后写 ready。
- 失败最多重试 3 次；仍失败标记 failed，保留原文。
- 未启用 LLM 或未配置 base_url/model/Key 时不创建 pending、不启动调用。
- 进程重启后从 summary 表继续处理。

### 7.4 提示词与限制

记忆摘要：

- 输入：记忆原文（最多 `max_input_chars`）。
- 输出：简洁中文条目，保留关键约束/事实，不编造，不超过 300 字。

任务摘要：

- 输入：任务目标、步骤、下一步、最近片段（截断）。
- 输出：1–3 句交接摘要，不超过 400 字。

调用失败、超时、返回空文本都视为失败。

### 7.5 展示规则

- 记忆列表/任务列表：summary ready 时显示摘要，并显示“AI 摘要”标记；否则显示原文/规则字段。`GET /v1/memory`、`GET /v1/tasks`、`GET /v1/tasks/item` 的条目增加 `summary`、`summary_state`、`summary_error` 字段。
- 任务详情：摘要与原始 Goal/NextSteps 都可见；编辑仍保存原文。
- Handoff：任务摘要 ready 时优先使用，原文保留在任务详情。
- 摘要 pending/failed 不阻塞页面，不显示空白；失败可在设置页查看计数并重试。

## 8. 数据与备份

### 8.1 展示

设置页数据分区显示：

- 数据目录
- `memory.db` 大小
- 最近备份时间
- 摘要队列统计（pending/ready/failed）

### 8.2 备份

`POST /v1/data/backup`：

- 使用 SQLite `VACUUM INTO` 生成一致副本。
- 保存到 `<DataDir>/backups/memory-YYYYMMDD-HHMMSS.db`。
- 保留最近 10 份，超出删除最旧备份。
- 返回路径、大小、时间。

### 8.3 JSON 导出

`GET /v1/data/export` 下载 JSON：

```json
{
  "format_version": 1,
  "generated_at": "…",
  "projects": [...],
  "memories": [...],
  "tasks": [...]
}
```

任务包含步骤与摘要（不含 API Key、token、原始会话 transcript）。导出为临时文件，响应后删除。

### 8.4 JSON 导入

`POST /v1/data/import` 分两步：

1. 上传 JSON，返回 `import_token` 与预览：新增/合并的项目、记忆、任务数量，以及错误列表。`import_token` 存数据目录临时文件，10 分钟过期。
2. 用户确认后 `POST /v1/data/import/apply`，携带 `import_token`。

合并规则：

- 项目按 `project_key`。
- 记忆按 `scope + project_key + type + text_norm_hash`，命中则更新摘要/来源。
- 任务优先按来源会话（source_agent + 会话 external_id）合并；没有来源时按项目内相同目标合并；否则创建手动任务。
- 导入前自动备份；格式/字段校验失败在任何写入前拒绝。若写库过程中遇到 SQLite/磁盘错误，接口返回错误并保留备份，可用备份恢复。
- 上传文件上限 50MB；格式版本不符直接拒绝。

### 8.5 打开目录

`POST /v1/data/open` 调用系统文件管理器打开数据目录；CLI/非 Windows 返回“不支持”。

## 9. API 汇总

设置：

```text
GET  /v1/settings
POST /v1/settings/service
POST /v1/settings/llm
POST /v1/settings/scan
POST /v1/settings/llm/test
```

MCP：

```text
GET /v1/mcp/snippets
```

数据：

```text
POST /v1/data/backup
GET  /v1/data/export
POST /v1/data/import
POST /v1/data/import/apply
POST /v1/data/open
```

所有接口继续使用 Bearer；写接口使用 Origin 校验。LLM Key 永不返回。

## 10. UI 页面

- `/settings`：单页 + 左侧四分区的设置中心。
- `/mcp`：MCP 教程与复制。
- `/tasks`：筛选条 + 任务列表。
- 导航：任务 / MCP / 设置入口；窄屏时设置左侧分区折叠为顶部下拉或横向标签。

## 11. 安全与错误处理

- API Key DPAPI/0600，不写日志、不进导出。
- 端口修改失败回滚；agent MCP 配置逐个备份。
- 设置保存失败保留前端输入，不下发半套配置。
- 摘要失败不影响原文、任务状态、Handoff 生成。
- 导入 JSON 严格校验字段/版本；导入前备份；失败整批回滚。
- 备份写盘失败不删除旧备份；备份文件名不可覆盖。
- 非桌面进程或热切换失败时返回 `restart_required`，不假装已生效。

## 12. 测试与验收

- Store：任务筛选组合、摘要表状态、备份、导入合并。
- Daemon：设置读写、端口校验、MCP snippets、LLM test、数据接口鉴权。
- LLM worker：fake OpenAI 服务成功、超时、空返回、重试、未配置零调用。
- DPAPI：Windows 加密/解密；非 Windows 0600 fallback。
- UI：任务筛选 URL 保持；MCP 复制；设置四分区独立保存；端口修改后自动重连；托盘关闭行为；备份/导出/导入预览；摘要成功与失败展示。
- 全部现有测试通过；旧库打开不要求重建。

## 13. 成功标准

- 任务页能按关键词/项目/状态/owner/来源/时间筛选，刷新后条件保留。
- MCP 页生成当前端口/token 的配置，三家已验证配置均可一键复制。
- 设置页四分区独立工作；端口修改自动更新 MCP 配置并重启，失败回滚。
- 未配置 LLM 时所有功能正常；配置后 MCP 记忆与任务能异步生成摘要，原文保留，失败自动退回规则展示。
- 备份、JSON 导出、导入预览与合并可用，导入前自动备份。
- 桌面端与浏览器实测通过，不要求重建数据库。
