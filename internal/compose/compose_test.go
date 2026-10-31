package compose

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/extract"
	"github.com/muyi99/agent-memory/internal/store"
)

type fixture struct {
	st        *store.Store
	projectID int64
	ctx       context.Context
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	pid, err := st.EnsureProject(ctx, "f:/go/demo", "demo", "", `F:\go\demo`, now)
	if err != nil {
		t.Fatal(err)
	}

	add := func(scope domain.Scope, pid int64, typ domain.MemoryType, text, agent, path string, seen time.Time) {
		t.Helper()
		_, _, err := st.UpsertMemory(ctx, pid, domain.MemoryDraft{
			Scope: scope, Type: typ, Text: text, SourceAgent: agent, SourcePath: path, Quote: text,
		}, seen)
		if err != nil {
			t.Fatal(err)
		}
	}
	add(domain.ScopeUser, 0, domain.TypePreference, "沟通用中文，提交信息也用中文", "qoder", "/u1.md", now)
	add(domain.ScopeUser, 0, domain.TypePreference, "变量用 snake_case", "trae", "/u2.md", now.Add(time.Minute))
	add(domain.ScopeProject, pid, domain.TypeConstraint, "余额扣减需 WHERE balance >= cost 条件更新", "qoder", "/p1.md", now)
	add(domain.ScopeProject, pid, domain.TypeConvention, "ai_token_log.change_type：1=充值，-1=扣减", "trae", "/p2.md", now)
	add(domain.ScopeProject, pid, domain.TypeFact, "已废弃的旧写法", "qoder", "/p3.md", now)

	ids, err := st.QueryStrings(ctx, `SELECT CAST(id AS TEXT) FROM memory_item WHERE text = ?`, "已废弃的旧写法")
	if err != nil || len(ids) != 1 {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	staleID, err := strconv.ParseInt(ids[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetStaleByID(ctx, staleID, true); err != nil {
		t.Fatal(err)
	}

	// 另一个项目的记忆不能混进本项目简报。
	otherPID, err := st.EnsureProject(ctx, "f:/go/other", "other", "", `F:\go\other`, now)
	if err != nil {
		t.Fatal(err)
	}
	add(domain.ScopeProject, otherPID, domain.TypeConstraint, "别的项目才有的约束", "qoder", "/o1.md", now)

	// 两张任务卡：一新一旧，简报只应带最近 3 张里的前几张
	sid := seedSessionCard(t, st, ctx, "s-old", pid, now.Add(-time.Hour), domain.StatusDone,
		"接入余额校验", "无", []string{`F:\a.go`})
	_ = sid
	seedSessionCard(t, st, ctx, "s-new", pid, now, domain.StatusBlocked,
		"给扣减加乐观锁", "undefined: decrementWithLock", []string{`F:\svc.go`, `F:\dao.go`})

	return &fixture{st: st, projectID: pid, ctx: ctx}
}

func seedSessionCard(t *testing.T, st *store.Store, ctx context.Context, external string, pid int64, at time.Time, status, goal, next string, files []string) int64 {
	t.Helper()
	sid, err := st.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: pid, Title: goal,
		StartedAt: at.Add(-10 * time.Minute), UpdatedAt: at, EventCount: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	card := domain.TaskCard{SessionID: sid, ProjectID: pid, Goal: goal, Status: status,
		NextSteps: next, Files: files, Commands: []string{"go test ./..."}, GeneratedBy: "rule", Confidence: "rule"}
	if status == domain.StatusBlocked {
		card.Errors = []string{next}
	}
	if _, err := st.UpsertTaskCard(ctx, card); err != nil {
		t.Fatal(err)
	}
	return sid
}

func render(t *testing.T, f *fixture, budget int) Result {
	t.Helper()
	c := New(f.st, Config{BudgetTokens: budget, RecentCards: 3})
	res, err := c.Render(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestRender_ContainsAllSixSectionsInFixedOrder(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	for _, want := range []string{
		"## 项目概况",
		"## 用户偏好",
		"## 项目硬约束与约定",
		"## 最近任务",
		"## 未决问题",
		"## 共享记忆入口",
	} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("missing section %q in:\n%s", want, res.Body)
		}
	}
	idx := func(s string) int { return strings.Index(res.Body, s) }
	if !(idx("## 项目概况") < idx("## 用户偏好") && idx("## 用户偏好") < idx("## 项目硬约束与约定") &&
		idx("## 项目硬约束与约定") < idx("## 最近任务") && idx("## 最近任务") < idx("## 未决问题") &&
		idx("## 未决问题") < idx("## 共享记忆入口")) {
		t.Fatalf("sections out of order:\n%s", res.Body)
	}
}

func TestRender_ExcludesStaleAndForeignProject(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	if strings.Contains(res.Body, "已废弃的旧写法") {
		t.Fatal("stale memory must not appear")
	}
	if strings.Contains(res.Body, "别的项目") {
		t.Fatal("foreign project memory leaked")
	}
}

func TestRender_CardsAreMostRecentFirst(t *testing.T) {
	res := render(t, newFixture(t), 1500)
	if strings.Index(res.Body, "给扣减加乐观锁") > strings.Index(res.Body, "接入余额校验") {
		t.Fatalf("cards must be ordered by session recency:\n%s", res.Body)
	}
	if !strings.Contains(res.Body, "状态: blocked") {
		t.Fatalf("status missing:\n%s", res.Body)
	}
}

func TestRender_IsDeterministic(t *testing.T) {
	f := newFixture(t)
	a := render(t, f, 1500)
	b := render(t, f, 1500)
	if a.Body != b.Body {
		t.Fatal("same store state must produce the same bytes")
	}
	if a.ContentHash != b.ContentHash {
		t.Fatal("hash must match content")
	}
}

func TestRender_BudgetTrimsCardDetailBeforeDroppingSections(t *testing.T) {
	// 预算刚好容不下降级后的卡片细节：优先砍细节，再砍卡片数量
	res := render(t, newFixture(t), 90)
	if !res.Truncated {
		t.Fatal("want truncated flag under a tight budget")
	}
	for _, mustKeep := range []string{"## 用户偏好", "## 项目硬约束与约定", "沟通用中文"} {
		if !strings.Contains(res.Body, mustKeep) {
			t.Fatalf("protected section dropped:\n%s", res.Body)
		}
	}
	if strings.Contains(res.Body, "F:\\svc.go") {
		t.Fatalf("card file list should be the first thing cut:\n%s", res.Body)
	}
	if res.Tokens > 90 {
		t.Fatalf("tokens=%d exceeds budget", res.Tokens)
	}
}

func TestRender_ExtremeBudgetStillKeepsPreferencesAndConstraints(t *testing.T) {
	res := render(t, newFixture(t), 5)
	if !strings.Contains(res.Body, "## 用户偏好") || !strings.Contains(res.Body, "## 项目硬约束与约定") {
		t.Fatalf("protected sections must survive any budget:\n%s", res.Body)
	}
	if !strings.Contains(res.Body, "## 共享记忆入口") {
		t.Fatalf("entry hint must survive:\n%s", res.Body)
	}
}

func TestRender_TightBudgetKeepsEveryProtectedItem(t *testing.T) {
	// 预算紧到放不下任务卡时，偏好与硬约束的每一条都必须还在，只允许去掉元信息。
	res := render(t, newFixture(t), 90)
	for _, want := range []string{
		"变量用 snake_case",
		"沟通用中文，提交信息也用中文",
		"余额扣减需 WHERE balance >= cost 条件更新",
		"ai_token_log.change_type：1=充值，-1=扣减",
	} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("protected item %q dropped:\n%s", want, res.Body)
		}
	}
	if strings.Contains(res.Body, "给扣减加乐观锁") {
		t.Fatalf("cards should be gone under this budget:\n%s", res.Body)
	}
	if res.Tokens > 90 || !res.Truncated {
		t.Fatalf("tokens=%d truncated=%v", res.Tokens, res.Truncated)
	}
}

func TestEstimateTokens_CountsCJKAsOnePerChar(t *testing.T) {
	if got := EstimateTokens("中文中文中文"); got < 5 {
		t.Fatalf("cjk undercounted: %d", got)
	}
	if got := EstimateTokens("hello hello hello"); got > 6 {
		t.Fatalf("ascii overcounted: %d", got)
	}
}

// TestRender_RealisticDefaultBudgetKeepsTasks 守住默认预算：真实项目里十二条较长偏好、
// 三条项目约束加上两张任务卡，总量接近实测的 3.4k token。全默认渲染时任务卡与未决问题
// 必须还在，否则简报会退化成只剩偏好清单，接手的人看不出当前在做什么。
func TestRender_RealisticDefaultBudgetKeepsTasks(t *testing.T) {
	prefs := []string{
		"沟通与提交信息统一用中文，代码注释只解释为什么这样做；提交前必须跑 go test ./... 与 go vet ./...，任何一项失败都不允许提交，也不要加 --no-verify 绕过钩子，有疑问先在 PR 里留言讨论。遇到必须放宽规则的情况，先在团队频道同步原因，事后补上自动化测试。",
		"数据库结构变更一律通过版本化迁移脚本执行，禁止手工修改线上表；迁移需要可重复执行，正向与回滚脚本同一次提交，并在 PR 描述中写清影响行数、锁表时长与回滚验证结果。线上执行前先在预发库跑一遍并记录耗时，超过一分钟的变更必须安排在低峰期并提前通知客服与运营。",
		"对外 HTTP 接口字段统一用 snake_case，新增或修改字段必须先更新 openapi.yaml 再改代码；响应体新增字段要说明默认值和空值语义，避免前后端对 null 的理解出现分歧。错误码沿用现有分段规范，新增错误码要同时补 i18n 文案，禁止把内部异常堆栈直接透出给客户端。",
		"所有金额相关计算使用整数分或 decimal 类型，禁止用 float64 参与余额、手续费和汇率换算；涉及余额扣减的 SQL 必须带 WHERE balance >= cost 条件，并检查影响行数是否为 1。对账与退款同样按最小货币单位处理，任何四舍五入都在最终展示层做，并在单测里覆盖边界场景。",
		"日志统一走结构化 logger，禁止在业务代码里直接 fmt.Println；错误日志必须带 trace_id、项目 key 与关键业务参数，敏感字段手机号、身份证、银行卡号在落盘前脱敏。日志级别按环境和错误严重程度区分，生产默认 info，调试日志必须可动态关闭，避免高频循环把磁盘打满。",
		"接口超时与重试策略集中配置：内部 RPC 默认 500ms 超时、最多重试两次并带指数退避；重试只用于幂等操作，非幂等写接口必须先落幂等键，禁止在外面随手包一层 for 循环重试。熔断阈值与重试预算写在配置中心，变更要走评审，禁止在业务代码里硬编码 sleep 或自行实现退避算法。",
		"新增依赖前先确认许可证与维护状态，优先使用标准库；不得引入 cgo 依赖，以免破坏交叉编译；升级依赖单独提交，并在提交信息里注明升级原因与已验证的构建和测试命令。构建产物与锁文件一并提交，CI 使用只读缓存，升级后必须在 linux/amd64 与 darwin/arm64 两个平台各跑一次测试。",
		"单元测试与实现代码同一次提交，覆盖率不强制数字，但核心路径必须有表驱动测试；测试禁止依赖本机时间和网络，使用固定 clock 或注入时间源，失败用例要能在离线环境稳定复现。涉及数据库和文件系统的用例用临时目录与事务回滚隔离，禁止依赖执行顺序，随机种子固定并打印在失败日志里方便复现。",
		"配置项只从环境变量或配置文件读取，禁止在代码里写死地址与密钥；新增配置必须给出默认值并同步更新 README 与示例 config.toml，密钥类配置一律标 0600 且不进 git。本地覆盖配置放 config.local.toml 并加入 gitignore，不允许静默回退到空值。",
		"前端组件状态尽量下沉到最小粒度，避免一个巨型 store 承载所有页面状态；异步请求统一走封装的 request 方法处理错误提示与 loading，不要在组件里直接调用 fetch 或 axios。组件卸载时取消未完成的请求，避免内存泄漏；列表渲染必须带稳定 key，禁止用数组下标代替 id。",
		"代码评审以小步提交为原则，一个 PR 只做一件事，超过四百行改动需要拆分；评审意见用「建议」「必须」区分严重程度，合并前必须解决所有「必须」项并重新跑一遍完整测试。提交信息写清背景、改动与验证命令，方便回溯；回滚时优先 revert 整个提交，不要在线上手改代码绕过发布流程。",
		"文档与注释跟随代码同 PR 更新，README 里的命令必须能直接复制执行；废弃接口先标记 deprecated 并给出替代方案和迁移截止时间，至少保留一个发布周期后再删除。对外文档与内部设计文档分开维护，设计变更先在 docs/design 下留记录，评审通过后再同步到 README 和用户手册。",
	}
	constraints := []string{
		"余额扣减必须使用 UPDATE ... WHERE balance >= cost 的条件更新，禁止先查询再写入；影响行数为 0 时按余额不足处理，不允许把负余额写进账户表。批量扣减按账户 id 升序加锁，避免多笔任务交叉持有锁导致死锁；扣减成功与流水写入必须在同一事务里提交，失败时整体回滚，不允许只写流水不改余额，也不允许先改余额再异步补流水。对账发现负数余额时立即冻结账户并触发人工复核，修复脚本需审批后由值班同学执行。",
		"所有时间字段统一存 UTC，展示层再按用户时区转换；数据库列用 DATETIME(3)，接口输出 RFC3339，禁止在业务代码里用本地时间直接比较或拼接 SQL。接口入参里的时间必须带时区，解析失败直接返回参数错误；跨时区报表按业务所在时区聚合，夏令时切换当天要补一条固定用例。数据库默认值与 Go 侧零值语义必须一致，禁止用 0000-00-00 表示未知时间，未知就用 NULL 并让调用方显式处理。",
		"项目内新增文件必须带用途说明，禁止提交编译产物与本地配置；.agent-memory/ 只保留简报与索引，任何包含密钥或用户隐私的样本数据都不得进入仓库。提交前用 git diff --check 检查空白与冲突标记；大文件与二进制资源走对象存储，仓库里只存地址和校验和。日志、快照与测试夹具不得包含真实用户数据，需要样本时用脱敏脚本生成，并在 README 注明生成命令与数据来源。",
	}
	for i, text := range prefs {
		if n := utf8.RuneCountInString(text); n < 80 || n > 150 {
			t.Fatalf("preference %d has %d runes, want 80-150 to mirror real data", i, n)
		}
	}

	st, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	pid, err := st.EnsureProject(ctx, "f:/go/realistic", "realistic", "", `F:\go\realistic`, now)
	if err != nil {
		t.Fatal(err)
	}

	add := func(scope domain.Scope, projectID int64, typ domain.MemoryType, text string, at time.Time) {
		t.Helper()
		if _, _, err := st.UpsertMemory(ctx, projectID, domain.MemoryDraft{
			Scope: scope, Type: typ, Text: text, SourceAgent: "qoder", SourcePath: "/memory.md", Quote: text,
		}, at); err != nil {
			t.Fatal(err)
		}
	}
	for i, text := range prefs {
		add(domain.ScopeUser, 0, domain.TypePreference, text, now.Add(time.Duration(i)*time.Minute))
	}
	for i, text := range constraints {
		add(domain.ScopeProject, pid, domain.TypeConstraint, text, now.Add(time.Duration(i)*time.Minute))
	}

	seedDetailedCard(t, st, ctx, pid, "s-lock", now, domain.TaskCard{
		Goal:   "给余额扣减补乐观锁，并覆盖并发重复扣款场景",
		Status: domain.StatusBlocked,
		Decisions: []string{
			"扣减改为 UPDATE account SET balance = balance - ?, version = version + 1 WHERE id = ? AND balance >= ? AND version = ?，影响行数为 0 时重读并有限重试，避免先 SELECT 再 UPDATE 的竞态；整段扣减在事务内执行，隔离级别用 READ COMMITTED，并在注释里写明为什么不用 SELECT FOR UPDATE",
			"重试上限 3 次，间隔 20ms/50ms/100ms，超过后返回 ErrConcurrentModify，由上层统一转成可重试错误并记录指标；重试日志带上 account_id、attempt 与 version，方便按账户回放",
			"并发用例用 32 个 goroutine 对同一账户各扣 1 分，断言最终余额恰好减少 32 分，且流水条数与扣款次数一致；再用 -race 跑一遍，确认计数器和随机数没有数据竞争",
			"change_type 沿用 1=充值、-1=扣减，不新增枚举，避免下游报表同时兼容两套口径；报表侧如需新类型，先在数据字典登记并评审，不允许直接上线未知枚举",
			"监控记录重试次数与最终失败数，失败率超过千分之一时在告警群 @ 值班同学；告警消息附最近三次失败请求的 trace_id，减少来回翻日志",
		},
		Files:         []string{`F:\go\agent-memory\internal\store\account.go`, `F:\go\agent-memory\internal\store\account_test.go`, `F:\go\agent-memory\internal\store\db_test.go`, `F:\go\agent-memory\internal\service\deduct.go`, `F:\go\agent-memory\internal\service\deduct_test.go`, `F:\go\agent-memory\migrations\20261027_add_version.sql`, `F:\go\agent-memory\docs\api\account.yaml`, `F:\go\agent-memory\docs\design\deduct-lock.md`},
		Commands:      []string{"go test ./internal/store -run TestDeduct -count=20", "go test ./internal/service -run TestDeduct_Concurrent -count=50", "go test ./internal/service -race", "go vet ./..."},
		NextSteps:     "把并发用例加进 nightly，确认 MySQL 8 与 SQLite 两条链路的锁行为一致后合入 main；补一条回归用例覆盖版本号回绕，并把压测结果贴到 PR 描述里",
		Errors:        []string{"FAIL: TestDeduct_Concurrent 第 17 轮偶发余额变成 -1，怀疑测试库隔离不干净；本地单独跑又能通过，需要确认 nightly 是否复用同一个 schema"},
		OpenQuestions: "失败重试是否要区分余额不足与版本冲突？ / 流水表要不要补 request_id 唯一索引？ / 乐观锁版本号回绕时是否直接拒绝请求？ / 压测并发度按 32 还是按线上峰值 128 配置？",
	})
	seedDetailedCard(t, st, ctx, pid, "s-reconcile", now.Add(-time.Hour), domain.TaskCard{
		Goal:   "接入对账任务失败重试与告警，保证日终账单可追溯",
		Status: domain.StatusDoing,
		Decisions: []string{
			"对账任务按商户分片，单片失败只重跑该片，整体状态用 partial 表示，避免一个商户拖垮全量；分片信息落在批次表，重跑前先核对上一轮处理水位，避免重复记账",
			"重试 3 次后仍失败则落 dead_letter 表，保留原始请求与错误堆栈，人工补跑成功后回写处理人；dead_letter 表带批次号与分片号的唯一索引，人工回放接口必须幂等",
			"告警走现有值班通道，消息里带任务批次号、失败分片数与 dead_letter 条数，避免只报一个失败率；告警恢复后发一条 summary，写清耗时与最终成功分片数",
			"对账结果与财务系统按批次号幂等写入，重复推送直接返回上一次结果，不重复记账；财务侧对账文件按日切分，跨日批次以业务日期为准，不用任务启动时间",
			"批次完成后把明细归档到冷存储，热表只保留最近 7 天，归档任务失败不能阻塞主流程",
		},
		Files:         []string{`F:\go\agent-memory\internal\reconcile\job.go`, `F:\go\agent-memory\internal\reconcile\job_test.go`, `F:\go\agent-memory\internal\reconcile\deadletter.go`, `F:\go\agent-memory\internal\reconcile\replay.go`, `F:\go\agent-memory\migrations\20261027_add_dead_letter.sql`, `F:\go\agent-memory\docs\design\reconcile.md`},
		Commands:      []string{"go test ./internal/reconcile -count=1", "go test ./internal/reconcile -run TestDeadLetterReplay -count=10", "go run ./cmd/reconcile -date 2026-10-27 -dry-run"},
		NextSteps:     "补 dead_letter 表迁移与回放命令，和财务确认 partial 状态的对账口径；回放命令先在预发演练一遍，确认 partial 批次能重新收敛到 done",
		OpenQuestions: "partial 状态要不要同步给财务后台？ / dead_letter 保留 30 天还是 90 天？ / 对账文件缺失时按空文件继续还是整批失败？ / partial 批次超过 24 小时未收敛如何升级告警？",
	})

	// 全默认：预算与卡片数都走 New 内部缺省值。
	res, err := New(st, Config{}).Render(ctx, pid)
	if err != nil {
		t.Fatal(err)
	}
	var sections []string
	for _, s := range []string{"## 项目概况", "## 用户偏好", "## 项目硬约束与约定", "## 最近任务", "## 未决问题", "## 共享记忆入口"} {
		if strings.Contains(res.Body, s) {
			sections = append(sections, s)
		}
	}
	t.Logf("tokens=%d truncated=%v sections=%s", res.Tokens, res.Truncated, strings.Join(sections, " "))
	if res.Truncated {
		t.Fatalf("default budget must keep the brief complete, tokens=%d:\n%s", res.Tokens, res.Body)
	}
	for _, want := range []string{"## 最近任务", "给余额扣减补乐观锁", "## 未决问题"} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("missing %q under the default budget:\n%s", want, res.Body)
		}
	}
	if res.Tokens > 4000 {
		t.Fatalf("tokens=%d exceeds the default budget of 4000", res.Tokens)
	}
}

