package ingest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
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
	Errors    []string
}

func (r Result) String() string {
	base := fmt.Sprintf("units=%d created=%d merged=%d unchanged=%d staled=%d",
		r.Units, r.Created, r.Merged, r.Unchanged, r.Staled)
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
}

func New(st *store.Store, adapters ...agent.Adapter) *Runner {
	return &Runner{store: st, adapters: adapters, RemoteReader: normalize.GitRemoteFromConfig}
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
	runAt, err := r.runMarker(ctx)
	if err != nil {
		return res, err
	}
	at := time.Unix(runAt, 0)

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
				if err := r.store.MarkSourceError(ctx, a.Name(), "memory", u.Path, u.Err.Error(), runAt); err != nil {
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

			projectID, err := r.projectFor(ctx, u.Drafts, at)
			if err != nil {
				// 归属不明的源不写哈希：下一轮还会重试，而不是被当成「已处理」
				res.Errors = append(res.Errors, fmt.Sprintf("%s: project: %v", u.Path, err))
				continue
			}

			for _, d := range u.Drafts {
				_, created, err := r.store.UpsertMemory(ctx, projectID, d, at)
				if err != nil {
					res.Errors = append(res.Errors, fmt.Sprintf("%s: %v", u.Path, err))
					continue
				}
				if created {
					res.Created++
				} else {
					res.Merged++
				}
			}

			if err := r.store.PutSource(ctx, domain.SourceState{
				Agent: a.Name(), Kind: "memory", Path: u.Path,
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

// runMarker 生成本轮的唯一标记。last_seen_run 的语义是「哪一轮见过」，不是墙上时间：
// Unix 秒精度下同一秒内连跑两次会拿到同一个值，stale 判定就会把上一轮的来源
// 当成「本轮见过」，源文件消失后永远标不出过时。因此标记必须严格递增。
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

// projectFor 决定这批草稿属于哪个项目。
//
// 低置信（只有 slug 反解出来的路径）时故意建一个 agent 专有的独立项目，
// 并且不写 project_path 映射：slug 里的 '-' 既可能是层级分隔符也可能是项目名的一部分，
// 猜错就会把两个不同项目并成一个，那比留两个孤立项目更糟。
//
// 高置信时先按路径反查——同一份 checkout 重扫应当命中同一个项目；
// 未命中再读 .git/config 的 remote，让同一仓库的多份 checkout 合并成一个项目。
func (r *Runner) projectFor(ctx context.Context, drafts []domain.MemoryDraft, at time.Time) (int64, error) {
	if len(drafts) == 0 {
		return 0, nil
	}
	d := drafts[0]
	if d.Scope != domain.ScopeProject {
		return 0, nil
	}

	if d.Confidence != "high" || d.ProjectPath == "" {
		if d.ProjectSlug == "" {
			return 0, fmt.Errorf("project-scope memory without usable path or slug: %s", d.SourcePath)
		}
		return r.store.EnsureProject(ctx, d.SourceAgent+":"+d.ProjectSlug, d.ProjectSlug, "", "", at)
	}

	path := d.ProjectPath
	if id, ok, err := r.store.ResolveProjectByPath(ctx, path); err != nil {
		return 0, err
	} else if ok {
		// 同一路径已经归属某个项目：补记本 agent 的 slug 关联，映射表随扫描收敛
		if err := r.store.LinkProjectPath(ctx, id, d.SourceAgent, d.ProjectSlug, path, "high"); err != nil {
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
	id, _, err := r.store.ProjectIDForPath(ctx, d.SourceAgent, d.ProjectSlug, path, remote, at)
	return id, err
}
