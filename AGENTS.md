<!-- agent-memory:begin -->
## 共享记忆（agent-memory）

这个项目接入跨 agent 共享记忆，project_key: `f:/go/agent-memory`。

- 接手前**先读** `.agent-memory/HANDOFF.md`：目标、进度、下一步与硬约束都在里面
- 学到的约定/约束/踩坑用 MCP tool `memory_add` 写回共享库，别只留在本 agent 记忆里
- 需要更多细节时用 `memory_search` 按需检索，不要复述整份简报
- 简报由 `agent-memory scan` 自动更新；手动重生成：`agent-memory handoff --project .`
<!-- agent-memory:end -->
