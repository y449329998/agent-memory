# agent-memory

让 Qoder、Trae、WorkBuddy、OpenCode 共用一份记忆。切到另一个 agent 时不必从头描述任务。

M1 接入 Qoder 的记忆文件；M2 起再导入它的会话历史，提炼任务卡并生成项目交接简报。Trae / WorkBuddy / OpenCode 的适配器留到后续里程碑。

设计文档见 `docs/superpowers/specs/`，实施计划见 `docs/superpowers/plans/`。

## 现在能做什么（M2）

- 只读导入 Qoder 的用户级记忆（`~/.qoder-cn/memory/*.md`）与项目级记忆（`~/.qoder-cn/projects/*/memory/*.md`）
- 只读导入 Qoder 会话历史（`~/.qoder-cn/projects/*/*.jsonl`），把写完的会话提炼成任务卡（目标 / 状态 / 改动文件 / 下一步 / 未决问题）；还在写的会话（默认 5 分钟内改过）只入库、不出卡
- 归并去重后存进一个共享 SQLite 库；FTS5 + 自切分中文二元组，两字中文词（如「偏好」「命名」）也能召回
- 为有内容的项目生成 `.agent-memory/HANDOFF.md`：切到别的 agent 时先读它，不用重新描述任务
- `agent-memory handoff --project <路径> --print` 随时重看简报；`scan` 结束会自动刷新有项目级记忆或任务卡、且目录仍存在的项目，内容没变则不重写
- 命令行查、写、列、清理；`doctor` 汇总库状态、会话 / 任务卡 / 简报计数、agent 发现结果与写入目标

## 安装

    go build ./cmd/agent-memory
    ./agent-memory init

`init` 写 `~/.agent-memory/config.toml` 并建库。要换数据目录用 `init --data-dir <目录>`；**配置已存在时 `--data-dir` 会被忽略并给出提示，必须再加 `--force` 才会改写配置**（否则配置与实际库会不一致）。

## 用法

    agent-memory scan                        # 导入记忆与会话，并刷新简报；重复执行幂等（第二次由 unchanged / sessions_unchanged 计数体现）
    agent-memory scan --agent qoder          # 只扫指定 agent，未知名字退出码 2
    agent-memory scan --full                 # 忽略内容哈希全量重解析，用于修好解析失败的源

    agent-memory handoff --project F:/go-hiprint          # 写 <项目>/.agent-memory/HANDOFF.md
    agent-memory handoff --project F:/go-hiprint --print  # 只打印到标准输出，不写项目文件
    agent-memory handoff --project F:/go-hiprint --print --budget 4096  # 临时放宽简报预算

    agent-memory memory search 偏好           # 中文检索，默认 limit 20
    agent-memory memory search 命名 --limit 5 # flag 可以写在关键词后面
    agent-memory memory search 部署 --project F:/go/agent-memory   # 叠加该项目记忆（用户级始终参与）
    agent-memory memory search 部署 --type convention --include-stale

    agent-memory memory add "提交前先跑 go test" --type convention
    agent-memory memory add "..." --scope project --project F:/go/agent-memory

    agent-memory memory list                  # 默认只列用户级
    agent-memory memory list --project F:/go/agent-memory   # 路径或 project key；此时列出该项目级 + 用户级
    agent-memory memory list --include-stale  # 带上已过时条目

    agent-memory memory purge                 # 不加 --yes 只报过时条数
    agent-memory memory purge --yes           # 真删

    agent-memory doctor                       # 库状态、会话/任务卡/简报计数、写入目标清单、agent 发现结果、未合并项目
    agent-memory doctor --since 24h           # 列出 24h 内变更过的源文件（默认 720h）

记忆类型：`preference` `constraint` `convention` `lesson` `fact` `goal`。
`memory add` 默认 `--type fact`、`--scope user`、`--agent cli`；`--scope project` 必须同时给 `--project <绝对路径>`。该路径若已由 `scan` 登记过就复用那个项目（项目 key 可能是 git remote，不一定是路径），否则新建一个按路径识别的项目。

