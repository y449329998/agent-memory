package ingest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/extract"
	"github.com/muyi99/agent-memory/internal/normalize"
	"github.com/muyi99/agent-memory/internal/store"
)

// Result 是一轮扫描的汇总。Errors 收集「单源失败」而不是整轮失败：
// 一个坏文件不应该挡住其他 agent 的导入。
type Result struct {
	Units     int
	Created   int
	Merged    int
	Unchanged int
	Staled    int

	Sessions         int
	SessionUnchanged int
	Cards            int
	SkippedUnsettled int

	Errors []string
}

func (r Result) String() string {
	base := fmt.Sprintf("units=%d created=%d merged=%d unchanged=%d staled=%d sessions=%d sessions_unchanged=%d cards=%d unsettled=%d",
		r.Units, r.Created, r.Merged, r.Unchanged, r.Staled,
		r.Sessions, r.SessionUnchanged, r.Cards, r.SkippedUnsettled)
	if len(r.Errors) == 0 {
		return base
	}
	return base + fmt.Sprintf(" errors=%d", len(r.Errors))
}

// Runner 编排一轮扫描。它是唯一把「适配器」和「仓储」连起来的层，
// 本身不含任何 agent 专有知识。
type Runner struct {
	store    *store.Store
	adapters []agent.Adapter
	// RemoteReader 可读 git remote；测试里替换掉，默认实现直接解析 .git/config。
	RemoteReader func(dir string) (string, bool, error)
	// SettleIdle 是「会话算写完」的空闲阈值，默认 5 分钟。
	SettleIdle time.Duration
}

func New(st *store.Store, adapters ...agent.Adapter) *Runner {
	return &Runner{
		store:        st,
		adapters:     adapters,
		RemoteReader: normalize.GitRemoteFromConfig,
		SettleIdle:   5 * time.Minute,
	}
}

// ValidateAgent 校验 --agent 过滤值：空串表示全部，其余必须是已配置的适配器名。
func ValidateAgent(known []string, want string) error {
	if want == "" {
		return nil
	}
	for _, name := range known {
		if name == want {
			return nil
		}
	}
	return fmt.Errorf("unknown agent %q (known: %s)", want, strings.Join(known, ", "))
}