func seedDetailedCard(t *testing.T, st *store.Store, ctx context.Context, pid int64, external string, at time.Time, card domain.TaskCard) {
	t.Helper()
	sid, err := st.UpsertSession(ctx, domain.Session{
		Agent: "qoder", ExternalID: external, ProjectID: pid, Title: card.Goal,
		StartedAt: at.Add(-30 * time.Minute), UpdatedAt: at, EventCount: 42,
	})
	if err != nil {
		t.Fatal(err)
	}
	card.SessionID = sid
	card.ProjectID = pid
	card.GeneratedBy = "rule"
	card.Confidence = "rule"
	if _, err := st.UpsertTaskCard(ctx, card); err != nil {
		t.Fatal(err)
	}
}

func TestRender_UsesExtractorRulesEndToEnd(t *testing.T) {
	// 简报里的任务卡字段必须与 extract 的产出一致，防止 compose 自己编内容
	f := newFixture(t)
	events := []domain.SessionEvent{
		{Seq: 1, Role: "user", Kind: domain.KindUserText, Text: "写个新目标"},
		{Seq: 2, Role: "assistant", Kind: domain.KindAssistant, Text: "先不动"},
	}
	card, ok := extract.TaskCard(domain.Session{ID: 99, ProjectID: f.projectID}, events, time.Unix(1700000100, 0))
	if !ok {
		t.Fatal("want a card")
	}
	sid, err := f.st.UpsertSession(f.ctx, domain.Session{Agent: "qoder", ExternalID: "s-x",
		ProjectID: f.projectID, Title: card.Goal, StartedAt: time.Unix(1700000100, 0), UpdatedAt: time.Unix(1700000100, 0)})
	if err != nil {
		t.Fatal(err)
	}
	// extract 的输入会话 id 是占位值，落库前必须换成真实会话 id，否则外键拒绝插入。
	card.SessionID = sid
	if _, err := f.st.UpsertTaskCard(f.ctx, card); err != nil {
		t.Fatal(err)
	}
	res := render(t, f, 1500)
	if !strings.Contains(res.Body, "写个新目标") {
		t.Fatalf("extracted goal missing from brief:\n%s", res.Body)
	}
}

func TestRenderIncludesFormalTaskSteps(t *testing.T) {
	f := newFixture(t)
	now := time.Unix(1700000000, 0)
	_, err := f.st.CreateTask(f.ctx, domain.Task{
		ProjectID: f.projectID, Goal: "实现导出", Status: domain.StatusDoing,
		OwnerAgent: "qoder", NextSteps: "补测试",
		CreatedAt: now, UpdatedAt: now,
		Steps: []domain.TaskStep{
			{Subject: "写接口", Status: domain.StepDone},
			{Subject: "补测试", Status: domain.StepDoing},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := New(f.st, Config{}).Render(f.ctx, f.projectID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## 进行中的任务", "实现导出", "1/2", "补测试", "owner: qoder"} {
		if !strings.Contains(res.Body, want) {
			t.Fatalf("handoff missing %q:\n%s", want, res.Body)
		}
	}
}