### 交接简报

`handoff` 的正文分六节：项目概况、用户偏好、项目硬约束与约定、最近任务、未决问题、共享记忆入口。写盘时用临时文件 + rename 原子替换；内容完全相同则报「未变化」，不碰文件。`.agent-memory/` 会加进 `.git/info/exclude`（只影响本机，不改 `.gitignore`）。

默认预算 4000 token（真实项目六节齐全约 3.5k）；超预算时依次压缩任务卡细节、减少卡片数量、省略任务卡与未决问题，用户偏好与项目硬约束不砍。真实数据条目更多时用 `--budget` 或配置里的 `[handoff] budget_tokens` 放宽。

### `--` 的用法

位置参数以 `-` 开头时（例如以负号开头的文本），在它前面单独写 `--`，之后的所有内容都按文本处理，不再当 flag 解析：

    agent-memory memory add -- "-5% 阈值不触发告警"
    agent-memory memory search -- "-5%"

### 输出与退出码

`memory search` 每条命中依次打印：分数 + `[scope/type]` + 正文、`来源 <agent>: <文件路径>`（可能多行）、`最后见到 <RFC3339 时间>`。分数是 SQLite `bm25()` 的原始值，越负越相关（输出已按相关性升序）。

退出码：`0` 成功；`1` 运行错误（库打不开、`unknown project` 等）；`2` 用法错误（未知子命令/flag、缺参数、非法类型）。

## 只读边界与写入点

导入记忆与会话的过程只对 agent 目录做 `os.ReadDir` / `os.ReadFile` / `os.Stat`，不写、不改、不删、也不在 `~/.qoder-cn` 下创建任何文件。

M2 的新增写入点只有项目内 `.agent-memory/HANDOFF.md` 与 `.git/info/exclude` 的一行 `/.agent-memory/`：`scan` 只为「有项目级记忆或任务卡、且目录仍存在」的项目刷新简报；显式 `handoff --print` 不写任何项目文件。不会碰被 git 跟踪的文件。

`doctor` 会打印完整的写入目标清单，形如：

    写入目标（本程序只会改这几处）:
      <数据目录>                       # 配置里的 data_dir，默认 ~/.agent-memory
      <数据目录>/memory.db             # 共享记忆库
      项目内 .agent-memory/ 与 link 装过的区块（M2/M3 起）

实际目前只会写前两行，以及项目内 `.agent-memory/HANDOFF.md`；link 装区块是 M3 的事。

验收方法：`scan` / `handoff` 前后对原生 `*.md` 与 `*.jsonl` 做 md5 对比，无差异即证明未改动；再结合上面这份封闭的写入目标清单，共同说明「没被改」且「无处可改」。注意正在运行的 agent 自己会持续写当前会话的 jsonl，验收窗口内这类活跃会话文件可能变化，需单独核对。

## 它不会做什么

- 不修改任何 agent 的原生记忆文件或会话文件
- 不保存完整对话原文：会话只保留裁剪后的事件（单条最多 500 字符）与提炼出的任务卡
- 不自动裁决互相冲突的记忆（冲突条目并存，用检索分数与来源区分）
- 不把内容写进被 git 跟踪的文件：只写 `.agent-memory/`（已加进 `.git/info/exclude`），不改 `.gitignore` 与业务代码；link 装区块留到 M3

## 数据位置

- 配置：`~/.agent-memory/config.toml`（`[handoff]` 段可调 `budget_tokens` / `recent_cards` / `settle_idle`）
- 记忆库：`~/.agent-memory/memory.db`
- 项目简报：`<项目>/.agent-memory/HANDOFF.md`
- 检索：FTS5 + 自切分中文二元组；`trigram` 对两字中文词召回为 0，所以没用