// Run 扫描全部（或 onlyAgent 指定的一个）适配器。
// 单个源的失败记进 Result.Errors，不中断整轮；返回的 error 只表示整轮无法继续
// （ctx 取消、库不可用）。
func (r *Runner) Run(ctx context.Context, onlyAgent string) (Result, error) {
	var res Result
	// runAt 只是「轮次序号」，可能因单调递增而超前墙钟；所有时间戳单独取墙钟。
	runAt, err := r.runMarker(ctx)
	if err != nil {
		return res, err
	}
	at := time.Now().Truncate(time.Second)

	var scanned []string
	for _, a := range r.adapters {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if onlyAgent != "" && onlyAgent != a.Name() {
			continue
		}
		units, err := a.ScanMemoryUnits(ctx)
		if err != nil {
			// 整个适配器没扫成：不把它算进本轮「看过的 agent」，
			// 否则会把它的全部记忆误判成已消失。
			res.Errors = append(res.Errors, fmt.Sprintf("%s scan: %v", a.Name(), err))
			continue
		}
		scanned = append(scanned, a.Name())

		for _, u := range units {
			res.Units++
			if err := ctx.Err(); err != nil {
				return res, err
			}

			if u.Err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, u.Err))
				if err := r.store.MarkSourceError(ctx, a.Name(), "memory", u.Path, u.Err.Error(), at.Unix()); err != nil {
					return res, err
				}
				continue
			}

			hash := normalize.HashBytes(u.Content)
			prev, seen, err := r.store.SourceHash(ctx, a.Name(), "memory", u.Path)
			if err != nil {
				return res, err
			}
			if seen && prev == hash {
				res.Unchanged++
				// 内容没变也要登记「本轮见过」，否则 stale 判定会把它当成消失的源
				if err := r.store.TouchSource(ctx, a.Name(), "memory", u.Path, runAt); err != nil {
					return res, err
				}
				continue
			}

			projectID, err := r.projectForDraft(ctx, u.Drafts, at)
			if err != nil {
				// 归属不明的源不写哈希：下一轮还会重试，而不是被当成「已处理」
				res.Errors = append(res.Errors, fmt.Sprintf("%s: project: %v", u.Path, err))
				continue
			}

			failed := false
			for _, d := range u.Drafts {
				_, created, err := r.store.UpsertMemory(ctx, projectID, d, at)
				if err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, err))
					failed = true
					continue
				}
				if created {
					res.Created++
				} else {
					res.Merged++
				}
			}
			if failed {
				// 不推进源哈希：否则下一轮判 Unchanged，这条记忆永远不再重试
				continue
			}

			if err := r.store.PutSource(ctx, domain.SourceState{
				Agent: a.Name(), Kind: "memory", Path: u.Path,
				ContentHash: hash, MTime: u.MTime, Size: u.Size,
			}, runAt); err != nil {
				return res, err
			}
		}

		sUnits, err := a.ScanSessionUnits(ctx)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s sessions: %v", a.Name(), err))
			continue
		}
		for _, u := range sUnits {
			res.Sessions++
			if err := ctx.Err(); err != nil {
				return res, err
			}

			if u.Err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, u.Err))
				if err := r.store.MarkSourceError(ctx, a.Name(), "session", u.Path, u.Err.Error(), runAt); err != nil {
					return res, err
				}
				continue
			}

			// 会话与记忆共用同一套源门控：内容没变就只登记「本轮见过」，
			// 不重写会话与事件（事件整体替换的代价高，且会丢掉已有的卡）。
			hash := normalize.HashBytes(u.Content)
			prev, seen, err := r.store.SourceHash(ctx, a.Name(), "session", u.Path)
			if err != nil {
				return res, err
			}
			if seen && prev == hash {
				res.SessionUnchanged++
				if err := r.store.TouchSource(ctx, a.Name(), "session", u.Path, runAt); err != nil {
					return res, err
				}
				// 首次扫到还在写的会话时不出卡；等内容静默后哈希已不变，
				// 必须在这里补一次出卡机会，否则这场会话永远拿不到卡。
				added, err := r.ensureTaskCard(ctx, a.Name(), u, time.Now())
				if err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: card: %v", u.Path, err))
				} else if added {
					res.Cards++
				}
				continue
			}

			projectID, err := r.resolveProject(ctx, a.Name(), u.Session.ProjectSlug, u.Session.ProjectPath, u.Session.Confidence, at)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: project: %v", u.Path, err))
				continue
			}

			sess := u.Session
			sess.Agent = a.Name()
			sess.ProjectID = projectID
			sessID, err := r.store.UpsertSession(ctx, sess)
			if err != nil {
				res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, err))
				continue
			}

			events := agent.EventsWithSession(sessID, u.Events)
			if err := r.store.ReplaceEvents(ctx, sessID, events); err != nil {
				return res, err
			}

			// 是否出卡只看源文件 mtime：还在写的会话只导入事件，等下一轮再提炼。
			if extract.Settled(u.MTime, time.Now(), r.SettleIdle) {
				card, ok := extract.TaskCard(sess, events, time.Now())
				if ok {
					card.SessionID = sessID
					card.ProjectID = projectID
					if _, err := r.store.UpsertTaskCard(ctx, card); err != nil {
						res.Errors = append(res.Errors, fmt.Sprintf("%s: card: %v", u.Path, err))
					} else {
						res.Cards++
					}
				}
			} else {
				res.SkippedUnsettled++
			}

			if err := r.store.PutSource(ctx, domain.SourceState{
				Agent: a.Name(), Kind: "session", Path: u.Path,
				ContentHash: hash, MTime: u.MTime, Size: u.Size,
			}, runAt); err != nil {
				return res, err
			}
		}
	}

	// 全部适配器跑完后再统一判定：每个 agent 单独判一次会误伤共同支撑的条目。
	n, err := r.store.MarkStaleForRun(ctx, scanned, runAt)
	if err != nil {
		return res, err
	}
	res.Staled += n
	return res, nil
}

