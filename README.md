# agent-memory

让 Qoder、Trae、WorkBuddy、OpenCode 共用一份记忆。切到另一个 agent 时不必从头描述任务。

M1 只接入 Qoder：只读导入它已有的记忆文件。Trae / WorkBuddy / OpenCode 的适配器留到后续里程碑。

设计文档见 `docs/superpowers/specs/`，实施计划见 `docs/superpowers/plans/`。

## 现在能做什么（M1）

- 只读导入 Qoder 的用户级记忆（`~/.qoder-cn/memory/*.md`）与项目级记忆（`~/.qoder-cn/projects/*/memory/*.md`）
- 归并去重后存进一个共享 SQLite 库；FTS5 + 自切分中文二元组，两字中文词（如「偏好」「命名」）也能召回
- 命令行查、写、列、清理；`doctor` 汇总库状态、agent 发现结果与写入目标

## 安装

    go build ./cmd/agent-memory
    ./agent-memory init

`init` 写 `~/.agent-memory/config.toml` 并建库。要换数据目录用 `init --data-dir <目录>`；**配置已存在时 `--data-dir` 会被忽略并给出提示，必须再加 `--force` 才会改写配置**（否则配置与实际库会不一致）。

## 用法

    agent-memory scan                        # 导入；重复执行幂等（第二次由 unchanged 计数体现）
    agent-memory scan --agent qoder          # 只扫指定 agent，未知名字退出码 2
    agent-memory scan --full                 # 忽略内容哈希全量重解析，用于修好解析失败的源

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

    agent-memory doctor                       # 库状态、写入目标清单、agent 发现结果、未合并项目
    agent-memory doctor --since 24h           # 列出 24h 内变更过的源文件（默认 720h）

记忆类型：`preference` `constraint` `convention` `lesson` `fact` `goal`。
`memory add` 默认 `--type fact`、`--scope user`、`--agent cli`；`--scope project` 必须同时给 `--project <绝对路径>`。该路径若已由 `scan` 登记过就复用那个项目（项目 key 可能是 git remote，不一定是路径），否则新建一个按路径识别的项目。

### `--` 的用法

位置参数以 `-` 开头时（例如以负号开头的文本），在它前面单独写 `--`，之后的所有内容都按文本处理，不再当 flag 解析：

    agent-memory memory add -- "-5% 阈值不触发告警"
    agent-memory memory search -- "-5%"

### 输出与退出码

`memory search` 每条命中依次打印：分数 + `[scope/type]` + 正文、`来源 <agent>: <文件路径>`（可能多行）、`最后见到 <RFC3339 时间>`。分数是 SQLite `bm25()` 的原始值，越负越相关（输出已按相关性升序）。

退出码：`0` 成功；`1` 运行错误（库打不开、`unknown project` 等）；`2` 用法错误（未知子命令/flag、缺参数、非法类型）。

## 只读边界

导入过程只对 agent 目录做 `os.ReadDir` / `os.ReadFile` / `os.Stat`，不写、不改、不删、也不在 `~/.qoder-cn` 下创建任何文件。

`doctor` 会打印完整的写入目标清单，形如：

    写入目标（本程序只会改这几处）:
      <数据目录>                       # 配置里的 data_dir，默认 ~/.agent-memory
      <数据目录>/memory.db             # 共享记忆库
      项目内 .agent-memory/ 与 link 装过的区块（M2/M3 起）

前两行是 M1 实际会写的全部位置；第三行写明 M2/M3 才出现的写入点，M1 不会碰。

验收方法：`scan` 前后对原生 `*.md` 做 md5 对比，无差异即证明未改动；再结合上面这份封闭的写入目标清单，共同说明「没被改」且「无处可改」。

## 它不会做什么

- 不修改任何 agent 的原生记忆文件
- 不保存完整对话内容（只存记忆文件里的条目文本）
- 不自动裁决互相冲突的记忆（冲突条目并存，用检索分数与来源区分）
- M1 不回写、不 link 到项目目录

## 数据位置

- 配置：`~/.agent-memory/config.toml`
- 记忆库：`~/.agent-memory/memory.db`
- 检索：FTS5 + 自切分中文二元组；`trigram` 对两字中文词召回为 0，所以没用