// ensureTaskCard 给「内容未变」的会话补一次出卡机会。
//
// 首次扫到还在写的会话时只导入事件、不出卡（正确）；等它静默后内容不再变化，
// 后续扫描会走进未变分支。若这里不补卡，这张卡就永远不会生成。
//
// 只补「没有卡」的会话：已有卡可能是人工或后续 LLM 修订过的，重写会冲掉这些改动。
// 连接数为 1，所有查询必须顺序执行：先取会话主键，再判卡是否存在，
// 最后读事件（rows 由 EventsFor 自行读尽并关闭），不能边遍历 rows 边查库。
func (r *Runner) ensureTaskCard(ctx context.Context, agentName string, u agent.SessionUnit, now time.Time) (bool, error) {
	sessID, ok, err := r.store.SessionIDByExternal(ctx, agentName, u.Session.ExternalID)
	if err != nil {
		return false, err
	}
	if !ok {
		// 上一轮可能没导入成功（例如项目归属失败）：跳过不算错误，
		// 等内容变化时仍会走正常导入路径。
		return false, nil
	}

	if _, err := r.store.TaskCardBySession(ctx, sessID); err == nil {
		return false, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	if !extract.Settled(u.MTime, now, r.SettleIdle) {
		return false, nil
	}

	sess, ok, err := r.store.SessionByID(ctx, sessID)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	events, err := r.store.EventsFor(ctx, sessID)
	if err != nil {
		return false, err
	}
	card, ok := extract.TaskCard(sess, events, now)
	if !ok {
		return false, nil
	}
	card.SessionID = sessID
	card.ProjectID = sess.ProjectID
	if _, err := r.store.UpsertTaskCard(ctx, card); err != nil {
		return false, err
	}
	return true, nil
}

// runMarker 生成本轮的唯一标记。last_seen_run 的语义是「哪一轮见过」，不是墙上时间：
// Unix 秒精度下同一秒内连跑两次会拿到同一个值，stale 判定就会把上一轮的来源
// 当成「本轮见过」，源文件消失后永远标不出过时。因此标记必须严格递增。
//
// 注意：它只保证单调递增，可能超前墙钟（同一秒内连跑 30 轮就会超前 30 秒），
// 绝不能当时间戳使用；first_seen_at / changed_at / project.updated_at 等一律另取墙钟。
func (r *Runner) runMarker(ctx context.Context) (int64, error) {
	now := time.Now().Unix()
	maxSeen, err := r.store.QueryInt(ctx, `SELECT COALESCE(MAX(last_seen_run), 0) FROM source`)
	if err != nil {
		return 0, err
	}
	if last := int64(maxSeen); now <= last {
		return last + 1, nil
	}
	return now, nil
}

// projectForDraft 决定这批草稿属于哪个项目。空草稿或用户级草稿返回 0。
func (r *Runner) projectForDraft(ctx context.Context, drafts []domain.MemoryDraft, at time.Time) (int64, error) {
	if len(drafts) == 0 {
		return 0, nil
	}
	d := drafts[0]
	if d.Scope != domain.ScopeProject {
		return 0, nil
	}
	return r.resolveProject(ctx, d.SourceAgent, d.ProjectSlug, d.ProjectPath, d.Confidence, at)
}

// resolveProject 是记忆与会话共用的项目归属逻辑。
//
// 低置信（只有 slug 反解出来的路径）时故意建一个 agent 专有的独立项目，
// 并且不写 project_path 映射：slug 里的 '-' 既可能是层级分隔符也可能是项目名的一部分，
// 猜错就会把两个不同项目并成一个，那比留两个孤立项目更糟。
//
// 高置信时先按路径反查——同一份 checkout 重扫应当命中同一个项目；
// 未命中再读 .git/config 的 remote，让同一仓库的多份 checkout 合并成一个项目。
func (r *Runner) resolveProject(ctx context.Context, agent, slug, path, confidence string, at time.Time) (int64, error) {
	if confidence != "high" || path == "" {
		if slug == "" {
			return 0, fmt.Errorf("project without usable path or slug (agent %s)", agent)
		}
		return r.store.EnsureProject(ctx, agent+":"+slug, slug, "", "", at)
	}

	if id, ok, err := r.store.ResolveProjectByPath(ctx, path); err != nil {
		return 0, err
	} else if ok {
		// 同一路径已经归属某个项目：补记本 agent 的 slug 关联，映射表随扫描收敛
		if err := r.store.LinkProjectPath(ctx, id, agent, slug, path, "high"); err != nil {
			return 0, err
		}
		return id, nil
	}

	remote := ""
	// 读不到 remote 不算致命：退化为路径 key，只是少了跨 checkout 合并的能力
	if r.RemoteReader != nil {
		if v, ok, err := r.RemoteReader(path); err == nil && ok {
			remote = v
		}
	}
	id, _, err := r.store.ProjectIDForPath(ctx, agent, slug, path, remote, at)
	return id, err
}
