# M1 核心存储与 Qoder 记忆检索 · 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 建成 agent-memory 的存储底座——把 Qoder 的用户级与项目级记忆导入 SQLite，得到中文可检索、跨来源去重、重扫幂等的共享记忆库与命令行。

**Architecture:** 单二进制分层。`agent` 包只负责「解析原生文件 → 结构化草稿」，不碰数据库；`store` 包是唯一写入口（SQLite WAL + FTS5）；`ingest` 编排扫描并按内容哈希做幂等门控；`cli` 暴露 `init/scan/memory/doctor`。检索层用自切分二元组索引，解决 FTS5 默认分词搜不到中文。

**Tech Stack:** Go 1.22.6（windows-amd64，`CGO_ENABLED=0`）、`modernc.org/sqlite` v1.34.4（纯 Go，实测支持 FTS5 + JSON1，内含 SQLite 3.46.0）、`golang.org/x/text` v0.16.0（NFKC）、`github.com/BurntSushi/toml` v1.3.2。四个依赖均已在本机 `go get` 验证可装。

**上游 spec:** `docs/superpowers/specs/2026-10-27-agent-memory-design.md`。M2（会话→任务卡→交接简报）与 M3（MCP + stdio 桥 + link + watcher）各自独立成计划。

---

## 开工约定

- 模块路径 `github.com/muyi99/agent-memory`；所有命令在项目根 `F:\go\agent-memory` 执行（Git Bash）。
- 每个 Task 结束时提交一次，提交信息用中文。
- 测试一律用 `t.TempDir()` 造假目录，**禁止**读写真实 `~/.qoder-cn`；真实数据只在 Task 16 手工验收里出现。
- 时间戳在库里存 Unix 秒（INTEGER），Go 侧用 `time.Time`，转换只发生在 `store` 层。
- `project_id` 用 `0` 表示用户级而不是 NULL：SQLite 唯一索引把 NULL 视为互不相等，NULL 方案下用户级记忆永远去不了重。
- 本计划只建库不锁库：`serve` 在 M3 才出现，M1 的 CLI 直接开库（WAL 支持多进程，busy_timeout 兜住）。

---

## 文件结构

```
go.mod                                     依赖
cmd/agent-memory/main.go                   入口，退出码
internal/domain/types.go                   跨层数据结构，零依赖
internal/normalize/segment.go              中文二元组切分（索引与查询共用）
internal/normalize/memory.go               去重用的文本归一化与哈希
internal/normalize/project.go              project_key 归一化 + 读 .git/config 的 remote
internal/store/schema.go                   建表 SQL（含 M2/M3 用到的表，一次建全）
internal/store/store.go                    Open/Close、PRAGMA、schema 版本
internal/store/project.go                  项目与路径映射仓储
internal/store/memory.go                   记忆 upsert / 列表 / stale / purge
internal/store/search.go                   FTS5 检索，实现 SearchProvider
internal/store/source.go                   扫描源游标、错误、变更历史、统计
internal/agent/adapter.go                  Adapter 接口与产出单元类型
internal/agent/qoder/format.go             Qoder frontmatter 记忆文件解析
internal/agent/qoder/paths.go              目录发现 + slug → 真实路径
internal/agent/qoder/adapter.go            产出 MemoryUnit
internal/ingest/ingest.go                  扫描编排 + 哈希门控 + stale 标记
internal/config/config.go                  配置默认值与加载
internal/cli/root.go                       子命令分发
internal/cli/init.go                       init
internal/cli/scan.go                       scan
internal/cli/memory.go                     memory search|add|list|purge
internal/cli/doctor.go                     doctor
internal/agent/qoder/testdata/             解析器 fixture
```

每个文件一个职责：`normalize` 是纯函数集合（最易测），`store` 是唯一持有 `*sql.DB` 的包，`agent/qoder` 是唯一知道 Qoder 磁盘布局的包，`cli` 是唯一碰 stdout 的包。`ingest` 只做编排，不含任何 agent 专有知识。

---

## Task 1: 模块初始化与入口

**Files:**
- Create: `go.mod`
- Create: `cmd/agent-memory/main.go`
- Create: `internal/cli/root.go`
- Create: `internal/cli/root_test.go`
- Create: `.gitignore`

- [ ] **Step 1: 写 go.mod**

```
module github.com/muyi99/agent-memory

go 1.22
```

- [ ] **Step 2: 写失败测试 `internal/cli/root_test.go`**

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun_UnknownCommandExit2(t *testing.T) {
	var out bytes.Buffer
	code := Run([]string{"nope"}, &out, &out)
	if code != 2 {
		t.Fatalf("exit code = %d, want 2", code)
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Fatalf("want usage text, got %q", out.String())
	}
}

func TestRun_VersionPrintsVersion(t *testing.T) {
	var out bytes.Buffer
	if code := Run([]string{"version"}, &out, &out); code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "agent-memory ") {
		t.Fatalf("got %q", out.String())
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/cli/ -v`
Expected: FAIL，`undefined: Run`

- [ ] **Step 4: 写 `internal/cli/root.go`**

```go
package cli

import (
	"fmt"
	"io"
	"sort"
)

const Version = "0.1.0-m1"

type command struct {
	name string
	desc string
	run  func(args []string, stdout, stderr io.Writer) int
}

var commands = map[string]command{}

func register(c command) { commands[c.name] = c }

func usage(w io.Writer) {
	fmt.Fprintln(w, "usage: agent-memory <command> [args]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "commands:")
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(w, "  %-10s %s\n", name, commands[name].desc)
	}
}

// Run 分发子命令。0 成功，1 运行错误，2 用法错误。
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	switch args[0] {
	case "help", "-h", "--help":
		usage(stdout)
		return 0
	case "version", "--version":
		fmt.Fprintf(stdout, "agent-memory %s\n", Version)
		return 0
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[0])
		usage(stderr)
		return 2
	}
	return cmd.run(args[1:], stdout, stderr)
}
```

- [ ] **Step 5: 写 `cmd/agent-memory/main.go`**

```go
package main

import (
	"os"

	"github.com/muyi99/agent-memory/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 6: 写 `.gitignore`**

```
/agent-memory.exe
/agent-memory
*.test
```

- [ ] **Step 7: 跑测试确认通过**

Run: `go test ./internal/cli/ -v`
Expected: PASS（2 个测试）

- [ ] **Step 8: 编译入口**

Run: `go build ./...`
Expected: 无输出、退出码 0

- [ ] **Step 9: 提交**

```bash
git add go.mod cmd internal/cli .gitignore
git commit -m "chore: 初始化 Go 模块与命令行入口骨架"
```

---

## Task 2: domain 数据结构

只定义类型，不含逻辑，因此不写测试（无可断言行为）。后续 Task 的签名全部引用这里的名字，先落地能避免连锁改动。

**Files:**
- Create: `internal/domain/types.go`

- [ ] **Step 1: 写 `internal/domain/types.go`**

```go
package domain

import (
	"context"
	"time"
)

type Scope string

const (
	ScopeUser    Scope = "user"
	ScopeProject Scope = "project"
)

type MemoryType string

const (
	TypePreference MemoryType = "preference"
	TypeConstraint MemoryType = "constraint"
	TypeConvention MemoryType = "convention"
	TypeLesson     MemoryType = "lesson"
	TypeFact       MemoryType = "fact"
	TypeGoal       MemoryType = "goal"
)

func ValidMemoryType(t MemoryType) bool {
	switch t {
	case TypePreference, TypeConstraint, TypeConvention, TypeLesson, TypeFact, TypeGoal:
		return true
	}
	return false
}

// MemoryDraft 是适配器产出的记忆条目，尚未落库。
type MemoryDraft struct {
	Scope       Scope
	Type        MemoryType
	Text        string
	SourceAgent string
	SourcePath  string
	Quote       string
	ProjectPath string
	ProjectSlug string
	Confidence  string
	SeenAt      time.Time
}

type MemorySource struct {
	MemoryID int64
	Agent    string
	Path     string
	Quote    string
}

type MemoryItem struct {
	ID           int64
	Scope        Scope
	ProjectID    int64
	Type         MemoryType
	Text         string
	TextNormHash string
	Stale        bool
	FirstSeenAt  time.Time
	LastSeenAt   time.Time
	EmbedModel   string
	EmbedDim     int
	EmbeddedAt   time.Time
	Sources      []MemorySource
}

type Project struct {
	ID           int64
	Key          string
	DisplayName  string
	GitRemote    string
	PrimaryPath  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type SearchQuery struct {
	Text         string
	ProjectID    int64
	Scope        Scope
	Type         MemoryType
	IncludeStale bool
	Limit        int
}

type Hit struct {
	Memory MemoryItem
	Score  float64
	Origin string
}

// SearchProvider 是检索抽象。本期只有 ftsProvider 一个实现；
// M5 的向量与 RRF 融合通过新增实现接入，上层不变。
type SearchProvider interface {
	Search(ctx context.Context, q SearchQuery) ([]Hit, error)
}

type SourceState struct {
	Agent       string
	Kind        string
	Path        string
	ContentHash string
	PrevHash    string
	ChangedAt   time.Time
	LastError   string
	LastSeenRun int64
	MTime       time.Time
	Size        int64
}

type Stats struct {
	Projects     int
	Memories     int
	Stale        int
	Sources      int
	FailedSource int
	Changed      int
	MemoriesByAgent map[string]int
	MemoriesByType  map[string]int
}
```

`IngestResult` 不在这里定义：它属于 `ingest` 包的编排结果，放在 `internal/ingest/ingest.go`（Task 13）。`domain` 只放跨层数据结构，不放各包自己的返回值。

- [ ] **Step 2: 编译**

Run: `go build ./...`
Expected: 无输出、退出码 0

- [ ] **Step 3: 提交**

```bash
git add internal/domain/types.go
git commit -m "feat: 定义跨层数据结构 domain"
```

---

## Task 3: 中文分词 Segment / QueryTokens

这是本项目最容易做错的模块：索引与查询必须用同一套切分规则，否则 FTS5 一定搜不到中文。实测依据见 spec 5.3。

**Files:**
- Create: `internal/normalize/segment.go`
- Test: `internal/normalize/segment_test.go`

- [ ] **Step 1: 写失败测试 `internal/normalize/segment_test.go`**

```go
package normalize

import (
	"sort"
	"strings"
	"testing"
)

func TestSegment_IndexesSingleCharsAndBigrams(t *testing.T) {
	got := Segment("用户偏好 snake_case 命名")
	want := []string{"用", "用户", "户", "户偏", "偏", "偏好", "好", "snake_case", "命", "命名", "名"}
	sort.Strings(want)
	sort.Strings(got)
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestSegment_SplitsAtPunctuation(t *testing.T) {
	got := Segment("查询构造器，闭包外用 $list")
	// 索引侧同时输出单字与二元组，二者在切片里交替出现（查、查询、询、询构…），
	// 所以只能按 token 判断，不能断言二元组在拼接串里连续出现。
	tokens := make(map[string]bool, len(got))
	for _, tok := range got {
		tokens[tok] = true
	}
	for _, want := range []string{"查询", "询构", "构造"} {
		if !tokens[want] {
			t.Fatalf("missing bigram %q in %v", want, got)
		}
	}
	// 标点必须切断汉字串：不能出现跨「，」或「$」的二元组
	for _, bad := range []string{"器闭", "用l"} {
		if tokens[bad] {
			t.Fatalf("punctuation did not split the run: found %q in %v", bad, got)
		}
	}
	if !tokens["list"] {
		t.Fatalf("expected ascii token list, got %v", got)
	}
}

func TestSegment_KeepsHyphenatedAndUnderscoreIdentifiers(t *testing.T) {
	got := strings.Join(Segment("nuonuo-fresh3-api similar_words"), "|")
	for _, tok := range []string{"nuonuo-fresh3-api", "similar_words"} {
		if !strings.Contains(got, tok) {
			t.Fatalf("token %q missing in %q", tok, got)
		}
	}
}

func TestQueryTokens_DropsSingleCharsForAdjacentPhrase(t *testing.T) {
	got := QueryTokens("中文偏好")
	join := strings.Join(got, "|")
	if join != "中文|文偏|偏好" {
		t.Fatalf("got %q, want %q", join, "中文|文偏|偏好")
	}
}

func TestQueryTokens_FallsBackToSingleChar(t *testing.T) {
	got := QueryTokens("币")
	if len(got) != 1 || got[0] != "币" {
		t.Fatalf("got %v, want [币]", got)
	}
}

func TestQueryTokens_AsciiOnlyQuery(t *testing.T) {
	got := QueryTokens("snake_case")
	if len(got) != 1 || got[0] != "snake_case" {
		t.Fatalf("got %v", got)
	}
}

func TestMatchExpr_QuotesEveryToken(t *testing.T) {
	got := MatchExpr(`a"b`)
	if got == "" {
		t.Fatal("empty expr")
	}
	if strings.Count(got, `"`)%2 != 0 {
		t.Fatalf("unbalanced quotes: %q", got)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/normalize/ -v`
Expected: FAIL，`undefined: Segment`

- [ ] **Step 3: 写 `internal/normalize/segment.go`**

```go
package normalize

import (
	"strings"
	"unicode"
)

// isHan 用 Unicode 脚本属性判断汉字。汉字记忆是本项目的主要语料。
func isHan(r rune) bool {
	return unicode.Is(unicode.Han, r)
}

func isWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-'
}

// Segment 产出索引用 token：连续汉字串切成「单字 + 相邻二元组」，
// 字母数字词（含 _ 与 -）原样小写保留。切分边界由任何非标点字母的字符决定。
func Segment(s string) []string {
	var out []string
	var word, han []rune

	flushWord := func() {
		if len(word) > 0 {
			out = append(out, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		for i := 0; i < len(han); i++ {
			out = append(out, string(han[i]))
			if i+1 < len(han) {
				out = append(out, string(han[i:i+2]))
			}
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case isHan(r):
			flushWord()
			han = append(han, r)
		case isWordRune(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()
	return out
}

// QueryTokens 是查询侧切分：只保留二元组与整词，丢掉单字。
// 单字进查询会让「中文偏好」这类短语要求语料里存在独立成词的单字，反而降低召回。
// 若二元组与整词都切不出来（单字查询），退回 Segment 结果。
func QueryTokens(s string) []string {
	var out []string
	var word, han []rune

	flushWord := func() {
		if len(word) > 0 {
			out = append(out, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	flushHan := func() {
		for i := 0; i+1 < len(han); i++ {
			out = append(out, string(han[i:i+2]))
		}
		han = han[:0]
	}

	for _, r := range s {
		switch {
		case isHan(r):
			flushWord()
			han = append(han, r)
		case isWordRune(r):
			flushHan()
			word = append(word, r)
		default:
			flushWord()
			flushHan()
		}
	}
	flushWord()
	flushHan()

	if len(out) == 0 {
		return Segment(s)
	}
	return out
}

// MatchExpr 把 token 拼成 FTS5 匹配表达式。每个 token 加双引号并把内部
// 双引号翻倍，避免用户记忆里的符号被当成 FTS 语法。
func MatchExpr(tokens ...string) string {
	parts := make([]string, 0, len(tokens))
	for _, t := range tokens {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		parts = append(parts, `"`+strings.ReplaceAll(t, `"`, `""`)+`"`)
	}
	return strings.Join(parts, " ")
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/normalize/ -v`
Expected: PASS（7 个测试）

- [ ] **Step 5: 提交**

```bash
git add internal/normalize/segment.go internal/normalize/segment_test.go
git commit -m "feat: 中文二元组切分与 FTS 匹配表达式构造"
```

---

## Task 4: 去重用的文本归一化与哈希

**Files:**
- Create: `internal/normalize/memory.go`
- Test: `internal/normalize/memory_test.go`

- [ ] **Step 1: 写失败测试 `internal/normalize/memory_test.go`**

```go
package normalize

import (
	"strings"
	"testing"
)

func TestCanonicalText_StripsBulletMarker(t *testing.T) {
	for _, b := range []string{"-", "*", "+"} {
		with := CanonicalText(b + " Git 提交信息必须使用中文")
		plain := CanonicalText("Git 提交信息必须使用中文")
		if with != plain {
			t.Fatalf("bullet %q not stripped: %q != %q", b, with, plain)
		}
	}
}

func TestCanonicalText_CollapsesWhitespaceAcrossLines(t *testing.T) {
	a := CanonicalText("不在  foreach\n\t 内做数据查询")
	b := CanonicalText("不在 foreach 内做数据查询")
	if a != b {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestCanonicalText_NFKCMergesFullWidthForms(t *testing.T) {
	if CanonicalText("ＡＢＣ") != CanonicalText("abc") {
		t.Fatal("full-width ASCII should canonicalize equal")
	}
}

func TestCanonicalText_KeepsMinusInsideIdentifier(t *testing.T) {
	got := CanonicalText("nuonuo-fresh3-api 项目")
	if !strings.Contains(got, "nuonuo-fresh3-api") {
		t.Fatalf("identifier damaged: %q", got)
	}
}

func TestHash_StableHex64(t *testing.T) {
	h := Hash("abc")
	if len(h) != 64 || h != Hash("abc") {
		t.Fatalf("bad hash %q", h)
	}
	if h == Hash("abd") {
		t.Fatal("collision on trivial input")
	}
}

func TestHashBytes_MatchesHashOfContent(t *testing.T) {
	if HashBytes([]byte("abc")) != Hash("abc") {
		t.Fatal("HashBytes and Hash must agree")
	}
}

// 归一化必须是不动点：同一文本归一化两次得到同一结果。否则去重哈希会随
// 调用次数漂移，同一条记忆在两次扫描里被当成两条。
func TestCanonicalText_IsIdempotentAcrossUnicodeWhitespace(t *testing.T) {
	inputs := []string{
		"\u2028- x",
		"\u2029* y",
		"\u0085+ z",
		"\u00a0- 全角空格 bullet",
		"- - 连续 bullet",
		"普通文本",
	}
	for _, in := range inputs {
		once := CanonicalText(in)
		if twice := CanonicalText(once); once != twice {
			t.Fatalf("not idempotent for %q: first=%q second=%q", in, once, twice)
		}
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/normalize/ -run 'TestCanonical|TestHash' -v`
Expected: FAIL，`undefined: CanonicalText`

- [ ] **Step 3: 写 `internal/normalize/memory.go`**

```go
package normalize

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"

	"golang.org/x/text/unicode/norm"
)

var (
	bulletRE = regexp.MustCompile(`^\s*[-*+]\s+`)
	spaceRE  = regexp.MustCompile(`\s+`)
)

// CanonicalText 产出用于去重比较的文本：NFKC → 去代码围栏 → 反复去 bullet 前缀
// → 折叠空白 → 小写。归一化结果同时是 FTS 的索引文本，所以两者必须同源。
//
// 每轮先 TrimSpace 再剥 bullet：Go 正则的 \s 只认 ASCII 空白，而 TrimSpace 认
// Unicode 空白。行首若是 U+2028/2029/0085 这类空白，不先 trim 就会漏掉 bullet，
// 使本函数不是不动点（同一文本两次归一化得到不同哈希）。
func CanonicalText(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ReplaceAll(s, "```", " ")
	for {
		s = strings.TrimSpace(s)
		next := bulletRE.ReplaceAllString(s, "")
		if next == s {
			break
		}
		s = next
	}
	s = spaceRE.ReplaceAllString(strings.TrimSpace(s), " ")
	return strings.ToLower(s)
}

func Hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
```

- [ ] **Step 4: 加依赖并跑测试**

Run: `go get golang.org/x/text@v0.16.0 && go test ./internal/normalize/ -v`
Expected: PASS（含 Task 3 的测试）

- [ ] **Step 5: 提交**

```bash
git add internal/normalize go.mod go.sum
git commit -m "feat: 记忆文本归一化与内容哈希"
```

---

## Task 5: 项目身份归一化与 git remote 读取

**Files:**
- Create: `internal/normalize/project.go`
- Test: `internal/normalize/project_test.go`

- [ ] **Step 1: 写失败测试 `internal/normalize/project_test.go`**

```go
package normalize

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectKey_PrefersGitRemote(t *testing.T) {
	for _, remote := range []string{
		"https://github.com/Foo/Bar.git",
		"git@github.com:Foo/Bar.git",
		"ssh://git@github.com/Foo/Bar.git",
	} {
		got := ProjectKey(remote, `D:\somewhere\else\bar`)
		if got != "github.com/foo/bar" {
			t.Fatalf("remote %q -> %q, want github.com/foo/bar", remote, got)
		}
	}
}

func TestProjectKey_FallsBackToAbsolutePath(t *testing.T) {
	got := ProjectKey("", filepath.FromSlash("F:/go/Agent-Memory/"))
	want := "f:/go/agent-memory"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestProjectKey_BothEmpty(t *testing.T) {
	if got := ProjectKey("", ""); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestProjectKey_CollapsesDuplicateSeparators(t *testing.T) {
	a := ProjectKey("", `C:\a\\b`)
	b := ProjectKey("", `c:/a/b`)
	if a != b || a != "c:/a/b" {
		t.Fatalf("a=%q b=%q", a, b)
	}
}

func TestGitRemoteFromConfig(t *testing.T) {
	dir := t.TempDir()
	gitDir := filepath.Join(dir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[core]\n\trepositoryformatversion = 0\n[remote \"origin\"]\n\turl = git@github.com:Foo/Bar.git\n\tfetch = +refs/heads/*:refs/remotes/origin/*\n[remote \"upstream\"]\n\turl = https://github.com/up/repo.git\n"
	if err := os.WriteFile(filepath.Join(gitDir, "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok, err := GitRemoteFromConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got != "git@github.com:Foo/Bar.git" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestGitRemoteFromConfig_NoRepo(t *testing.T) {
	_, ok, err := GitRemoteFromConfig(t.TempDir())
	if err != nil {
		t.Fatalf("missing repo is not an error: %v", err)
	}
	if ok {
		t.Fatal("want ok=false when no .git/config")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/normalize/ -run 'TestProjectKey|TestGitRemote' -v`
Expected: FAIL，`undefined: ProjectKey`

- [ ] **Step 3: 写 `internal/normalize/project.go`**

```go
package normalize

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var (
	multiSlashRE = regexp.MustCompile(`/+`)
	scpRE        = regexp.MustCompile(`^[^/@]+@([^:]+):(.+)$`)
)

// ProjectKey 生成跨 agent 稳定的项目标识：git remote 优先，其次规范化绝对路径。
// 同一个仓库在两个 agent 里路径不同时，只有 remote 能把它们合并成一个项目。
func ProjectKey(gitRemote, absPath string) string {
	if k := keyFromRemote(gitRemote); k != "" {
		return k
	}
	return keyFromPath(absPath)
}

func keyFromRemote(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	s = strings.TrimPrefix(s, "ssh://")
	s = strings.TrimPrefix(s, "https://")
	s = strings.TrimPrefix(s, "http://")
	s = strings.TrimPrefix(s, "git://")
	if m := scpRE.FindStringSubmatch(s); m != nil {
		s = m[1] + "/" + m[2]
	}
	s = strings.TrimSuffix(s, ".git")
	s = strings.ReplaceAll(filepath.ToSlash(s), "\\", "/")
	s = multiSlashRE.ReplaceAllString(s, "/")
	s = strings.Trim(s, "/")
	return strings.ToLower(s)
}

func keyFromPath(absPath string) string {
	s := strings.TrimSpace(absPath)
	if s == "" {
		return ""
	}
	s = filepath.Clean(s)
	s = filepath.ToSlash(s)
	s = multiSlashRE.ReplaceAllString(s, "/")
	s = strings.TrimSuffix(s, "/")
	return strings.ToLower(s)
}

// GitRemoteFromConfig 直接读 <dir>/.git/config 取 origin 的 url，不起子进程。
// 返回值第二参数表示是否存在；缺目录、缺 remote 都不算错误。
func GitRemoteFromConfig(dir string) (remote string, ok bool, err error) {
	data, err := os.ReadFile(filepath.Join(dir, ".git", "config"))
	if os.IsNotExist(err) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	lines := strings.Split(string(data), "\n")
	inOrigin := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "["):
			inOrigin = trimmed == `[remote "origin"]`
		case inOrigin && strings.HasPrefix(trimmed, "url"):
			parts := strings.SplitN(trimmed, "=", 2)
			if len(parts) != 2 {
				continue
			}
			v := strings.Trim(strings.TrimSpace(parts[1]), `"`)
			if v == "" {
				continue
			}
			return v, true, nil
		}
	}
	return "", false, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/normalize/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/normalize/project.go internal/normalize/project_test.go
git commit -m "feat: 项目标识归一化与 git remote 读取"
```

---

## Task 6: 数据库打开与建表

schema 一次建全（含 M2/M3 的表），避免后续再加迁移；本期只写入 `project`、`source`、`memory_item`、`memory_source`、`memory_fts`。

**Files:**
- Create: `internal/store/schema.go`
- Create: `internal/store/store.go`
- Test: `internal/store/store_test.go`

- [ ] **Step 1: 写 `internal/store/schema.go`**

```go
package store

const schemaSQL = `
CREATE TABLE IF NOT EXISTS meta (
  key   TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS project (
  id           INTEGER PRIMARY KEY,
  key          TEXT NOT NULL UNIQUE,
  display_name TEXT NOT NULL DEFAULT '',
  git_remote   TEXT NOT NULL DEFAULT '',
  primary_path TEXT NOT NULL DEFAULT '',
  created_at   INTEGER NOT NULL,
  updated_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS project_path (
  id         INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  agent      TEXT NOT NULL,
  slug       TEXT NOT NULL DEFAULT '',
  path       TEXT NOT NULL,
  confidence TEXT NOT NULL DEFAULT 'high',
  UNIQUE(project_id, agent, path)
);

CREATE TABLE IF NOT EXISTS source (
  id            INTEGER PRIMARY KEY,
  agent         TEXT NOT NULL,
  kind          TEXT NOT NULL,
  path          TEXT NOT NULL,
  content_hash  TEXT NOT NULL,
  prev_hash     TEXT NOT NULL DEFAULT '',
  changed_at    INTEGER NOT NULL DEFAULT 0,
  mtime         INTEGER NOT NULL DEFAULT 0,
  size          INTEGER NOT NULL DEFAULT 0,
  last_error    TEXT NOT NULL DEFAULT '',
  last_seen_run INTEGER NOT NULL DEFAULT 0,
  updated_at    INTEGER NOT NULL,
  UNIQUE(agent, kind, path)
);

CREATE TABLE IF NOT EXISTS memory_item (
  id             INTEGER PRIMARY KEY,
  scope          TEXT NOT NULL,
  project_id     INTEGER NOT NULL DEFAULT 0,
  type           TEXT NOT NULL,
  text           TEXT NOT NULL,
  text_norm_hash TEXT NOT NULL,
  stale          INTEGER NOT NULL DEFAULT 0,
  first_seen_at  INTEGER NOT NULL,
  last_seen_at   INTEGER NOT NULL,
  embedding      BLOB,
  embed_model    TEXT NOT NULL DEFAULT '',
  embed_dim      INTEGER NOT NULL DEFAULT 0,
  embedded_at    INTEGER NOT NULL DEFAULT 0,
  UNIQUE(scope, project_id, type, text_norm_hash)
);

CREATE TABLE IF NOT EXISTS memory_source (
  id        INTEGER PRIMARY KEY,
  memory_id INTEGER NOT NULL REFERENCES memory_item(id) ON DELETE CASCADE,
  agent     TEXT NOT NULL,
  path      TEXT NOT NULL,
  quote     TEXT NOT NULL DEFAULT '',
  UNIQUE(memory_id, agent, path)
);

CREATE TABLE IF NOT EXISTS session (
  id          INTEGER PRIMARY KEY,
  agent       TEXT NOT NULL,
  external_id TEXT NOT NULL,
  project_id  INTEGER NOT NULL DEFAULT 0,
  title       TEXT NOT NULL DEFAULT '',
  started_at  INTEGER NOT NULL DEFAULT 0,
  updated_at  INTEGER NOT NULL DEFAULT 0,
  event_count INTEGER NOT NULL DEFAULT 0,
  path        TEXT NOT NULL DEFAULT '',
  UNIQUE(agent, external_id)
);

CREATE TABLE IF NOT EXISTS session_event (
  id         INTEGER PRIMARY KEY,
  session_id INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  seq        INTEGER NOT NULL,
  role       TEXT NOT NULL,
  kind       TEXT NOT NULL,
  text       TEXT NOT NULL,
  tool       TEXT NOT NULL DEFAULT '',
  files_json TEXT NOT NULL DEFAULT '[]',
  ts         INTEGER NOT NULL DEFAULT 0,
  UNIQUE(session_id, seq)
);

CREATE TABLE IF NOT EXISTS task_card (
  id             INTEGER PRIMARY KEY,
  session_id     INTEGER NOT NULL REFERENCES session(id) ON DELETE CASCADE,
  project_id     INTEGER NOT NULL DEFAULT 0,
  goal           TEXT NOT NULL DEFAULT '',
  status         TEXT NOT NULL DEFAULT 'unknown',
  decisions_json TEXT NOT NULL DEFAULT '[]',
  files_json     TEXT NOT NULL DEFAULT '[]',
  commands_json  TEXT NOT NULL DEFAULT '[]',
  errors_json    TEXT NOT NULL DEFAULT '[]',
  open_questions TEXT NOT NULL DEFAULT '',
  next_steps     TEXT NOT NULL DEFAULT '',
  confidence     TEXT NOT NULL DEFAULT 'rule',
  generated_by   TEXT NOT NULL DEFAULT 'rule',
  created_at     INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL,
  UNIQUE(session_id)
);

CREATE TABLE IF NOT EXISTS handoff (
  id            INTEGER PRIMARY KEY,
  project_id    INTEGER NOT NULL REFERENCES project(id) ON DELETE CASCADE,
  body          TEXT NOT NULL,
  content_hash  TEXT NOT NULL,
  budget_tokens INTEGER NOT NULL DEFAULT 0,
  generated_at  INTEGER NOT NULL,
  UNIQUE(project_id)
);

CREATE TABLE IF NOT EXISTS agent_link (
  id             INTEGER PRIMARY KEY,
  project_id     INTEGER NOT NULL DEFAULT 0,
  agent          TEXT NOT NULL,
  block_file     TEXT NOT NULL,
  installed_hash TEXT NOT NULL,
  installed_at   INTEGER NOT NULL,
  UNIQUE(project_id, agent)
);

CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
  text_seg,
  tokenize = 'unicode61'
);

CREATE INDEX IF NOT EXISTS idx_memory_source_memory ON memory_source(memory_id);
CREATE INDEX IF NOT EXISTS idx_memory_source_agent ON memory_source(agent);
CREATE INDEX IF NOT EXISTS idx_memory_item_project ON memory_item(project_id, stale);
CREATE INDEX IF NOT EXISTS idx_session_project ON session(project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_task_card_project ON task_card(project_id, updated_at);
`

const schemaVersion = "1"
```

- [ ] **Step 2: 写失败测试 `internal/store/store_test.go`**

```go
package store

import (
	"path/filepath"
	"testing"
)

func openStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpen_CreatesAllSpecTables(t *testing.T) {
	s := openStore(t)
	want := []string{
		"meta", "project", "project_path", "source", "memory_item", "memory_source",
		"session", "session_event", "task_card", "handoff", "agent_link", "memory_fts",
	}
	got := map[string]bool{}
	rows, err := s.db.Query(`SELECT name FROM sqlite_master WHERE type IN ('table','view')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		got[n] = true
	}
	for _, name := range want {
		if !got[name] {
			t.Fatalf("table %q missing; found %v", name, keys(got))
		}
	}
}

func TestOpen_WALEnabled(t *testing.T) {
	s := openStore(t)
	var mode string
	if err := s.db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

func TestOpen_RecordsSchemaVersionAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	s1.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen should succeed: %v", err)
	}
	defer s2.Close()
	var v string
	if err := s2.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != schemaVersion {
		t.Fatalf("schema_version = %q, want %q", v, schemaVersion)
	}
}

func TestOpen_RejectsNewerSchemaVersion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.db")
	s1, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s1.db.Exec(`UPDATE meta SET value='99' WHERE key='schema_version'`); err != nil {
		t.Fatal(err)
	}
	s1.Close()

	if _, err := Open(path); err == nil {
		t.Fatal("want error when db schema_version is newer than binary")
	}
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/store/ -v`
Expected: FAIL，`undefined: Store` / `undefined: Open`

- [ ] **Step 4: 写 `internal/store/store.go`**

```go
package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

// ErrEmptyMemory 拒绝把空白内容写成记忆条目。
var ErrEmptyMemory = errors.New("memory text is empty")

// Store 持有唯一的 *sql.DB。全项目只有这个包读写数据库。
//
// 连接数固定为 1：SQLite 的写是串行的，单连接天然避免了 SQLITE_BUSY，
// 也让常驻服务里的扫描与 MCP 写入共用同一条队列。
type Store struct {
	db   *sql.DB
	path string
}

func Open(path string) (*Store, error) {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create data dir: %w", err)
		}
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	pragmas := []string{
		`PRAGMA busy_timeout = 5000`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA foreign_keys = ON`,
		`PRAGMA synchronous = NORMAL`,
	}
	for _, p := range pragmas {
		if _, err := db.Exec(p); err != nil {
			db.Close()
			return nil, fmt.Errorf("%s: %w", p, err)
		}
	}

	if _, err := db.Exec(schemaSQL); err != nil {
		db.Close()
		return nil, fmt.Errorf("create schema: %w", err)
	}

	ver, ok, err := metaValue(db, "schema_version")
	if err != nil {
		db.Close()
		return nil, err
	}
	if ok && ver != schemaVersion {
		db.Close()
		return nil, fmt.Errorf("database schema version %q is newer than this binary (%s); rebuild with `agent-memory scan --full`", ver, schemaVersion)
	}
	if !ok {
		if err := setMeta(db, "schema_version", schemaVersion); err != nil {
			db.Close()
			return nil, err
		}
	}

	return &Store{db: db, path: path}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Path() string { return s.path }

func metaValue(db *sql.DB, key string) (string, bool, error) {
	var v string
	err := db.QueryRow(`SELECT value FROM meta WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

func setMeta(db *sql.DB, key, value string) error {
	_, err := db.Exec(`INSERT INTO meta(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}
```

- [ ] **Step 5: 加依赖并跑测试**

Run: `go get modernc.org/sqlite@v1.34.4 && go test ./internal/store/ -v`
Expected: PASS（5 个测试）

- [ ] **Step 6: 提交**

```bash
git add internal/store/schema.go internal/store/store.go internal/store/store_test.go go.mod go.sum
git commit -m "feat: SQLite 存储底座与全量建表"
```

---

## Task 7: 项目仓储与路径映射

`project_path.path` 存的是 `normalize.ProjectKey("", rawPath)` 的结果（小写、正斜杠、去尾分隔符），查表时先归一化再比；`project.primary_path` 存可展示的原始路径。这样同一个仓库在 Qoder 与 WorkBuddy 里的两种写法能命中同一行。

**Files:**
- Create: `internal/store/project.go`
- Test: `internal/store/project_test.go`

- [ ] **Step 1: 写失败测试 `internal/store/project_test.go`**

```go
package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/normalize"
)

func TestEnsureProject_IdempotentKeepsStableID(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id1, err := s.EnsureProject(ctx, "github.com/foo/bar", "bar", "git@github.com:Foo/Bar.git", `D:\x\bar`, now)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.EnsureProject(ctx, "github.com/foo/bar", "bar", "", `E:\other\bar`, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if id1 != id2 {
		t.Fatalf("ids differ: %d vs %d", id1, id2)
	}

	p, ok, err := s.ProjectByKey(ctx, "github.com/foo/bar")
	if err != nil || !ok {
		t.Fatalf("ProjectByKey ok=%v err=%v", ok, err)
	}
	if p.GitRemote == "" {
		t.Fatal("first write should keep git_remote")
	}
	if p.PrimaryPath != `D:\x\bar` {
		t.Fatalf("primary_path overwritten: %q", p.PrimaryPath)
	}
	if p.UpdatedAt.Equal(now) {
		t.Fatal("updated_at should advance on second write")
	}
}

func TestEnsureProject_FillsEmptyFieldsOnLaterWrite(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id, err := s.EnsureProject(ctx, "f:/go/agent-memory", "", "", `F:\go\agent-memory`, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureProject(ctx, "f:/go/agent-memory", "agent-memory", "git@github.com:muyi99/agent-memory.git", "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	p, _, err := s.ProjectByKey(ctx, "f:/go/agent-memory")
	if err != nil {
		t.Fatal(err)
	}
	if p.DisplayName != "agent-memory" || p.GitRemote == "" {
		t.Fatalf("empty fields should be filled later: %+v", p)
	}
	if p.ID != id {
		t.Fatalf("id changed: %d vs %d", p.ID, id)
	}
}

func TestLinkProjectPathAndResolveByPath(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id, err := s.EnsureProject(ctx, "f:/go/hiprint", "hiprint", "", `F:\go-hiprint`, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, id, "qoder", "F--go-hiprint", `F:\go-hiprint`, "high"); err != nil {
		t.Fatal(err)
	}
	// 第二次链接同一项目、另一 agent 的写法：路径归一化后必须解析到同一 id
	if err := s.LinkProjectPath(ctx, id, "workbuddy", "", `f:/go-hiprint/`, "high"); err != nil {
		t.Fatal(err)
	}

	got, ok, err := s.ResolveProjectByPath(ctx, `F:\go-hiprint`)
	if err != nil || !ok {
		t.Fatalf("resolve ok=%v err=%v", ok, err)
	}
	if got != id {
		t.Fatalf("got project %d want %d", got, id)
	}

	missing, ok, err := s.ResolveProjectByPath(ctx, `Z:\nowhere`)
	if err != nil {
		t.Fatal(err)
	}
	if ok || missing != 0 {
		t.Fatalf("want (0,false), got (%d,%v)", missing, ok)
	}
}

func TestLinkProjectPath_DeduplicatesSameAgentPath(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	id, _ := s.EnsureProject(ctx, "e:/go/deploy", "", "", `E:\go-deploy`, now)

	key := normalize.ProjectKey("", filepath.FromSlash("E:/go-deploy"))
	if err := s.LinkProjectPath(ctx, id, "qoder", "E--go-deploy", `E:\go-deploy`, "high"); err != nil {
		t.Fatal(err)
	}
	if err := s.LinkProjectPath(ctx, id, "qoder", "E--go-deploy", key, "low"); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT count(*) FROM project_path`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("want 1 row after normalized dedupe, got %d", n)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run 'TestEnsureProject|TestLinkProject|TestProjectBy' -v`
Expected: FAIL，`s.EnsureProject undefined`

- [ ] **Step 3: 写 `internal/store/project.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

// EnsureProject 按 key 建或取项目。已存在时只补空字段并推进 updated_at，
// 不覆盖已有 display_name / git_remote / primary_path，避免后来者以低质量信息盖掉高质量信息。
func (s *Store) EnsureProject(ctx context.Context, key, displayName, gitRemote, primaryPath string, at time.Time) (int64, error) {
	if key == "" {
		return 0, errors.New("project key is empty")
	}
	now := at.Unix()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project(key, display_name, git_remote, primary_path, created_at, updated_at)
VALUES(?, ?, ?, ?, ?, ?)
ON CONFLICT(key) DO UPDATE SET
  updated_at   = excluded.updated_at,
  display_name = CASE WHEN project.display_name = '' THEN excluded.display_name ELSE project.display_name END,
  git_remote   = CASE WHEN project.git_remote   = '' THEN excluded.git_remote  ELSE project.git_remote  END,
  primary_path = CASE WHEN project.primary_path = '' THEN excluded.primary_path ELSE project.primary_path END`,
		key, displayName, gitRemote, primaryPath, now, now)
	if err != nil {
		return 0, fmt.Errorf("ensure project %q: %w", key, err)
	}
	var id int64
	if err := s.db.QueryRowContext(ctx, `SELECT id FROM project WHERE key = ?`, key).Scan(&id); err != nil {
		return 0, fmt.Errorf("lookup project %q: %w", key, err)
	}
	return id, nil
}

func (s *Store) ProjectByKey(ctx context.Context, key string) (domain.Project, bool, error) {
	var p domain.Project
	var created, updated int64
	err := s.db.QueryRowContext(ctx, `
SELECT id, key, display_name, git_remote, primary_path, created_at, updated_at
FROM project WHERE key = ?`, key).Scan(
		&p.ID, &p.Key, &p.DisplayName, &p.GitRemote, &p.PrimaryPath, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Project{}, false, nil
	}
	if err != nil {
		return domain.Project{}, false, err
	}
	p.CreatedAt = time.Unix(created, 0)
	p.UpdatedAt = time.Unix(updated, 0)
	return p, true, nil
}

// LinkProjectPath 登记「某 agent 在某处把这个项目写成什么路径」。path 先归一化再存，
// 因为各 agent 对同一路径的写法不同（反斜杠、大小写、尾部分隔符）。
func (s *Store) LinkProjectPath(ctx context.Context, projectID int64, agent, slug, path, confidence string) error {
	key := normalize.ProjectKey("", path)
	if key == "" {
		return errors.New("project path is empty")
	}
	if confidence == "" {
		confidence = "high"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO project_path(project_id, agent, slug, path, confidence)
VALUES(?, ?, ?, ?, ?)
ON CONFLICT(project_id, agent, path) DO UPDATE SET
  slug = CASE WHEN excluded.slug <> '' THEN excluded.slug ELSE project_path.slug END,
  confidence = CASE WHEN excluded.confidence = 'high' THEN 'high' ELSE project_path.confidence END`,
		projectID, agent, slug, key, confidence)
	return err
}

func (s *Store) ResolveProjectByPath(ctx context.Context, path string) (int64, bool, error) {
	key := normalize.ProjectKey("", path)
	if key == "" {
		return 0, false, nil
	}
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT project_id FROM project_path WHERE path = ? LIMIT 1`, key).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return id, true, nil
}

// ProjectIDForPath 解析或创建项目：git remote 决定 key，让同一仓库的不同路径写法合并。
// 读不到 remote 时退化为路径 key。primaryPath 存原始写法便于展示。
func (s *Store) ProjectIDForPath(ctx context.Context, agent, slug, rawPath, gitRemote string, at time.Time) (int64, string, error) {
	key := normalize.ProjectKey(gitRemote, rawPath)
	if key == "" {
		return 0, "", fmt.Errorf("cannot derive project key from %q", rawPath)
	}
	id, err := s.EnsureProject(ctx, key, baseName(rawPath), gitRemote, rawPath, at)
	if err != nil {
		return 0, key, err
	}
	if err := s.LinkProjectPath(ctx, id, agent, slug, rawPath, "high"); err != nil {
		return 0, key, err
	}
	return id, key, nil
}

func baseName(p string) string {
	p = normalize.ProjectKey("", p)
	if i := lastSlash(p); i >= 0 {
		return p[i+1:]
	}
	return p
}

func lastSlash(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '/' {
			return i
		}
	}
	return -1
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

注意时间列的写法：`project.created_at` 在库里是 INTEGER，`domain.Project.CreatedAt` 是 `time.Time`，必须扫进 `int64` 局部变量再 `time.Unix` 转换——直接 `Scan` 到 `time.Time` 字段会报类型不匹配。后面所有表都按这个规矩写。

- [ ] **Step 5: 跑全部 store 测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/store/project.go internal/store/project_test.go
git commit -m "feat: 项目身份与跨 agent 路径映射仓储"
```

---

## Task 8: 记忆条目 upsert、去重与 stale

去重键是 `(scope, project_id, type, text_norm_hash)`；FTS 行与条目同 id（`memory_fts.rowid = memory_item.id`），所以更新文本时必须先删后插——本期文本不可变，只处理插入与删除。

**Files:**
- Create: `internal/store/memory.go`
- Test: `internal/store/memory_test.go`

- [ ] **Step 1: 写失败测试 `internal/store/memory_test.go`**

```go
package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func draft(agent, path, text string) domain.MemoryDraft {
	return domain.MemoryDraft{
		Scope:       domain.ScopeUser,
		Type:        domain.TypePreference,
		Text:        text,
		SourceAgent: agent,
		SourcePath:  path,
		Quote:       text,
	}
}

func TestUpsertMemory_DedupesAcrossAgentsAndKeepsBothSources(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	id1, created1, err := s.UpsertMemory(ctx, 0, draft("qoder", `/home/.qoder-cn/memory/a.md`, "Git 提交信息必须使用中文"), now)
	if err != nil || !created1 {
		t.Fatalf("first upsert id=%d created=%v err=%v", id1, created1, err)
	}

	// Trae 用了 bullet 前缀和不同空白，内容等价：应当合并成同一条
	id2, created2, err := s.UpsertMemory(ctx, 0, draft("trae", `/home/.trae-cn/memory/user_profile.md`, "-  Git 提交信息必须使用中文"), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if created2 {
		t.Fatalf("equivalent text must not create a second item")
	}
	if id1 != id2 {
		t.Fatalf("ids differ %d vs %d", id1, id2)
	}

	item, err := s.MemoryByID(ctx, id1)
	if err != nil {
		t.Fatal(err)
	}
	if len(item.Sources) != 2 {
		t.Fatalf("want 2 sources, got %d: %+v", len(item.Sources), item.Sources)
	}
	if !item.LastSeenAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("last_seen_at not advanced: %v", item.LastSeenAt)
	}
	if item.FirstSeenAt.Equal(item.LastSeenAt) {
		t.Fatal("first_seen_at should stay at the original write")
	}
}

func TestUpsertMemory_TypeAndProjectParticipateInDedupe(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	text := "批量匹配时优先按索引关联 AI 返回结果"
	a := draft("qoder", `/a.md`, text)
	b := draft("trae", `/b.md`, text)
	b.Type = domain.TypeLesson
	if _, _, err := s.UpsertMemory(ctx, 0, a, now); err != nil {
		t.Fatal(err)
	}
	_, created, err := s.UpsertMemory(ctx, 0, b, now)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("different type should not dedupe into the same item")
	}

	c := draft("workbuddy", `/c.md`, text)
	c.Type = domain.TypeLesson
	if _, created, err := s.UpsertMemory(ctx, 42, c, now); err != nil {
		t.Fatal(err)
	} else if !created {
		t.Fatal("different project should not dedupe")
	}
}

func TestUpsertMemory_RejectsBlankText(t *testing.T) {
	s := openStore(t)
	_, _, err := s.UpsertMemory(context.Background(), 0, draft("qoder", `/a.md`, "   \n\t "), time.Unix(1, 0))
	if !errors.Is(err, domain.ErrEmptyMemory) {
		t.Fatalf("want ErrEmptyMemory, got %v", err)
	}
}

func TestUpsertMemory_WritesSegmentedFTSRow(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	id, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/a.md`, "用户偏好 snake_case 命名"), time.Unix(1700000000, 0))
	if err != nil {
		t.Fatal(err)
	}
	var seg string
	if err := s.db.QueryRow(`SELECT text_seg FROM memory_fts WHERE rowid = ?`, id).Scan(&seg); err != nil {
		t.Fatal(err)
	}
	if seg == "" {
		t.Fatal("fts row missing segmented text")
	}
}

func TestMarkStaleExcept_OnlyStalesItemsWhoseAgentSourcesDisappeared(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)

	// qoder 的一条记忆，来自两个源文件
	kept, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/a.md`, "保持存在的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "只在消失文件里出现的条目"), run1); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, run1.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/gone.md", ContentHash: "h2"}, run1.Unix()); err != nil {
		t.Fatal(err)
	}
	// 另一 agent 单独支撑一条记忆，不应被 qoder 的扫描波及
	other, _, err := s.UpsertMemory(ctx, 0, draft("trae", `/t/x.md`, "属于 trae 的条目"), run1)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/x.md", ContentHash: "h3"}, run1.Unix()); err != nil {
		t.Fatal(err)
	}

	// 第二次扫描：a.md 仍在，gone.md 不再出现
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, run2.Unix()); err != nil {
		t.Fatal(err)
	}
	staled, err := s.MarkStaleExcept(ctx, "qoder", run2.Unix())
	if err != nil {
		t.Fatal(err)
	}
	if staled != 1 {
		t.Fatalf("want 1 staled, got %d", staled)
	}

	assertStale := func(id int64, want bool) {
		t.Helper()
		item, err := s.MemoryByID(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if item.Stale != want {
			t.Fatalf("memory %d stale=%v want %v (%s)", id, item.Stale, want, item.Text)
		}
	}
	assertStale(kept, false)
	assertStale(other, false)

	gone, err := s.db.Query(`SELECT id FROM memory_item WHERE text = '只在消失文件里出现的条目'`)
	if err != nil {
		t.Fatal(err)
	}
	var goneID int64
	for gone.Next() {
		gone.Scan(&goneID)
	}
	gone.Close()
	assertStale(goneID, true)

	// 重新出现应撤销 stale
	if _, created, err := s.UpsertMemory(ctx, 0, draft("qoder", `/q/gone.md`, "只在消失文件里出现的条目"), run2); err != nil || created {
		t.Fatalf("reappear should merge, created=%v err=%v", created, err)
	}
	assertStale(goneID, false)
}

func TestPurgeStale_RemovesFTSRowAndSources(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	kept, _, _ := s.UpsertMemory(ctx, 0, draft("qoder", `/a.md`, "长期有效"), now)
	doomed, _, _ := s.UpsertMemory(ctx, 0, draft("qoder", `/b.md`, "已过时"), now)
	if _, err := s.db.Exec(`UPDATE memory_item SET stale = 1 WHERE id = ?`, doomed); err != nil {
		t.Fatal(err)
	}

	n, err := s.PurgeStale(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("purged %d, want 1", n)
	}
	if _, err := s.MemoryByID(ctx, doomed); !errors.Is(err, domain.ErrMemoryNotFound) {
		t.Fatalf("want ErrMemoryNotFound, got %v", err)
	}
	var fts, src int
	s.db.QueryRow(`SELECT count(*) FROM memory_fts WHERE rowid = ?`, doomed).Scan(&fts)
	s.db.QueryRow(`SELECT count(*) FROM memory_source WHERE memory_id = ?`, doomed).Scan(&src)
	if fts != 0 || src != 0 {
		t.Fatalf("leftover fts=%d sources=%d", fts, src)
	}
	if _, err := s.MemoryByID(ctx, kept); err != nil {
		t.Fatalf("non-stale memory removed: %v", err)
	}
}

func TestListMemories_ProjectFilterIncludesUserScope(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	userItem := draft("qoder", `/u.md`, "沟通语言用中文")
	if _, _, err := s.UpsertMemory(ctx, 0, userItem, now); err != nil {
		t.Fatal(err)
	}
	projItem := draft("qoder", `/p.md`, "该项目的扣减需乐观锁")
	projItem.Scope = domain.ScopeProject
	if _, _, err := s.UpsertMemory(ctx, 7, projItem, now); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListMemories(ctx, 0, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("project 0 listing should only see user scope, got %d", len(all))
	}
	mixed, err := s.ListMemories(ctx, 7, false, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(mixed) != 2 {
		t.Fatalf("want project items + user items = 2, got %d", len(mixed))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestUpsert -v`
Expected: FAIL，`s.UpsertMemory undefined`

- [ ] **Step 3: 在 `internal/domain/types.go` 追加两个错误变量**

```go
var (
	ErrMemoryNotFound = errors.New("memory not found")
	ErrEmptyMemory    = errors.New("memory text is empty")
)
```

并把 `import` 块改成：

```go
import (
	"context"
	"errors"
	"time"
)
```

同时删除 `internal/store/store.go` 里的 `ErrEmptyMemory` 定义（现在归 `domain`），保持单一出处。

- [ ] **Step 4: 写 `internal/store/memory.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

// UpsertMemory 写入或合并一条记忆。返回 (id, created, err)。
//
// 合并条件是全字段命中唯一索引 (scope, project_id, type, text_norm_hash)：
// 命中时不复制文本，只追加来源并把条目恢复为新鲜。projectID 为 0 表示用户级。
// 适配器只报原文，归一化与哈希都在这层做，保证不同 agent 的排版差异不影响去重。
func (s *Store) UpsertMemory(ctx context.Context, projectID int64, d domain.MemoryDraft, at time.Time) (int64, bool, error) {
	canon := normalize.CanonicalText(d.Text)
	if canon == "" {
		return 0, false, domain.ErrEmptyMemory
	}
	hash := normalize.Hash(canon)
	now := at.Unix()
	scope := string(d.Scope)
	if scope == "" {
		scope = string(domain.ScopeUser)
	}
	typ := string(d.Type)
	if typ == "" {
		typ = string(domain.TypeFact)
	}

	var id int64
	err := s.db.QueryRowContext(ctx, `
SELECT id FROM memory_item
WHERE scope = ? AND project_id = ? AND type = ? AND text_norm_hash = ?`,
		scope, projectID, typ, hash).Scan(&id)

	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := s.db.ExecContext(ctx, `
INSERT INTO memory_item(scope, project_id, type, text, text_norm_hash, stale, first_seen_at, last_seen_at)
VALUES(?, ?, ?, ?, ?, 0, ?, ?)`,
			scope, projectID, typ, canon, hash, now, now)
		if err != nil {
			return 0, false, fmt.Errorf("insert memory: %w", err)
		}
		id, err = res.LastInsertId()
		if err != nil {
			return 0, false, err
		}
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO memory_fts(rowid, text_seg) VALUES(?, ?)`,
			id, strings.Join(normalize.Segment(canon), " ")); err != nil {
			return 0, false, fmt.Errorf("index memory %d: %w", id, err)
		}
		if err := s.addSource(ctx, id, d); err != nil {
			return 0, false, err
		}
		return id, true, nil
	case err != nil:
		return 0, false, err
	default:
		if _, err := s.db.ExecContext(ctx,
			`UPDATE memory_item SET last_seen_at = ?, stale = 0 WHERE id = ?`, now, id); err != nil {
			return 0, false, err
		}
		if err := s.addSource(ctx, id, d); err != nil {
			return 0, false, err
		}
		return id, false, nil
	}
}

func (s *Store) addSource(ctx context.Context, memoryID int64, d domain.MemoryDraft) error {
	agent := d.SourceAgent
	if agent == "" {
		agent = "mcp"
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO memory_source(memory_id, agent, path, quote)
VALUES(?, ?, ?, ?)
ON CONFLICT(memory_id, agent, path) DO UPDATE SET quote = excluded.quote`,
		memoryID, agent, d.SourcePath, truncate(d.Quote, 400))
	if err != nil {
		return fmt.Errorf("add source for memory %d: %w", memoryID, err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func (s *Store) MemoryByID(ctx context.Context, id int64) (domain.MemoryItem, error) {
	var (
		m            domain.MemoryItem
		stale        int
		first, last  int64
		embeddedAt   int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id, scope, project_id, type, text, text_norm_hash, stale, first_seen_at, last_seen_at, embed_model, embed_dim, embedded_at
FROM memory_item WHERE id = ?`, id).Scan(
		&m.ID, &m.Scope, &m.ProjectID, &m.Type, &m.Text, &m.TextNormHash, &stale, &first, &last,
		&m.EmbedModel, &m.EmbedDim, &embeddedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.MemoryItem{}, domain.ErrMemoryNotFound
	}
	if err != nil {
		return domain.MemoryItem{}, err
	}
	m.Stale = stale != 0
	m.FirstSeenAt = time.Unix(first, 0)
	m.LastSeenAt = time.Unix(last, 0)
	if embeddedAt != 0 {
		m.EmbeddedAt = time.Unix(embeddedAt, 0)
	}
	sources, err := s.sourcesFor(ctx, id)
	if err != nil {
		return domain.MemoryItem{}, err
	}
	m.Sources = sources
	return m, nil
}

func (s *Store) sourcesFor(ctx context.Context, memoryID int64) ([]domain.MemorySource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT memory_id, agent, path, quote FROM memory_source WHERE memory_id = ? ORDER BY agent, path`, memoryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []domain.MemorySource
	for rows.Next() {
		var ms domain.MemorySource
		if err := rows.Scan(&ms.MemoryID, &ms.Agent, &ms.Path, &ms.Quote); err != nil {
			return nil, err
		}
		out = append(out, ms)
	}
	return out, rows.Err()
}

// ListMemories 按项目列条目。projectID 为 0 时只列用户级；大于 0 时同时带出用户级，
// 因为交接简报需要「全局偏好 + 本项目约束」两部分。
func (s *Store) ListMemories(ctx context.Context, projectID int64, includeStale bool, limit int) ([]domain.MemoryItem, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}
	q := `SELECT id FROM memory_item WHERE 1=1`
	args := []any{}
	if projectID == 0 {
		q += ` AND scope = ?`
		args = append(args, string(domain.ScopeUser))
	} else {
		q += ` AND (project_id = ? OR scope = ?)`
		args = append(args, projectID, string(domain.ScopeUser))
	}
	if !includeStale {
		q += ` AND stale = 0`
	}
	q += ` ORDER BY type, last_seen_at DESC, id LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]domain.MemoryItem, 0, len(ids))
	for _, id := range ids {
		m, err := s.MemoryByID(ctx, id)
		if errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// MarkStaleExcept 把该 agent 本轮没有再见到的记忆置为过时。
// 只在该 agent 支撑过的条目里判定：别的 agent 或 MCP 写入的条目不受影响。
// 不硬删——agent 重写记忆文件时常常只是换措辞，直接删会把结论抹掉。
func (s *Store) MarkStaleExcept(ctx context.Context, agent string, runAt int64) (int, error) {
	res, err := s.db.ExecContext(ctx, `
UPDATE memory_item SET stale = 1
WHERE stale = 0
  AND id IN (SELECT memory_id FROM memory_source WHERE agent = ?)
  AND id NOT IN (
    SELECT ms.memory_id
    FROM memory_source ms
    JOIN source s ON s.agent = ms.agent AND s.path = ms.path
    WHERE ms.agent = ? AND s.last_seen_run = ?
  )`, agent, agent, runAt)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}

// PurgeStale 真删所有过时条目，并清掉 FTS 行与来源行。
func (s *Store) PurgeStale(ctx context.Context) (int, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM memory_item WHERE stale = 1`)
	if err != nil {
		return 0, err
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	if len(ids) == 0 {
		return 0, nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_fts WHERE rowid = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_source WHERE memory_id = ?`, id); err != nil {
			return 0, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM memory_item WHERE id = ?`, id); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(ids), nil
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/store/memory.go internal/store/memory_test.go internal/domain/types.go internal/store/store.go
git commit -m "feat: 记忆条目 upsert 去重、stale 标记与清理"
```

---

## Task 9: FTS5 检索与 SearchProvider

**Files:**
- Create: `internal/store/search.go`
- Test: `internal/store/search_test.go`

- [ ] **Step 1: 写失败测试 `internal/store/search_test.go`**

```go
package store

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func seedForSearch(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Unix(1700000000, 0)
	items := []struct {
		text string
		typ  domain.MemoryType
	}{
		{"查询构造器变量不要命名为 $query，闭包外用 $list", domain.TypeConvention},
		{"Git commit messages 必须使用中文", domain.TypePreference},
		{"nuonuo-fresh3-api 的 matchGoodsSku 接口限制 similar_words 最多 10 个", domain.TypeConstraint},
		{"用户偏好 snake_case 命名", domain.TypePreference},
	}
	for i, it := range items {
		d := domain.MemoryDraft{
			Scope:       domain.ScopeUser,
			Type:        it.typ,
			Text:        it.text,
			SourceAgent: "qoder",
			SourcePath:  fmt.Sprintf("/q/item%d.md", i),
		}
		if _, _, err := s.UpsertMemory(ctx, 0, d, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}
}

func search(t *testing.T, s *Store, q string) []string {
	t.Helper()
	hits, err := (FTS{store: s}).Search(context.Background(), domain.SearchQuery{Text: q, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.Memory.Text)
	}
	return out
}

func TestFTS_SearchFindsTwoCharChineseTerm(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	got := search(t, s, "偏好")
	if len(got) == 0 || !strings.Contains(got[0], "用户偏好") {
		t.Fatalf("两字中文词应命中, got %v", got)
	}
}

func TestFTS_SearchFindsIdentifierTerm(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	got := search(t, s, "similar_words")
	if len(got) != 1 || !strings.Contains(got[0], "matchGoodsSku") {
		t.Fatalf("want the api constraint, got %v", got)
	}
}

func TestFTS_PhraseQueryNarrowsResults(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	got := search(t, s, "查询构造器")
	if len(got) != 1 || !strings.Contains(got[0], "$list") {
		t.Fatalf("want only the convention item, got %v", got)
	}
}

func TestFTS_TypeFilterAndStaleExclusion(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	seedForSearch(t, s)

	hits, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "命名", Type: domain.TypePreference, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if h.Memory.Type != domain.TypePreference {
			t.Fatalf("type filter leaked %q", h.Memory.Text)
		}
	}

	// 把偏好类条目全部标 stale，默认检索应看不到
	if _, err := s.db.Exec(`UPDATE memory_item SET stale = 1 WHERE type = 'preference'`); err != nil {
		t.Fatal(err)
	}
	hidden, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "命名", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hidden {
		if h.Memory.Type == domain.TypePreference {
			t.Fatalf("stale item leaked into default search: %q", h.Memory.Text)
		}
	}
	withStale, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "命名", IncludeStale: true, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(withStale) <= len(hidden) {
		t.Fatalf("include_stale should return more: %d vs %d", len(withStale), len(hidden))
	}
}

func TestFTS_EmptyAndPunctuationOnlyQueriesReturnNoError(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	for _, q := range []string{"", "   ", "，。", `"}:` } {
		hits, err := (FTS{store: s}).Search(context.Background(), domain.SearchQuery{Text: q, Limit: 5})
		if err != nil {
			t.Fatalf("query %q: %v", q, err)
		}
		if len(hits) != 0 {
			t.Fatalf("query %q should match nothing, got %v", q, hits)
		}
	}
}

func TestFTS_ResultCarriesSourcesAndOrigin(t *testing.T) {
	s := openStore(t)
	seedForSearch(t, s)
	hits, err := (FTS{store: s}).Search(context.Background(), domain.SearchQuery{Text: "中文", Limit: 5})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	h := hits[0]
	if h.Origin != "fts" {
		t.Fatalf("origin = %q", h.Origin)
	}
	if len(h.Memory.Sources) == 0 || h.Memory.Sources[0].Agent != "qoder" {
		t.Fatalf("sources missing: %+v", h.Memory.Sources)
	}
}

func TestFTS_ProjectSearchAlsoReturnsUserScope(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	global := domain.MemoryDraft{Scope: domain.ScopeUser, Type: domain.TypePreference, Text: "沟通用中文", SourceAgent: "qoder", SourcePath: "/u.md"}
	if _, _, err := s.UpsertMemory(ctx, 0, global, now); err != nil {
		t.Fatal(err)
	}
	local := domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "本项目的中文文档放在 docs 下", SourceAgent: "trae", SourcePath: "/p.md"}
	if _, _, err := s.UpsertMemory(ctx, 3, local, now); err != nil {
		t.Fatal(err)
	}
	other := domain.MemoryDraft{Scope: domain.ScopeProject, Type: domain.TypeFact, Text: "别的项目的中文说明", SourceAgent: "trae", SourcePath: "/o.md"}
	if _, _, err := s.UpsertMemory(ctx, 9, other, now); err != nil {
		t.Fatal(err)
	}

	hits, err := (FTS{store: s}).Search(ctx, domain.SearchQuery{Text: "中文", ProjectID: 3, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	var sawGlobal, sawLocal, sawOther bool
	for _, h := range hits {
		switch {
		case strings.Contains(h.Memory.Text, "沟通用中文"):
			sawGlobal = true
		case strings.Contains(h.Memory.Text, "docs"):
			sawLocal = true
		default:
			sawOther = true
		}
	}
	if !sawGlobal || !sawLocal {
		t.Fatalf("want global+local, got global=%v local=%v", sawGlobal, sawLocal)
	}
	if sawOther {
		t.Fatal("other project's item leaked")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestFTS -v`
Expected: FAIL，`undefined: FTS`

- [ ] **Step 3: 写 `internal/store/search.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/normalize"
)

const (
	defaultSearchLimit = 20
	maxSearchLimit     = 200
)

// FTS 是基于 FTS5 的检索实现，满足 domain.SearchProvider。
// 索引文本与查询文本必须同源：索引写 Segment(canon) 的结果，查询用 QueryTokens(canon)。
// 换成 trigram 或 unicode61 直搜原文，中文两字词会全部搜不到（见 spec 5.3 实测）。
type FTS struct {
	store *Store
}

// NewFTS 给 CLI 与 M3 的 daemon 构造检索器：字段非导出，包外只能用它。
func NewFTS(s *Store) domain.SearchProvider { return FTS{store: s} }

var _ domain.SearchProvider = FTS{}

func (f FTS) Search(ctx context.Context, q domain.SearchQuery) ([]domain.Hit, error) {
	tokens := normalize.QueryTokens(normalize.CanonicalText(q.Text))
	expr := normalize.MatchExpr(tokens...)
	if expr == "" {
		return nil, nil
	}

	limit := q.Limit
	if limit <= 0 {
		limit = defaultSearchLimit
	}
	if limit > maxSearchLimit {
		limit = maxSearchLimit
	}

	where := []string{"memory_fts MATCH ?"}
	args := []any{expr}
	if q.ProjectID > 0 {
		where = append(where, `(m.project_id = ? OR m.scope = ?)`)
		args = append(args, q.ProjectID, string(domain.ScopeUser))
	}
	if q.Type != "" {
		where = append(where, `m.type = ?`)
		args = append(args, string(q.Type))
	}
	if q.Scope != "" {
		where = append(where, `m.scope = ?`)
		args = append(args, string(q.Scope))
	}
	if !q.IncludeStale {
		where = append(where, `m.stale = 0`)
	}

	sqlText := `
SELECT m.id, bm25(memory_fts) AS score
FROM memory_fts
JOIN memory_item m ON m.id = memory_fts.rowid
WHERE ` + strings.Join(where, " AND ") + `
ORDER BY score ASC, m.last_seen_at DESC, m.id ASC
LIMIT ?`
	args = append(args, limit)

	rows, err := f.store.db.QueryContext(ctx, sqlText, args...)
	if err != nil {
		return nil, fmt.Errorf("fts search %q: %w", q.Text, err)
	}
	defer rows.Close()

	type pair struct {
		id    int64
		score float64
	}
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.score); err != nil {
			return nil, err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	hits := make([]domain.Hit, 0, len(pairs))
	for _, p := range pairs {
		memory, err := f.store.MemoryByID(ctx, p.id)
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		hits = append(hits, domain.Hit{Memory: memory, Score: p.score, Origin: "fts"})
	}
	return hits, nil
}

// RecentMemories 给交接简报取「最近见过」的条目，检索排序不参与。
func (s *Store) RecentMemories(ctx context.Context, projectID int64, typ domain.MemoryType, limit int) ([]domain.MemoryItem, error) {
	if limit <= 0 || limit > maxSearchLimit {
		limit = defaultSearchLimit
	}
	q := `SELECT id FROM memory_item WHERE stale = 0 AND (project_id = ? OR scope = ?)`
	args := []any{projectID, string(domain.ScopeUser)}
	if typ != "" {
		q += ` AND type = ?`
		args = append(args, string(typ))
	}
	q += ` ORDER BY last_seen_at DESC, id LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := make([]domain.MemoryItem, 0, len(ids))
	for _, id := range ids {
		m, err := s.MemoryByID(ctx, id)
		if errors.Is(err, domain.ErrMemoryNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/search.go internal/store/search_test.go
git commit -m "feat: FTS5 检索实现，中文两字词与标识符均可命中"
```

---

## Task 10: 扫描源游标、变更历史与统计

`source` 表承担三件事：幂等门控（`content_hash`）、过时判定依据（`last_seen_run`）、变更归属证据（`prev_hash` + `changed_at`）。第三个字段是验收标准 3 的基础——原生文件哈希变了只能证明「变过」，归属靠对照本程序的写入目标清单。

**Files:**
- Create: `internal/store/source.go`
- Test: `internal/store/source_test.go`

- [ ] **Step 1: 写失败测试 `internal/store/source_test.go`**

```go
package store

import (
	"context"
	"testing"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestPutSource_RecordsHashAndDetectsChange(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)
	st := func(hash string) domain.SourceState {
		return domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: hash, MTime: run1, Size: 10}
	}

	if err := s.PutSource(ctx, st("h1"), run1.Unix()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.SourceHash(ctx, "qoder", "memory", "/q/a.md")
	if err != nil || !ok || got != "h1" {
		t.Fatalf("got %q ok=%v err=%v", got, ok, err)
	}

	// 同一 run 重复写：不应记为变更
	if err := s.PutSource(ctx, st("h1"), run1.Unix()); err != nil {
		t.Fatal(err)
	}
	var changed int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM source WHERE changed_at <> 0`).Scan(&changed); err != nil {
		t.Fatal(err)
	}
	if changed != 0 {
		t.Fatalf("unchanged rescan should not record change, got %d", changed)
	}

	// 内容变化：prev_hash 记下旧哈希，changed_at 记下时间
	if err := s.PutSource(ctx, st("h2"), run2.Unix()); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSource(ctx, "qoder", "memory", "/q/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if row.ContentHash != "h2" || row.PrevHash != "h1" {
		t.Fatalf("hash history wrong: %+v", row)
	}
	if !row.ChangedAt.Equal(run2) {
		t.Fatalf("changed_at = %v want %v", row.ChangedAt, run2)
	}
	if row.LastError != "" {
		t.Fatalf("successful put should clear last_error, got %q", row.LastError)
	}
}

func TestMarkSourceError_KeepsPreviousHash(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run := time.Unix(1700000000, 0)
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/x.md", ContentHash: "old"}, run.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "trae", "memory", "/t/x.md", "parse: bad frontmatter", run.Add(time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSource(ctx, "trae", "memory", "/t/x.md")
	if err != nil {
		t.Fatal(err)
	}
	if row.LastError == "" || row.ContentHash != "old" {
		t.Fatalf("want error recorded with hash kept: %+v", row)
	}
	// 错误路径不该推进 last_seen_run，否则 stale 判定会误判为「本轮见过」
	if row.LastSeenRun == run.Add(time.Minute).Unix() {
		t.Fatal("MarkSourceError must not mark the source as seen this run")
	}
}

func TestStats_CountsAndGroupings(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	now := time.Unix(1700000000, 0)

	for _, d := range []domain.MemoryDraft{
		{Scope: domain.ScopeUser, Type: domain.TypePreference, Text: "沟通用中文", SourceAgent: "qoder", SourcePath: "/q/a.md"},
		{Scope: domain.ScopeUser, Type: domain.TypeConstraint, Text: "余额扣减需乐观锁", SourceAgent: "trae", SourcePath: "/t/a.md"},
	} {
		if _, _, err := s.UpsertMemory(ctx, 0, d, now); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "trae", Kind: "memory", Path: "/t/a.md", ContentHash: "h2"}, now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkSourceError(ctx, "trae", "memory", "/t/bad.md", "boom", now.Unix()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`UPDATE memory_item SET stale = 1 WHERE type = 'constraint'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(`INSERT INTO project(key, created_at, updated_at) VALUES('p1', 1, 1)`); err != nil {
		t.Fatal(err)
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Memories != 2 || stats.Stale != 1 || stats.Projects != 1 {
		t.Fatalf("counts wrong: %+v", stats)
	}
	if stats.Sources != 3 {
		t.Fatalf("sources = %d, want 3 (含一个错误源)", stats.Sources)
	}
	if stats.FailedSource != 1 {
		t.Fatalf("failed = %d, want 1", stats.FailedSource)
	}
	if stats.MemoriesByAgent["qoder"] != 1 || stats.MemoriesByAgent["trae"] != 1 {
		t.Fatalf("by agent wrong: %v", stats.MemoriesByAgent)
	}
	if stats.MemoriesByType["preference"] != 1 || stats.MemoriesByType["constraint"] != 1 {
		t.Fatalf("by type wrong: %v", stats.MemoriesByType)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run 'TestPutSource|TestMarkSourceError|TestStats' -v`
Expected: FAIL，`s.PutSource undefined`

- [ ] **Step 3: 写 `internal/store/source.go`**

```go
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// PutSource 记录一个源文件的当前状态。哈希变化时把旧哈希留在 prev_hash，
// 并把 changed_at 置成本轮时间——doctor 用它回答「这个文件什么时候变过」。
// 同一份内容重复写入（重扫、重启）不会记为变更。
func (s *Store) PutSource(ctx context.Context, st domain.SourceState, runAt int64) error {
	changedAt := int64(0)
	if !st.MTime.IsZero() {
		changedAt = runAt
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO source(agent, kind, path, content_hash, prev_hash, changed_at, mtime, size, last_seen_run, updated_at, last_error)
VALUES(?, ?, ?, ?, '', ?, ?, ?, ?, ?, '')
ON CONFLICT(agent, kind, path) DO UPDATE SET
  prev_hash = CASE WHEN source.content_hash = excluded.content_hash THEN source.prev_hash ELSE source.content_hash END,
  changed_at = CASE WHEN source.content_hash = excluded.content_hash THEN source.changed_at ELSE excluded.changed_at END,
  content_hash = excluded.content_hash,
  mtime = excluded.mtime,
  size = excluded.size,
  last_seen_run = excluded.last_seen_run,
  updated_at = excluded.updated_at,
  last_error = ''`,
		st.Agent, st.Kind, st.Path, st.ContentHash, changedAt,
		st.MTime.Unix(), st.Size, runAt, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("put source %s:%s: %w", st.Agent, st.Path, err)
	}
	return nil
}

// MarkSourceError 只登记错误，不动 content_hash 也不推进 last_seen_run。
// 推进了会让 stale 判定以为「这一轮确实见到了这个源」。
func (s *Store) MarkSourceError(ctx context.Context, agent, kind, path, msg string, at int64) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO source(agent, kind, path, content_hash, last_error, updated_at)
VALUES(?, ?, ?, '', ?, ?)
ON CONFLICT(agent, kind, path) DO UPDATE SET
  last_error = excluded.last_error,
  updated_at = excluded.updated_at`,
		agent, kind, path, msg, at)
	if err != nil {
		return fmt.Errorf("mark source error %s:%s: %w", agent, path, err)
	}
	return nil
}

func (s *Store) SourceHash(ctx context.Context, agent, kind, path string) (string, bool, error) {
	var hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT content_hash FROM source WHERE agent = ? AND kind = ? AND path = ?`, agent, kind, path).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

func (s *Store) GetSource(ctx context.Context, agent, kind, path string) (domain.SourceState, error) {
	var (
		st        domain.SourceState
		mtime     int64
		changedAt int64
	)
	err := s.db.QueryRowContext(ctx, `
SELECT agent, kind, path, content_hash, prev_hash, changed_at, mtime, size, last_error, last_seen_run
FROM source WHERE agent = ? AND kind = ? AND path = ?`, agent, kind, path).Scan(
		&st.Agent, &st.Kind, &st.Path, &st.ContentHash, &st.PrevHash, &changedAt,
		&mtime, &st.Size, &st.LastError, &st.LastSeenRun)
	if err != nil {
		return domain.SourceState{}, fmt.Errorf("get source %s:%s: %w", agent, path, err)
	}
	st.MTime = time.Unix(mtime, 0)
	if changedAt != 0 {
		st.ChangedAt = time.Unix(changedAt, 0)
	}
	return st, nil
}

type ChangedSource struct {
	Agent      string
	Kind       string
	Path       string
	PrevHash   string
	ContentHash string
	ChangedAt  time.Time
}

// ChangedSources 返回有变更记录的源，供 doctor 列出「哪些原生文件动过」。
// since 为 0 时不过滤。
func (s *Store) ChangedSources(ctx context.Context, since int64) ([]ChangedSource, error) {
	q := `SELECT agent, kind, path, prev_hash, content_hash, changed_at FROM source WHERE changed_at <> 0`
	args := []any{}
	if since > 0 {
		q += ` AND changed_at >= ?`
		args = append(args, since)
	}
	q += ` ORDER BY changed_at DESC LIMIT 200`

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangedSource
	for rows.Next() {
		var (
			c         ChangedSource
			changedAt int64
		)
		if err := rows.Scan(&c.Agent, &c.Kind, &c.Path, &c.PrevHash, &c.ContentHash, &changedAt); err != nil {
			return nil, err
		}
		c.ChangedAt = time.Unix(changedAt, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) SourceErrors(ctx context.Context) ([]ChangedSource, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT agent, kind, path, prev_hash, content_hash, updated_at FROM source
WHERE last_error <> '' ORDER BY updated_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChangedSource
	for rows.Next() {
		var (
			c         ChangedSource
			updatedAt int64
		)
		if err := rows.Scan(&c.Agent, &c.Kind, &c.Path, &c.PrevHash, &c.ContentHash, &updatedAt); err != nil {
			return nil, err
		}
		c.ChangedAt = time.Unix(updatedAt, 0)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Stats(ctx context.Context) (domain.Stats, error) {
	var st domain.Stats
	scalars := []struct {
		q    string
		dest *int
	}{
		{`SELECT count(*) FROM project`, &st.Projects},
		{`SELECT count(*) FROM memory_item`, &st.Memories},
		{`SELECT count(*) FROM memory_item WHERE stale = 1`, &st.Stale},
		{`SELECT count(*) FROM source`, &st.Sources},
		{`SELECT count(*) FROM source WHERE last_error <> ''`, &st.FailedSource},
		{`SELECT count(*) FROM source WHERE changed_at <> 0`, &st.Changed},
	}
	for _, sc := range scalars {
		if err := s.db.QueryRowContext(ctx, sc.q).Scan(sc.dest); err != nil {
			return st, err
		}
	}

	st.MemoriesByAgent = map[string]int{}
	rows, err := s.db.QueryContext(ctx, `
SELECT ms.agent, count(DISTINCT ms.memory_id) FROM memory_source ms GROUP BY ms.agent`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	for rows.Next() {
		var agent string
		var n int
		if err := rows.Scan(&agent, &n); err != nil {
			return st, err
		}
		st.MemoriesByAgent[agent] = n
	}
	if err := rows.Err(); err != nil {
		return st, err
	}

	st.MemoriesByType = map[string]int{}
	rows2, err := s.db.QueryContext(ctx, `SELECT type, count(*) FROM memory_item GROUP BY type`)
	if err != nil {
		return st, err
	}
	defer rows2.Close()
	for rows2.Next() {
		var typ string
		var n int
		if err := rows2.Scan(&typ, &n); err != nil {
			return st, err
		}
		st.MemoriesByType[typ] = n
	}
	return st, rows2.Err()
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: 提交**

```bash
git add internal/store/source.go internal/store/source_test.go
git commit -m "feat: 扫描源游标、变更历史与库统计"
```

---

## Task 11: Adapter 接口与 Qoder 记忆文件解析

**Files:**
- Create: `internal/agent/adapter.go`
- Create: `internal/agent/qoder/format.go`
- Test: `internal/agent/qoder/format_test.go`

- [ ] **Step 1: 写 `internal/agent/adapter.go`**

```go
package agent

import (
	"context"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
)

// MemoryUnit 是一个源文件及其解析结果。适配器负责读文件并给出 Content，
// 由 ingest 统一算哈希做幂等门控——这样「哪些字节代表这个源」只有一处定义。
//
// Err 非空表示这个源读/解析失败：ingest 会登记 last_error 并跳过其中的 Drafts，
// 一个坏文件不影响同目录其他文件。
type MemoryUnit struct {
	Path    string
	Content []byte
	MTime   time.Time
	Size    int64
	Drafts  []domain.MemoryDraft
	Err     error
}

// Adapter 把一个 agent 的原生记忆文件变成结构化草稿。只读，不写库，不认识其他 agent。
type Adapter interface {
	Name() string
	ScanMemoryUnits(ctx context.Context) ([]MemoryUnit, error)
}
```

- [ ] **Step 2: 写失败测试 `internal/agent/qoder/format_test.go`**

```go
package qoder

import (
	"path/filepath"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

func TestParseMemoryFile_FrontMatterTypeMapping(t *testing.T) {
	cases := []struct {
		metadata string
		want     domain.MemoryType
	}{
		{"user", domain.TypePreference},
		{"feedback", domain.TypeConstraint},
		{"project", domain.TypeFact},
		{"reference", domain.TypeFact},
		{"nonsense", domain.TypeFact},
	}
	for _, c := range cases {
		content := "---\nname: x\ndescription: 说明\nmetadata:\n  type: " + c.metadata + "\n---\n\n正文第一段。\n\n第二段。\n"
		got, ok, err := ParseMemoryFile(filepath.Join("dir", c.metadata+".md"), []byte(content))
		if err != nil || !ok {
			t.Fatalf("type %q: ok=%v err=%v", c.metadata, ok, err)
		}
		if got.Type != c.want {
			t.Fatalf("type %q mapped to %q want %q", c.metadata, got.Type, c.want)
		}
		// 整篇正文是一条记忆，frontmatter 不进内容
		if got.Text != "正文第一段。\n\n第二段。" {
			t.Fatalf("body not preserved: %q", got.Text)
		}
	}
}

func TestParseMemoryFile_NoFrontMatterIsFact(t *testing.T) {
	got, ok, err := ParseMemoryFile("/x/plain.md", []byte("# 标题\n\n- 一条没有 frontmatter 的手写记忆\n"))
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Type != domain.TypeFact {
		t.Fatalf("type = %q, want fact", got.Type)
	}
	if got.Text == "" {
		t.Fatal("empty text")
	}
}

func TestParseMemoryFile_EmptyBodyFallsBackToDescription(t *testing.T) {
	content := "---\nname: x\ndescription: 只有描述没有正文\nmetadata:\n  type: project\n---\n\n"
	got, ok, err := ParseMemoryFile("/x/d.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if !ok || got.Text != "只有描述没有正文" {
		t.Fatalf("ok=%v text=%q", ok, got.Text)
	}
}

func TestParseMemoryFile_SkipsIndexFile(t *testing.T) {
	content := "- [某项目动机](initiative.md) — 索引行不是记忆\n"
	for _, name := range []string{"MEMORY.md", "memory.md"} {
		got, ok, err := ParseMemoryFile(filepath.Join("/x", name), []byte(content))
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			t.Fatalf("%s should be skipped as an index file, got %+v", name, got)
		}
	}
}

func TestParseMemoryFile_UnclosedFrontMatterTreatedAsBody(t *testing.T) {
	got, ok, err := ParseMemoryFile("/x/u.md", []byte("---\nname: x\n\n正文\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("should still yield an item")
	}
	if got.Type != domain.TypeFact {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Text == "" {
		t.Fatal("empty text")
	}
}

func TestParseMemoryFile_IndentedColonInDescription(t *testing.T) {
	content := "---\nname: x\ndescription: 关键设计: 不能提前扣减余额\nmetadata:\n  type: feedback\n---\n\n正文\n"
	got, _, err := ParseMemoryFile("/x/c.md", []byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if got.Type != domain.TypeConstraint {
		t.Fatalf("type = %q", got.Type)
	}
	if got.Text != "正文" {
		t.Fatalf("text = %q", got.Text)
	}
}

func TestParseMemoryFile_BlankFileIsSkipped(t *testing.T) {
	if _, ok, err := ParseMemoryFile("/x/e.md", []byte("  \n\n")); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}
```

- [ ] **Step 3: 跑测试确认失败**

Run: `go test ./internal/agent/... -v`
Expected: FAIL，`undefined: ParseMemoryFile`

- [ ] **Step 4: 写 `internal/agent/qoder/format.go`**

```go
package qoder

import (
	"path/filepath"
	"strings"

	"github.com/muyi99/agent-memory/internal/domain"
)

// metadata.type → 记忆类型。Qoder 只会写 user/feedback/project/reference 四种。
var typeByMetadata = map[string]domain.MemoryType{
	"user":      domain.TypePreference,
	"feedback":  domain.TypeConstraint,
	"project":   domain.TypeFact,
	"reference": domain.TypeFact,
}

// ParseMemoryFile 解析一个 Qoder 记忆文件：整篇正文是一条记忆。
// 返回值第二参数为 false 表示这个文件不产出内容（索引文件、空文件）。
func ParseMemoryFile(path string, content []byte) (domain.MemoryDraft, bool, error) {
	if strings.EqualFold(filepath.Base(path), "MEMORY.md") {
		return domain.MemoryDraft{}, false, nil
	}
	text := strings.TrimSpace(string(content))
	if text == "" {
		return domain.MemoryDraft{}, false, nil
	}

	typ := domain.TypeFact
	body := text
	if fm, rest, ok := splitFrontMatter(text); ok {
		if mapped, hit := typeByMetadata[fm["type"]]; hit {
			typ = mapped
		}
		body = strings.TrimSpace(rest)
		if body == "" {
			body = strings.TrimSpace(fm["description"])
		}
	}
	if body == "" {
		return domain.MemoryDraft{}, false, nil
	}
	return domain.MemoryDraft{Type: typ, Text: body, SourcePath: path, Quote: body}, true, nil
}

// splitFrontMatter 只取本项目用得上的三个键：name、description、metadata.type。
// 不引入 YAML 依赖：这些 frontmatter 由 agent 自己生成，形状固定，手写解析更好控制失败行为。
func splitFrontMatter(s string) (map[string]string, string, bool) {
	lines := strings.Split(s, "\n")
	if strings.TrimSpace(lines[0]) != "---" {
		return nil, s, false
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, s, false
	}

	fm := map[string]string{}
	inMetadata := false
	for _, line := range lines[1:end] {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented := line[0] == ' ' || line[0] == '\t'
		key, value, ok := cutKey(line)
		if !ok {
			continue
		}
		if !indented {
			inMetadata = key == "metadata"
		}
		switch {
		case key == "name":
			fm["name"] = value
		case key == "description":
			fm["description"] = value
		case inMetadata && key == "type":
			fm["type"] = value
		}
	}
	return fm, strings.Join(lines[end+1:], "\n"), true
}

func cutKey(line string) (key, value string, ok bool) {
	trimmed := strings.TrimSpace(line)
	i := strings.Index(trimmed, ":")
	if i <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(trimmed[:i]), strings.Trim(strings.TrimSpace(trimmed[i+1:]), `"'`), true
}
```

- [ ] **Step 5: 跑测试确认通过**

Run: `go test ./internal/agent/... -v`
Expected: PASS（7 个测试）

- [ ] **Step 6: 提交**

```bash
git add internal/agent/adapter.go internal/agent/qoder/format.go internal/agent/qoder/format_test.go
git commit -m "feat: Agent 适配器接口与 Qoder 记忆文件解析"
```

---

## Task 12: Qoder 目录发现与会话反查项目路径

**Files:**
- Create: `internal/agent/qoder/paths.go`
- Create: `internal/agent/qoder/adapter.go`
- Test: `internal/agent/qoder/paths_test.go`
- Test: `internal/agent/qoder/adapter_test.go`

- [ ] **Step 1: 写失败测试 `internal/agent/qoder/paths_test.go`**

```go
package qoder

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverHome_PrefersQoderCNAgent(t *testing.T) {
	base := t.TempDir()
	mk(t, filepath.Join(base, ".qoder", "projects"))
	mk(t, filepath.Join(base, ".qoder-cn", "memory"))

	got, ok := DiscoverHome(base)
	if !ok {
		t.Fatal("not found")
	}
	if filepath.Base(got) != ".qoder-cn" {
		t.Fatalf("got %q, want .qoder-cn first", got)
	}
}

func TestDiscoverHome_FallsBackToPlainQoder(t *testing.T) {
	base := t.TempDir()
	mk(t, filepath.Join(base, ".qoder", "memory"))
	if got, ok := DiscoverHome(base); !ok || filepath.Base(got) != ".qoder" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
}

func TestDiscoverHome_IgnoresEmptyLookalikeDirs(t *testing.T) {
	base := t.TempDir()
	mk(t, filepath.Join(base, ".qoder")) // 存在但没有 memory/projects
	if _, ok := DiscoverHome(base); ok {
		t.Fatal("empty lookalike dir must not count as a Qoder home")
	}
}

func TestProjectPathFromSessions_HighConfidenceFromJSONL(t *testing.T) {
	dir := t.TempDir()
	// 目标目录必须真实存在才可信
	target := filepath.Join(t.TempDir(), "repo")
	mk(t, target)

	content := "{\"type\":\"runtime-config\",\"sessionId\":\"s1\"}\n" +
		"{\"type\":\"workspace-directories\",\"sessionId\":\"s1\",\"directories\":[\"" + jsonEscape(target) + "\"]}\n" +
		"{\"type\":\"user\",\"sessionId\":\"s1\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "s1.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, conf := ProjectPathFromSessions(dir)
	if conf != "high" {
		t.Fatalf("confidence = %q, want high", conf)
	}
	if filepath.Clean(got) != filepath.Clean(target) {
		t.Fatalf("got %q want %q", got, target)
	}
}

func TestProjectPathFromSessions_SkipsHugeLinesBeyondBuffer(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "repo2")
	mk(t, target)
	// 技能正文之类会把 workspace-directories 行挤到很后面；行本身也可能极长
	long := make([]byte, 3*1024*1024)
	for i := range long {
		long[i] = 'x'
	}
	content := "{\"type\":\"user\",\"content\":\"" + string(long) + "\"}\n" +
		"{\"type\":\"workspace-directories\",\"directories\":[\"" + jsonEscape(target) + "\"]}\n"
	if err := os.WriteFile(filepath.Join(dir, "s.jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got, conf := ProjectPathFromSessions(dir)
	if conf != "high" || filepath.Clean(got) != filepath.Clean(target) {
		t.Fatalf("3MB 行不应被丢弃: got %q conf=%q", got, conf)
	}
}

func TestProjectPathFromSessions_FallsBackToSlugWithLowConfidence(t *testing.T) {
	dir := t.TempDir()
	slug := "E--company-nuonuo-fresh3-api"
	project := filepath.Join(dir, slug)
	mk(t, project)

	got, conf := ProjectPathFromSessions(project)
	if conf != "low" {
		t.Fatalf("no session data means low confidence, got %q", conf)
	}
	// 反解只能用于展示：项目名里的连字符与层级分隔符无法区分，这里必然解错
	if filepath.Clean(got) == filepath.Clean(`E:\company\nuonuo-fresh3-api`) {
		t.Fatal("slug decode unexpectedly correct — the test asserts it is unreliable")
	}
}

func TestDecodeSlug(t *testing.T) {
	got, ok := decodeSlug("F--go-agent-memory")
	if !ok || got == "" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	if _, ok := decodeSlug("novalue"); ok {
		t.Fatal("slug without -- should not decode")
	}
}

func mk(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func jsonEscape(s string) string {
	out := make([]rune, 0, len(s)+4)
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		default:
			out = append(out, r)
		}
	}
	return string(out)
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/agent/qoder/ -run 'TestDiscoverHome|TestProjectPath|TestDecodeSlug' -v`
Expected: FAIL，`undefined: DiscoverHome`

- [ ] **Step 3: 写 `internal/agent/qoder/paths.go`**

```go
package qoder

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiscoverHome 在 base（通常是用户目录）下找 Qoder 配置根。
// 本机同时存在 ~/.qoder 与 ~/.qoder-cn，取真正装着 memory/projects 的那个；
// 判据用子目录存在性，避免命中同名的空壳目录。
func DiscoverHome(base string) (string, bool) {
	for _, name := range []string{".qoder-cn", ".qoder"} {
		dir := filepath.Join(base, name)
		if dirExists(filepath.Join(dir, "memory")) || dirExists(filepath.Join(dir, "projects")) {
			return dir, true
		}
	}
	return "", false
}

func dirExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

const sessionScanMaxLines = 500

// ProjectPathFromSessions 反查一个 Qoder projects/<slug> 目录对应的真实工作目录。
// 会话 JSONL 里的 workspace-directories 行是权威来源；找不到时退回 slug 反解并标 low。
func ProjectPathFromSessions(projectDir string) (path string, confidence string) {
	files, err := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	if err == nil {
		sort.Strings(files)
		for _, f := range files {
			if p, ok := firstWorkspaceDir(f); ok {
				return p, "high"
			}
		}
	}
	if p, ok := decodeSlug(filepath.Base(projectDir)); ok {
		return p, "low"
	}
	return "", "low"
}

type workspaceLine struct {
	Type        string   `json:"type"`
	Directories []string `json:"directories"`
}

func firstWorkspaceDir(path string) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	// 单行可能是几十 KB 的技能正文，甚至几 MB；默认 64KB 上限会直接报 too long。
	scanner.Buffer(make([]byte, 0, 256*1024), 16*1024*1024)
	for n := 0; n < sessionScanMaxLines && scanner.Scan(); n++ {
		line := scanner.Bytes()
		if !bytes.Contains(line, []byte("workspace-directories")) {
			continue
		}
		var w workspaceLine
		if err := json.Unmarshal(line, &w); err != nil {
			continue
		}
		if w.Type != "workspace-directories" || len(w.Directories) == 0 {
			continue
		}
		if w.Directories[0] == "" {
			continue
		}
		return w.Directories[0], true
	}
	return "", false
}

// decodeSlug 把 Qoder 的目录名猜回路径，只用于展示。
// 不可靠是已知的：项目名本身含连字符（nuonuo-fresh3-api），'-' 与层级分隔符无法区分，
// 所以调用方拿到的一律是 low 置信度，ingest 不据此合并项目。
func decodeSlug(slug string) (string, bool) {
	i := strings.Index(slug, "--")
	if i <= 0 {
		return "", false
	}
	drive := strings.ToUpper(slug[:i])
	if len(drive) != 1 {
		return "", false
	}
	rest := strings.ReplaceAll(slug[i+2:], "-", string(filepath.Separator))
	if rest == "" {
		return "", false
	}
	return drive + ":" + string(filepath.Separator) + rest, true
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/agent/qoder/ -run 'TestDiscoverHome|TestProjectPath|TestDecodeSlug' -v`
Expected: PASS

- [ ] **Step 5: 写失败测试 `internal/agent/qoder/adapter_test.go`**

```go
package qoder

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/muyi99/agent-memory/internal/domain"
)

// buildFakeHome 造一个和真实布局同构的假 Qoder 目录。
// 返回 home，以及三个便于断言的路径：真实项目目录、项目 slug、两个记忆文件路径。
func buildFakeHome(t *testing.T) (home string, projectRealDir string, userMemoryPath string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, ".qoder-cn")
	projectRealDir = filepath.Join(root, "repo", "fresh3-api")
	mustMkdirAll(t, projectRealDir)
	mustMkdirAll(t, filepath.Join(home, "memory"))
	slug := "X--repo-fresh3-api"
	mustMkdirAll(t, filepath.Join(home, "projects", slug, "memory"))

	session := "{\"type\":\"workspace-directories\",\"sessionId\":\"a1\",\"directories\":[\"" + jsonEscape(projectRealDir) + "\"]}\n"
	writeFile(t, filepath.Join(home, "projects", slug, "a1.jsonl"), session)

	userMemory := `---
name: user-profile
description: 用户画像
metadata:
  type: user
---

用户主要维护 Laravel 后端，中文沟通。
`
	writeFile(t, filepath.Join(home, "memory", "user-profile.md"), userMemory)
	// 索引文件必须被跳过
	writeFile(t, filepath.Join(home, "memory", "MEMORY.md"), "- [用户画像](user-profile.md) — 索引\n")

	projectMemory := `---
name: initiative
description: 项目状态
metadata:
  type: project
---

M1 已批准，等实施计划。
`
	writeFile(t, filepath.Join(home, "projects", slug, "memory", "initiative.md"), projectMemory)

	return home, projectRealDir, filepath.Join(home, "memory", "user-profile.md")
}

func TestAdapter_ScanMemoryUnits(t *testing.T) {
	home, projectRealDir, _ := buildFakeHome(t)
	a := New(home)
	units, err := a.ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 2 {
		t.Fatalf("want 2 units (MEMORY.md 被跳过), got %d", len(units))
	}

	var sawUser, sawProject bool
	for _, u := range units {
		if u.Err != nil {
			t.Fatalf("unexpected unit error for %s: %v", u.Path, u.Err)
		}
		if len(u.Drafts) != 1 {
			t.Fatalf("%s: want 1 draft, got %d", u.Path, len(u.Drafts))
		}
		if u.Size != int64(len(u.Content)) || u.MTime.IsZero() {
			t.Fatalf("%s: size/mtime not populated", u.Path)
		}
		d := u.Drafts[0]
		if d.SourceAgent != "qoder" {
			t.Fatalf("agent = %q", d.SourceAgent)
		}
		switch d.Scope {
		case domain.ScopeUser:
			sawUser = true
			if d.Type != domain.TypePreference || d.ProjectPath != "" {
				t.Fatalf("user draft wrong: %+v", d)
			}
		case domain.ScopeProject:
			sawProject = true
			if d.Type != domain.TypeFact {
				t.Fatalf("project draft type = %q", d.Type)
			}
			if filepath.Clean(d.ProjectPath) != filepath.Clean(projectRealDir) {
				t.Fatalf("project path not resolved: %q vs %q", d.ProjectPath, projectRealDir)
			}
			if d.ProjectSlug != "X--repo-fresh3-api" || d.Confidence != "high" {
				t.Fatalf("slug/confidence: %q / %q", d.ProjectSlug, d.Confidence)
			}
		default:
			t.Fatalf("unexpected scope %q", d.Scope)
		}
	}
	if !sawUser || !sawProject {
		t.Fatalf("want both scopes, user=%v project=%v", sawUser, sawProject)
	}
}

func TestAdapter_ProjectWithoutSessionsKeepsLowConfidence(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	slug := "Y--loose-project"
	mustMkdirAll(t, filepath.Join(home, "memory"))
	mustMkdirAll(t, filepath.Join(home, "projects", slug, "memory"))
	writeFile(t, filepath.Join(home, "projects", slug, "memory", "m.md"), "只有一行正文\n")

	units, err := New(home).ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("got %d units", len(units))
	}
	d := units[0].Drafts[0]
	if d.Confidence != "low" || d.ProjectSlug != slug {
		t.Fatalf("confidence=%q slug=%q", d.Confidence, d.ProjectSlug)
	}
}

func TestAdapter_SkipsProjectDirsWithoutMemoryDir(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	mustMkdirAll(t, filepath.Join(home, "memory"))
	mustMkdirAll(t, filepath.Join(home, "projects", "Z--nothing"))
	writeFile(t, filepath.Join(home, "memory", "a.md"), "一条用户记忆\n")

	units, err := New(home).ScanMemoryUnits(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(units) != 1 {
		t.Fatalf("want only the user unit, got %d", len(units))
	}
}

func mustMkdirAll(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 6: 跑测试确认失败**

Run: `go test ./internal/agent/qoder/ -run TestAdapter -v`
Expected: FAIL，`undefined: New`

- [ ] **Step 7: 写 `internal/agent/qoder/adapter.go`**

```go
package qoder

import (
	"context"
	"os"
	"path/filepath"
	"sort"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/domain"
)

// Adapter 读 Qoder 的记忆文件。Home 是 Qoder 配置根（含 memory/、projects/）。
type Adapter struct {
	Home string
}

func New(home string) *Adapter { return &Adapter{Home: home} }

func (a *Adapter) Name() string { return "qoder" }

func (a *Adapter) ScanMemoryUnits(ctx context.Context) ([]agent.MemoryUnit, error) {
	var units []agent.MemoryUnit

	units = append(units, a.scanFlat(ctx, filepath.Join(a.Home, "memory"), domain.ScopeUser, "", "", "high")...)

	projectsDir := filepath.Join(a.Home, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil && !os.IsNotExist(err) {
		return units, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return units, err
		}
		projectDir := filepath.Join(projectsDir, name)
		memoryDir := filepath.Join(projectDir, "memory")
		if !dirExists(memoryDir) {
			continue
		}
		// 只有带会话的项目目录才给出可信路径；这里拿不到就别硬猜
		realPath, confidence := ProjectPathFromSessions(projectDir)
		units = append(units, a.scanFlat(ctx, memoryDir, domain.ScopeProject, realPath, name, confidence)...)
	}
	return units, nil
}

func (a *Adapter) scanFlat(ctx context.Context, dir string, scope domain.Scope, projectPath, slug, confidence string) []agent.MemoryUnit {
	var out []agent.MemoryUnit
	entries, err := os.ReadDir(dir)
	if err != nil {
		return out
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !isMarkdown(e.Name()) {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)

	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return out
		}
		path := filepath.Join(dir, name)
		unit := agent.MemoryUnit{Path: path}
		content, err := os.ReadFile(path)
		if err != nil {
			unit.Err = err
			out = append(out, unit)
			continue
		}
		unit.Content = content
		unit.Size = int64(len(content))
		if st, err := os.Stat(path); err == nil {
			unit.MTime = st.ModTime()
		}

		draft, ok, err := ParseMemoryFile(path, content)
		if err != nil {
			unit.Err = err
			out = append(out, unit)
			continue
		}
		if !ok {
			continue
		}
		draft.Scope = scope
		draft.SourceAgent = a.Name()
		draft.ProjectPath = projectPath
		draft.ProjectSlug = slug
		draft.Confidence = confidence
		unit.Drafts = []domain.MemoryDraft{draft}
		out = append(out, unit)
	}
	return out
}

func isMarkdown(name string) bool {
	return filepath.Ext(name) == ".md"
}
```

MTime 与 Size 都从文件本身取（`os.Stat`），不依赖 `ReadDir` 的 DirEntry——扫描顺序与文件状态之间不需要保持一致性，少一层耦合。

- [ ] **Step 8: 跑测试确认通过**

Run: `go test ./internal/agent/... -v`
Expected: PASS

- [ ] **Step 9: 提交**

```bash
git add internal/agent/qoder
git commit -m "feat: Qoder 目录发现与会话反查项目路径适配器"
```

---

## Task 13: 扫描编排、幂等门控与过时标记

**Files:**
- Create: `internal/ingest/ingest.go`
- Modify: `internal/store/source.go`（追加 `TouchSource`）
- Modify: `internal/store/source_test.go`（追加 `TestTouchSource`）
- Test: `internal/ingest/ingest_test.go`

这一步是验收标准 3 的落点：内容哈希未变的源**完全不再触碰记忆行**，所以第二次扫描后库内容与第一次逐字节相同。

注意一个容易漏的坑：哈希没变时要跳过写入，但仍必须刷新 `last_seen_run`——否则紧接着的 `MarkStaleExcept` 会把「没变化」误判成「本轮不见了」，把全部记忆标成过时。这就是要加 `TouchSource` 的原因。

- [ ] **Step 1: 在 `internal/store/source_test.go` 追加失败测试**

```go
func TestTouchSource_RefreshesSeenRunWithoutTouchingHash(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run1 := time.Unix(1700000000, 0)
	run2 := run1.Add(time.Hour)
	st := domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1", MTime: run1, Size: 5}
	if err := s.PutSource(ctx, st, run1.Unix()); err != nil {
		t.Fatal(err)
	}

	if err := s.TouchSource(ctx, "qoder", "memory", "/q/a.md", run2.Unix()); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetSource(ctx, "qoder", "memory", "/q/a.md")
	if err != nil {
		t.Fatal(err)
	}
	if row.ContentHash != "h1" || row.LastSeenRun != run2.Unix() {
		t.Fatalf("hash=%q seen_run=%d want h1/%d", row.ContentHash, row.LastSeenRun, run2.Unix())
	}
	if row.ChangedAt.IsZero() == false {
		t.Fatalf("touch should not record a content change: %+v", row)
	}

	// 源不存在时 touch 应当显式失败，避免编排层静默漏更新
	if err := s.TouchSource(ctx, "qoder", "memory", "/q/missing.md", run2.Unix()); err == nil {
		t.Fatal("want error when source row does not exist")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestTouchSource -v`
Expected: FAIL，`s.TouchSource undefined`

- [ ] **Step 3: 在 `internal/store/source.go` 末尾追加 `TouchSource`**

```go
// TouchSource 只刷新 last_seen_run：内容没变的源仍然要在本轮"露过面"，
// 否则 MarkStaleExcept 会把没变化的条目误判成已消失。
func (s *Store) TouchSource(ctx context.Context, agent, kind, path string, runAt int64) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE source SET last_seen_run = ?, updated_at = ?
WHERE agent = ? AND kind = ? AND path = ?`,
		runAt, time.Now().Unix(), agent, kind, path)
	if err != nil {
		return fmt.Errorf("touch source %s:%s: %w", agent, path, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("touch source %s:%s: no such row", agent, path)
	}
	return nil
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./internal/store/ -v`
Expected: PASS

- [ ] **Step 5: 写 `internal/store/query.go`（测试与 CLI 都要用的单值查询原语）**

不给测试专门造 `CountProjectsAndPaths` 这类一次性方法——那会在生产代码里留下没人调用的死代码。导出两个通用原语，SQL 由调用方给：

```go
package store

import "context"

// QueryInt 跑单值聚合查询，SQL 由调用方给出。
func (s *Store) QueryInt(ctx context.Context, q string, args ...any) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&n)
	return n, err
}

// QueryStrings 跑多行单列查询。
func (s *Store) QueryStrings(ctx context.Context, q string, args ...any) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}
```

- [ ] **Step 6: 写失败测试 `internal/ingest/ingest_test.go`**

```go
package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/muyi99/agent-memory/internal/agent/qoder"
	"github.com/muyi99/agent-memory/internal/store"
)

func openTempStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(filepath.Join(t.TempDir(), "memory.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// fakeQoder 造一个与真实布局同构的假 home：
// 一条用户记忆 + 一个索引文件（应跳过）+ 两个各自带会话 JSONL 的项目。
func fakeQoder(t *testing.T) (home string, repoA string, repoB string) {
	t.Helper()
	root := t.TempDir()
	home = filepath.Join(root, ".qoder-cn")
	repoA = filepath.Join(root, "repos", "alpha")
	repoB = filepath.Join(root, "repos", "beta")
	write(t, filepath.Join(home, "memory", "user.md"), "---\nmetadata:\n  type: user\n---\n\n沟通用中文。\n")
	write(t, filepath.Join(home, "memory", "MEMORY.md"), "- 索引行\n")
	link(t, home, "RA", repoA, "a.jsonl", "---\nmetadata:\n  type: project\n---\n\nalpha 项目状态。\n")
	link(t, home, "RB", repoB, "b.jsonl", "---\nmetadata:\n  type: feedback\n---\n\n余额扣减需乐观锁。\n")
	return home, repoA, repoB
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// link 建一个 Qoder 项目目录：slug 目录 + 会话 JSONL（提供真实工作目录）+ memory 子目录
func link(t *testing.T, home, slug, realDir, sessionName, memory string) {
	t.Helper()
	dir := filepath.Join(home, "projects", slug)
	write(t, filepath.Join(dir, sessionName), fmt.Sprintf("{\"type\":\"workspace-directories\",\"directories\":[%q]}\n", realDir))
	write(t, filepath.Join(dir, "memory", "m.md"), memory)
}

// dump 把幂等判定所需的库内容拼成可比较字符串：记忆行 + 来源行。
func dump(t *testing.T, s *store.Store) string {
	t.Helper()
	ctx := context.Background()
	items, err := s.QueryStrings(ctx, `
SELECT id || '|' || scope || '|' || project_id || '|' || type || '|' || text_norm_hash || '|' || stale || '|' || first_seen_at || '|' || last_seen_at
FROM memory_item ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	sources, err := s.QueryStrings(ctx, `
SELECT memory_id || '|' || agent || '|' || path
FROM memory_source ORDER BY memory_id, agent, path`)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(items, "\n") + "\n#\n" + strings.Join(sources, "\n")
}

func mustInt(t *testing.T, s *store.Store, query string, args ...any) int {
	t.Helper()
	n, err := s.QueryInt(context.Background(), query, args...)
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRun_ImportsMemoriesAndIsIdempotent(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	r := New(s, qoder.New(home))

	first, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", first.Errors)
	}
	// 4 个 md 里有一个是 MEMORY.md 索引，应导入 3 条
	if first.Units != 3 || first.Created != 3 {
		t.Fatalf("first run = %+v, want units=3 created=3", first)
	}

	before := dump(t, s)
	second, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if second.Created != 0 || second.Merged != 0 {
		t.Fatalf("unchanged sources must not touch memory rows: %+v", second)
	}
	if second.Unchanged != 3 {
		t.Fatalf("unchanged = %d, want 3", second.Unchanged)
	}
	if after := dump(t, s); after != before {
		t.Fatalf("database changed between identical scans:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

func TestRun_MergesTwoCheckoutsByGitRemote(t *testing.T) {
	home, repoA, repoB := fakeQoder(t)
	// 同一个仓库的两份 checkout：路径不同，origin 相同
	for _, dir := range []string{repoA, repoB} {
		write(t, filepath.Join(dir, ".git", "config"), "[remote \"origin\"]\n\turl = git@github.com:acme/mono.git\n")
	}
	s := openTempStore(t)
	if _, err := New(s, qoder.New(home)).Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project`); got != 1 {
		t.Fatalf("same remote should merge into 1 project, got %d", got)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project_path`); got != 2 {
		t.Fatalf("want 2 path mappings, got %d", got)
	}
}

func TestRun_LowConfidenceSlugStaysItsOwnProject(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	slug := "Z--some-project"
	write(t, filepath.Join(home, "memory", "u.md"), "用户级记忆\n")
	// 没有会话 JSONL：路径只能靠 slug 反解，不可信
	write(t, filepath.Join(home, "projects", slug, "memory", "m.md"), "项目级记忆\n")

	s := openTempStore(t)
	if _, err := New(s, qoder.New(home)).Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project_path`); got != 0 {
		t.Fatalf("low-confidence path must not be registered in project_path, got %d rows", got)
	}
	keys, err := s.QueryStrings(context.Background(), `SELECT key FROM project ORDER BY key`)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) == 0 || keys[0] != "qoder:"+slug {
		t.Fatalf("project keys = %v, want first %q", keys, "qoder:"+slug)
	}
}

func TestRun_MarksStaleWhenSourceFileDisappears(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	r := New(s, qoder.New(home))
	if _, err := r.Run(context.Background(), ""); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(filepath.Join(home, "memory", "user.md")); err != nil {
		t.Fatal(err)
	}
	third, err := r.Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if third.Staled != 1 {
		t.Fatalf("staled = %d, want 1", third.Staled)
	}
	// 只统计 qoder 支撑的过时条目，别的 agent 的不应被波及
	qoderStale := mustInt(t, s, `SELECT count(*) FROM memory_item
WHERE stale = 1 AND id IN (SELECT memory_id FROM memory_source WHERE agent = ?)`, "qoder")
	if qoderStale != 1 {
		t.Fatalf("qoder-sourced stale count = %d, want 1", qoderStale)
	}
}

func TestRun_OnlyAgentFilter(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	res, err := New(s, qoder.New(home)).Run(context.Background(), "trae")
	if err != nil {
		t.Fatal(err)
	}
	if res.Units != 0 {
		t.Fatalf("filtering to trae should import nothing, got %d units", res.Units)
	}
}

func TestRun_ContextCancelledStopsBeforeWriting(t *testing.T) {
	home, _, _ := fakeQoder(t)
	s := openTempStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New(s, qoder.New(home)).Run(ctx, ""); err == nil {
		t.Fatal("want error from cancelled context")
	}
	if got := mustInt(t, s, `SELECT count(*) FROM project_path`); got != 0 {
		t.Fatalf("cancelled run wrote state: %d project_path rows", got)
	}
}

func TestRun_RecordsSourceErrorAndKeepsOthers(t *testing.T) {
	home, _, _ := fakeQoder(t)
	// 目录形式的 .md 会让 ReadFile 失败
	bad := filepath.Join(home, "projects", "RA", "memory", "bad.md")
	if err := os.MkdirAll(bad, 0o755); err != nil {
		t.Fatal(err)
	}
	s := openTempStore(t)
	res, err := New(s, qoder.New(home)).Run(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) == 0 {
		t.Fatal("want a recorded error for the unreadable source")
	}
	if res.Created != 3 {
		t.Fatalf("healthy files should still import, created=%d want 3", res.Created)
	}
	if got := mustInt(t, s, `SELECT count(*) FROM source WHERE last_error <> ''`); got == 0 {
		t.Fatal("source last_error not persisted")
	}
}

func TestResult_StringIsReadable(t *testing.T) {
	got := Result{Units: 4, Created: 2, Merged: 1, Unchanged: 1, Staled: 0}.String()
	for _, want := range []string{"units=4", "created=2", "merged=1", "unchanged=1", "staled=0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
}

func TestValidateAgent(t *testing.T) {
	if err := ValidateAgent([]string{"qoder"}, "qoder"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateAgent([]string{"qoder"}, ""); err != nil {
		t.Fatal(err)
	}
	err := ValidateAgent([]string{"qoder", "trae"}, "cursor")
	if err == nil || !strings.Contains(err.Error(), "cursor") {
		t.Fatalf("want unknown-agent error, got %v", err)
	}
}
```

- [ ] **Step 7: 跑测试确认失败**

Run: `go test ./internal/ingest/ -v`
Expected: FAIL，`undefined: New` / `undefined: ValidateAgent`

- [ ] **Step 8: 写 `internal/ingest/ingest.go`**

```go
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

// Run 扫描全部（或 onlyAgent 指定的一个）适配器。
// 单个源的失败记进 Result.Errors，不中断整轮。
func (r *Runner) Run(ctx context.Context, onlyAgent string) (Result, error) {
	var res Result
	runAt := time.Now().Unix()
	at := time.Unix(runAt, 0)

	for _, a := range r.adapters {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if onlyAgent != "" && onlyAgent != a.Name() {
			continue
		}
		units, err := a.ScanMemoryUnits(ctx)
		if err != nil {
			res.Errors = append(res.Errors, fmt.Sprintf("%s scan: %v", a.Name(), err))
			continue
		}

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

		n, err := r.store.MarkStaleExcept(ctx, a.Name(), runAt)
		if err != nil {
			return res, err
		}
		res.Staled += n
	}
	return res, nil
}

// projectFor 决定这批草稿属于哪个项目。
//
// 低置信（只有 slug 反解出来的路径）时故意建一个 agent 专有的独立项目，
// 并且不写 project_path 映射：slug 里的 '-' 既可能是层级分隔符也可能是项目名的一部分，
// 猜错就会把两个不同项目并成一个，那比留两个孤立项目更糟。
func (r *Runner) projectFor(ctx context.Context, drafts []domain.MemoryDraft, at time.Time) (int64, error) {
	if len(drafts) == 0 {
		return 0, nil
	}
	d := drafts[0]
	if d.Scope != domain.ScopeProject {
		return 0, nil
	}
	if d.Confidence != "high" || d.ProjectPath == "" {
		key := d.SourceAgent + ":" + d.ProjectSlug
		return r.store.EnsureProject(ctx, key, d.ProjectSlug, "", "", at)
	}
	if id, ok, err := r.store.ResolveProjectByPath(ctx, d.ProjectPath); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	remote, _, err := r.RemoteReader(d.ProjectPath)
	if err != nil {
		remote = ""
	}
	id, _, err := r.store.ProjectIDForPath(ctx, d.SourceAgent, d.ProjectSlug, d.ProjectPath, remote, at)
	return id, err
}

// Agents 返回已注册的适配器名，CLI 的 --agent 校验与 doctor 展示都用它。
func (r *Runner) Agents() []string {
	names := make([]string, 0, len(r.adapters))
	for _, a := range r.adapters {
		names = append(names, a.Name())
	}
	return names
}

// ValidateAgent 检查 --agent 取值，报错时列出可用适配器。CLI 与测试都用它。
func ValidateAgent(allowed []string, want string) error {
	if want == "" {
		return nil
	}
	for _, a := range allowed {
		if a == want {
			return nil
		}
	}
	return fmt.Errorf("unknown agent %q; available: %s", want, strings.Join(allowed, ", "))
}
```

- [ ] **Step 9: 跑测试确认通过**

Run: `go test ./internal/ingest/ -v`
Expected: PASS（7 个测试）

- [ ] **Step 10: 提交**

```bash
git add internal/ingest internal/store/query.go internal/store/source.go internal/store/source_test.go
git commit -m "feat: 扫描编排与幂等门控，含 TouchSource 修正 stale 误判"
```

---

## Task 14: 配置加载与目录发现

**Files:**
- Create: `internal/config/config.go`
- Test: `internal/config/config_test.go`

- [ ] **Step 1: 写失败测试 `internal/config/config_test.go`**

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDefault_DataDirUnderUserHome(t *testing.T) {
	home := t.TempDir()
	c := Default(home)
	if c.DataDir != filepath.Join(home, ".agent-memory") {
		t.Fatalf("DataDir = %q", c.DataDir)
	}
	if c.Qoder.Disabled {
		t.Fatal("qoder should default to enabled")
	}
	if c.Scan.Interval != 30*time.Second {
		t.Fatalf("interval = %v, want 30s", c.Scan.Interval)
	}
}

func TestLoad_MissingFileReturnsDefaults(t *testing.T) {
	c, err := Load(filepath.Join(t.TempDir(), "nope.toml"), "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.Scan.Interval != 30*time.Second {
		t.Fatalf("want defaults, got %+v", c)
	}
}

func TestLoad_PartialFileKeepsOtherDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	body := "data_dir = \"/tmp/am\"\n[qoder]\nhome = \"/opt/qoder-cn\"\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/tmp/am" {
		t.Fatalf("data_dir = %q", c.DataDir)
	}
	if c.Qoder.Home != "/opt/qoder-cn" {
		t.Fatalf("qoder.home = %q", c.Qoder.Home)
	}
	if c.Scan.Interval != 30*time.Second {
		t.Fatalf("unset scan interval should keep default, got %v", c.Scan.Interval)
	}
}

func TestLoad_RejectsUnknownTopLevelKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("dat_dir = \"/tmp/x\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path, "/fake/home"); err == nil {
		t.Fatal("typo'd key should be an error, not silently ignored")
	}
}

func TestQoderHome_OverrideThenDiscovery(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, ".qoder-cn")
	if err := os.MkdirAll(filepath.Join(home, "memory"), 0o755); err != nil {
		t.Fatal(err)
	}
	c := Default(root)
	if got := c.QoderHome(); got != home {
		t.Fatalf("discovered %q want %q", got, home)
	}

	c.Qoder.Home = filepath.Join(root, "manual")
	if got := c.QoderHome(); got != c.Qoder.Home {
		t.Fatalf("explicit home should win, got %q", got)
	}

	c.Qoder.Disabled = true
	if got := c.QoderHome(); got != "" {
		t.Fatalf("disabled agent should resolve to empty, got %q", got)
	}
}

func TestQoderHome_EmptyWhenNothingFound(t *testing.T) {
	c := Default(filepath.Join(t.TempDir(), "nothing"))
	if got := c.QoderHome(); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

func TestSaveThenLoad_RoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	c := Default("/fake/home")
	c.DataDir = "/tmp/mydata"
	c.Scan.Interval = 45 * time.Second
	if err := Save(path, c); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path, "/fake/home")
	if err != nil {
		t.Fatal(err)
	}
	if got.DataDir != "/tmp/mydata" || got.Scan.Interval != 45*time.Second {
		t.Fatalf("round trip lost values: %+v", got)
	}
}

func TestDBPathAndConfigPath(t *testing.T) {
	c := Default("/fake/home")
	c.DataDir = filepath.Join(t.TempDir(), "am")
	if filepath.Base(c.DBPath()) != "memory.db" {
		t.Fatalf("DBPath = %q", c.DBPath())
	}
	if filepath.Base(ConfigPath("/fake/home")) != "config.toml" {
		t.Fatalf("ConfigPath = %q", ConfigPath("/fake/home"))
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/config/ -v`
Expected: FAIL，`undefined: Default`

- [ ] **Step 3: 写 `internal/config/config.go`**

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"

	"github.com/muyi99/agent-memory/internal/agent/qoder"
)

const (
	dirName         = ".agent-memory"
	defaultInterval = 30 * time.Second
	defaultMemLimit = 20
)

type Config struct {
	DataDir string      `toml:"data_dir"`
	Qoder   QoderConfig `toml:"qoder"`
	Scan    ScanConfig  `toml:"scan"`
}

type QoderConfig struct {
	Disabled bool   `toml:"disabled"`
	Home     string `toml:"home"`
}

type ScanConfig struct {
	Interval       time.Duration `toml:"interval"`
	MemoryListSize int           `toml:"memory_list_size"`
}

// Default 以 userHome 为基准生成缺省值。把 home 作为参数传进来，
// 这样测试不需要改环境变量。
func Default(userHome string) Config {
	return Config{
		DataDir: filepath.Join(userHome, dirName),
		Qoder:   QoderConfig{},
		Scan:    ScanConfig{Interval: defaultInterval, MemoryListSize: defaultMemLimit},
	}
}

func ConfigPath(userHome string) string {
	return filepath.Join(userHome, dirName, "config.toml")
}

func (c Config) DBPath() string { return filepath.Join(c.DataDir, "memory.db") }

// QoderHome 返回 Qoder 配置根；显式配置优先，其次自动发现，禁用时返回空串。
func (c Config) QoderHome() string {
	if c.Qoder.Disabled {
		return ""
	}
	if c.Qoder.Home != "" {
		return c.Qoder.Home
	}
	base := c.userBaseForDiscovery()
	if home, ok := qoder.DiscoverHome(base); ok {
		return home
	}
	return ""
}

// userBaseForDiscovery: 发现用的基准目录。DataDir 是输出目录，不是 agent 的安装位置，
// 因此优先取 DataDir 的上一级（通常就是用户目录），取不到再回退用户主目录。
func (c Config) userBaseForDiscovery() string {
	if c.DataDir != "" {
		if parent := filepath.Dir(c.DataDir); parent != "" && parent != c.DataDir {
			if _, err := os.Stat(filepath.Join(parent, ".qoder-cn")); err == nil {
				return parent
			}
			if _, err := os.Stat(filepath.Join(parent, ".qoder")); err == nil {
				return parent
			}
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// Load 读配置；文件不存在时返回缺省值而不是错误——init 之前本来就没有配置文件。
// 未知键报错：拼错的键被静默忽略比报错更难查。
func Load(path, userHome string) (Config, error) {
	c := Default(userHome)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return c, nil
	}
	if err != nil {
		return c, fmt.Errorf("read config: %w", err)
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse config %s: %w", path, err)
	}
	if c.DataDir == "" {
		c.DataDir = Default(userHome).DataDir
	}
	if c.Scan.Interval <= 0 {
		c.Scan.Interval = defaultInterval
	}
	if c.Scan.MemoryListSize <= 0 {
		c.Scan.MemoryListSize = defaultMemLimit
	}
	return c, nil
}

// Save 写出配置文件。权限 0600：将来这里会放 LLM 端点等敏感项。
func Save(path string, c Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}
```

- [ ] **Step 4: 加依赖并跑测试**

Run: `go get github.com/BurntSushi/toml@v1.3.2 && go test ./internal/config/ -v`
Expected: PASS

`TestLoad_RejectsUnknownTopLevelKey` 一开始会失败是预期的：`BurntSushi/toml` 默认忽略未知键。把 `Load` 的解析换成严格模式，并在 import 块加 `"strings"`：

```go
	meta, err := toml.Decode(string(data), &c)
	if err != nil {
		return c, fmt.Errorf("parse config %s: %w", path, err)
	}
	if unknown := meta.Undecoded(); len(unknown) > 0 {
		keys := make([]string, 0, len(unknown))
		for _, k := range unknown {
			keys = append(keys, k.String())
		}
		return c, fmt.Errorf("config %s has unknown keys: %s", path, strings.Join(keys, ", "))
	}
```

（替换 `Load` 里原来那行 `toml.Unmarshal(data, &c)`。）

- [ ] **Step 5: 复跑全部测试**

Run: `go test ./...`
Expected: PASS

- [ ] **Step 6: 提交**

```bash
git add internal/config go.mod go.sum
git commit -m "feat: TOML 配置加载、缺省值与 Qoder 目录自动发现"
```

---

## Task 15: 命令行 init / scan / memory / doctor

**Files:**
- Create: `internal/cli/cli.go`
- Create: `internal/cli/init.go`
- Create: `internal/cli/scan.go`
- Create: `internal/cli/memory.go`
- Create: `internal/cli/doctor.go`
- Create: `internal/cli/integration_test.go`
- Modify: `internal/store/source.go`（追加 `ResetSources`）
- Modify: `internal/store/source_test.go`

- [ ] **Step 1: 在 `internal/store/source_test.go` 追加失败测试**

```go
func TestResetSources_ClearsHashGateForFullScan(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	run := time.Unix(1700000000, 0).Unix()
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "memory", Path: "/q/a.md", ContentHash: "h1"}, run); err != nil {
		t.Fatal(err)
	}
	if err := s.PutSource(ctx, domain.SourceState{Agent: "qoder", Kind: "session", Path: "/q/s.jsonl", ContentHash: "h2"}, run); err != nil {
		t.Fatal(err)
	}
	n, err := s.ResetSources(ctx, "qoder")
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("reset %d rows, want 2", n)
	}
	if _, ok, err := s.SourceHash(ctx, "qoder", "memory", "/q/a.md"); err != nil || ok {
		t.Fatalf("source row should be gone: ok=%v err=%v", ok, err)
	}
	// 记忆条目本身不受影响：full 扫描是重放，不是重置内容
	if _, _, err := s.UpsertMemory(ctx, 0, domain.MemoryDraft{Scope: domain.ScopeUser, Type: domain.TypeFact, Text: "x", SourceAgent: "qoder", SourcePath: "/q/a.md"}, time.Unix(run, 0)); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./internal/store/ -run TestResetSources -v`
Expected: FAIL，`s.ResetSources undefined`

- [ ] **Step 3: 在 `internal/store/source.go` 追加 `ResetSources`**

```go
// ResetSources 清空某个 agent 的全部扫描游标，用于 `scan --full`。
// 只删 source 行：记忆条目留在库里，重放时按哈希门控重新走一遍 upsert。
func (s *Store) ResetSources(ctx context.Context, agent string) (int, error) {
	q := `DELETE FROM source`
	args := []any{}
	if agent != "" {
		q += ` WHERE agent = ?`
		args = append(args, agent)
	}
	res, err := s.db.ExecContext(ctx, q, args...)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	return int(n), err
}
```

- [ ] **Step 4: 写 `internal/cli/cli.go`**

```go
package cli

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/muyi99/agent-memory/internal/agent"
	"github.com/muyi99/agent-memory/internal/agent/qoder"
	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

type env struct {
	cfg      config.Config
	userHome string
	st       *store.Store
}

// openEnv 读配置并打开共享库。每个子命令都从这里开始。
func openEnv() (*env, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("cannot resolve user home: %w", err)
	}
	cfg, err := config.Load(config.ConfigPath(home), home)
	if err != nil {
		return nil, err
	}
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return nil, err
	}
	return &env{cfg: cfg, userHome: home, st: st}, nil
}

// buildAdapters 把配置翻译成启用的适配器列表。
// 新增 agent 时只改这里：其余层都通过 agent.Adapter 接口工作。
func buildAdapters(cfg config.Config) []agent.Adapter {
	var out []agent.Adapter
	if home := cfg.QoderHome(); home != "" {
		out = append(out, qoder.New(home))
	}
	return out
}

// resolveProjectRef 接受项目路径或 project key，返回 project_id。
// 空串表示不限定项目（只查用户级）。
func resolveProjectRef(ctx context.Context, st *store.Store, ref string) (int64, error) {
	if ref == "" {
		return 0, nil
	}
	if id, ok, err := st.ResolveProjectByPath(ctx, ref); err != nil {
		return 0, err
	} else if ok {
		return id, nil
	}
	if p, ok, err := st.ProjectByKey(ctx, ref); err != nil {
		return 0, err
	} else if ok {
		return p.ID, nil
	}
	return 0, fmt.Errorf("unknown project %q: run `agent-memory scan` first", ref)
}

func reportError(w io.Writer, err error) int {
	fmt.Fprintf(w, "error: %v\n", err)
	return 1
}

// formatMemoryLine 是 search 与 list 共用的单行格式。
func formatMemoryLine(scope domain.Scope, typ domain.MemoryType, text string) string {
	return fmt.Sprintf("[%s/%s] %s", scope, typ, text)
}
```

- [ ] **Step 5: 写 `internal/cli/init.go`**

```go
package cli

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/muyi99/agent-memory/internal/config"
	"github.com/muyi99/agent-memory/internal/store"
)

func init() {
	register(command{name: "init", desc: "创建数据目录与配置文件", run: cmdInit})
}

func cmdInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "数据目录，默认 <用户目录>/.agent-memory")
	force := fs.Bool("force", false, "覆盖已存在的配置文件")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return reportError(stderr, err)
	}
	cfgPath := config.ConfigPath(home)
	cfg, err := config.Load(cfgPath, home)
	if err != nil {
		return reportError(stderr, err)
	}
	if *dataDir != "" {
		cfg.DataDir = *dataDir
	}

	if _, statErr := os.Stat(cfgPath); statErr == nil && !*force {
		fmt.Fprintf(stdout, "配置文件已存在，保持不变: %s（--force 覆盖）\n", cfgPath)
	} else if err := config.Save(cfgPath, cfg); err != nil {
		return reportError(stderr, err)
	}

	// 打开一次即可建表；后续子命令复用同一个库
	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return reportError(stderr, err)
	}
	defer st.Close()

	fmt.Fprintf(stdout, "数据目录: %s\n", cfg.DataDir)
	fmt.Fprintf(stdout, "共享记忆库: %s\n", cfg.DBPath())
	if q := cfg.QoderHome(); q != "" {
		fmt.Fprintf(stdout, "发现 Qoder: %s\n", q)
	} else {
		fmt.Fprintln(stdout, "未发现 Qoder 目录，可在 config.toml 的 [qoder] home 里指定")
	}
	fmt.Fprintln(stdout, "下一步: agent-memory scan")
	return 0
}
```

- [ ] **Step 6: 写 `internal/cli/scan.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/muyi99/agent-memory/internal/ingest"
)

func init() {
	register(command{name: "scan", desc: "扫描 agent 记忆文件并导入共享库", run: cmdScan})
}

func cmdScan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("scan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	agentFilter := fs.String("agent", "", "只扫描指定 agent")
	full := fs.Bool("full", false, "忽略内容哈希，全部重新解析")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	adapters := buildAdapters(e.cfg)
	if len(adapters) == 0 {
		fmt.Fprintln(stderr, "error: 没有可用的 agent，检查 config.toml")
		return 1
	}
	names := make([]string, 0, len(adapters))
	for _, a := range adapters {
		names = append(names, a.Name())
	}
	if err := ingest.ValidateAgent(names, *agentFilter); err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}

	ctx := context.Background()
	if *full {
		n, err := e.st.ResetSources(ctx, *agentFilter)
		if err != nil {
			return reportError(stderr, err)
		}
		fmt.Fprintf(stdout, "已重置 %d 个扫描游标\n", n)
	}

	r := ingest.New(e.st, adapters...)
	res, runErr := r.Run(ctx, *agentFilter)
	fmt.Fprintf(stdout, "scan: %s\n", res.String())
	for _, msg := range res.Errors {
		fmt.Fprintf(stdout, "  ! %s\n", msg)
	}
	if runErr != nil {
		return reportError(stderr, runErr)
	}
	return 0
}
```

- [ ] **Step 7: 写 `internal/cli/memory.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/muyi99/agent-memory/internal/domain"
	"github.com/muyi99/agent-memory/internal/store"
)

func init() {
	register(command{name: "memory", desc: "检索/写入共享记忆: search|add|list|purge", run: cmdMemory})
}

func cmdMemory(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: agent-memory memory <search|add|list|purge> [args]")
		return 2
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "search":
		return memorySearch(rest, stdout, stderr)
	case "add":
		return memoryAdd(rest, stdout, stderr)
	case "list":
		return memoryList(rest, stdout, stderr)
	case "purge":
		return memoryPurge(rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown memory subcommand %q\n", sub)
		return 2
	}
}

func memorySearch(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory search", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectRef := fs.String("project", "", "限定项目（路径或 project key）；用户级记忆始终参与")
	typ := fs.String("type", "", "限定记忆类型")
	limit := fs.Int("limit", 20, "返回条数")
	includeStale := fs.Bool("include-stale", false, "包含已过时条目")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	query := strings.Join(fs.Args(), " ")
	if query == "" {
		fmt.Fprintln(stderr, "usage: agent-memory memory search <关键词>")
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()

	ctx := context.Background()
	projectID, err := resolveProjectRef(ctx, e.st, *projectRef)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if *typ != "" && !domain.ValidMemoryType(domain.MemoryType(*typ)) {
		fmt.Fprintf(stderr, "error: unknown type %q\n", *typ)
		return 2
	}

	hits, err := store.NewFTS(e.st).Search(ctx, domain.SearchQuery{
		Text:         query,
		ProjectID:    projectID,
		Type:         domain.MemoryType(*typ),
		IncludeStale: *includeStale,
		Limit:        *limit,
	})
	if err != nil {
		return reportError(stderr, err)
	}
	if len(hits) == 0 {
		fmt.Fprintf(stdout, "无命中: %s\n", query)
		return 0
	}
	for _, h := range hits {
		fmt.Fprintf(stdout, "%.3f  %s\n", h.Score, formatMemoryLine(h.Memory.Scope, h.Memory.Type, h.Memory.Text))
		for _, src := range h.Memory.Sources {
			fmt.Fprintf(stdout, "        来源 %s: %s\n", src.Agent, src.Path)
		}
		fmt.Fprintf(stdout, "        最后见到 %s\n", h.Memory.LastSeenAt.Format(time.RFC3339))
	}
	return 0
}

func memoryAdd(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory add", flag.ContinueOnError)
	fs.SetOutput(stderr)
	typ := fs.String("type", string(domain.TypeFact), "记忆类型")
	scope := fs.String("scope", string(domain.ScopeUser), "user 或 project")
	projectRef := fs.String("project", "", "scope=project 时必填：项目绝对路径")
	agentName := fs.String("agent", "cli", "写入方标识，MCP 端会传 mcp")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	text := strings.Join(fs.Args(), " ")
	if strings.TrimSpace(text) == "" {
		fmt.Fprintln(stderr, "usage: agent-memory memory add <文本> [--type T] [--scope S] [--project P]")
		return 2
	}
	if !domain.ValidMemoryType(domain.MemoryType(*typ)) {
		fmt.Fprintf(stderr, "error: unknown type %q\n", *typ)
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	var projectID int64
	switch domain.Scope(*scope) {
	case domain.ScopeUser:
		projectID = 0
	case domain.ScopeProject:
		if *projectRef == "" {
			fmt.Fprintln(stderr, "error: --scope project 需要 --project <绝对路径>")
			return 2
		}
		if projectID, err = e.st.ProjectIDForPath(ctx, *agentName, "", *projectRef, "", time.Now()); err != nil {
			return reportError(stderr, err)
		}
	default:
		fmt.Fprintf(stderr, "error: unknown scope %q\n", *scope)
		return 2
	}

	draft := domain.MemoryDraft{
		Scope:       domain.Scope(*scope),
		Type:        domain.MemoryType(*typ),
		Text:        text,
		SourceAgent: *agentName,
		SourcePath:  "cli",
		Quote:       text,
	}
	id, created, err := e.st.UpsertMemory(ctx, projectID, draft, time.Now())
	if err != nil {
		return reportError(stderr, err)
	}
	word := "合并到已有条目"
	if created {
		word = "新建"
	}
	fmt.Fprintf(stdout, "memory#%d %s (%s/%s)\n", id, word, draft.Scope, draft.Type)
	return 0
}

func memoryList(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory list", flag.ContinueOnError)
	fs.SetOutput(stderr)
	projectRef := fs.String("project", "", "项目路径或 key；空则只列用户级")
	limit := fs.Int("limit", 50, "返回条数")
	includeStale := fs.Bool("include-stale", false, "包含已过时条目")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	projectID, err := resolveProjectRef(ctx, e.st, *projectRef)
	if err != nil {
		fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	items, err := e.st.ListMemories(ctx, projectID, *includeStale, *limit)
	if err != nil {
		return reportError(stderr, err)
	}
	for _, m := range items {
		flag := ""
		if m.Stale {
			flag = " (stale)"
		}
		agents := make([]string, 0, len(m.Sources))
		for _, s := range m.Sources {
			agents = append(agents, s.Agent)
		}
		fmt.Fprintf(stdout, "#%-4d %-8s %-11s %-40s [%s]%s\n",
			m.ID, m.Scope, m.Type, clip(m.Text, 40), strings.Join(agents, ","), flag)
	}
	fmt.Fprintf(stdout, "共 %d 条\n", len(items))
	return 0
}

func memoryPurge(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("memory purge", flag.ContinueOnError)
	fs.SetOutput(stderr)
	yes := fs.Bool("yes", false, "确认删除全部过时条目")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	n, err := e.st.QueryInt(ctx, `SELECT count(*) FROM memory_item WHERE stale = 1`)
	if err != nil {
		return reportError(stderr, err)
	}
	if !*yes {
		fmt.Fprintf(stdout, "有过时条目 %d 条，加 --yes 才会真删\n", n)
		return 0
	}
	deleted, err := e.st.PurgeStale(ctx)
	if err != nil {
		return reportError(stderr, err)
	}
	fmt.Fprintf(stdout, "已删除 %d 条过时记忆\n", deleted)
	return 0
}

func clip(s string, n int) string {
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
```

- [ ] **Step 8: 写 `internal/cli/doctor.go`**

```go
package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"time"
)

func init() {
	register(command{name: "doctor", desc: "库状态、agent 发现结果、写入目标与失败源清单", run: cmdDoctor})
}

func cmdDoctor(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	changedSince := fs.String("since", "720h", "列出多久内变更过的源文件，如 24h / 720h")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	e, err := openEnv()
	if err != nil {
		return reportError(stderr, err)
	}
	defer e.st.Close()
	ctx := context.Background()

	stats, err := e.st.Stats(ctx)
	if err != nil {
		return reportError(stderr, err)
	}

	fmt.Fprintf(stdout, "共享记忆库: %s\n", e.st.Path())
	fmt.Fprintf(stdout, "项目 %d，记忆 %d（过时 %d），源 %d（失败 %d，有变更记录 %d）\n",
		stats.Projects, stats.Memories, stats.Stale, stats.Sources, stats.FailedSource, stats.Changed)
	fmt.Fprintf(stdout, "按 agent 的记忆条数: %v\n", stats.MemoriesByAgent)
	fmt.Fprintf(stdout, "按类型的条数: %v\n", stats.MemoriesByType)

	fmt.Fprintf(stdout, "\n写入目标（本程序只会改这几处）:\n")
	fmt.Fprintf(stdout, "  %s\n", e.cfg.DataDir)
	fmt.Fprintf(stdout, "  %s\n", e.cfg.DBPath())
	fmt.Fprintf(stdout, "  项目内 .agent-memory/ 与 link 装过的区块（M2/M3 起）\n")

	fmt.Fprintf(stdout, "\nagent 发现结果:\n")
	if home := e.cfg.QoderHome(); home != "" {
		fmt.Fprintf(stdout, "  qoder: %s\n", home)
	} else {
		fmt.Fprintln(stdout, "  qoder: 未发现（可在 config.toml 指定 [qoder] home）")
	}

	d, err := time.ParseDuration(*changedSince)
	if err != nil {
		fmt.Fprintf(stderr, "error: bad --since %q\n", *changedSince)
		return 2
	}
	changed, err := e.st.ChangedSources(ctx, time.Now().Add(-d).Unix())
	if err != nil {
		return reportError(stderr, err)
	}
	if len(changed) > 0 {
		fmt.Fprintf(stdout, "\n最近变更的源文件（归属需与 agent 自己的编辑记录比对）:\n")
		for _, c := range changed {
			fmt.Fprintf(stdout, "  %s %s/%s @ %s\n", c.Kind, c.Agent, c.Path, c.ChangedAt.Format(time.RFC3339))
		}
	}

	errs, err := e.st.SourceErrors(ctx)
	if err != nil {
		return reportError(stderr, err)
	}
	if len(errs) > 0 {
		fmt.Fprintf(stdout, "\n解析失败的源:\n")
		for _, c := range errs {
			fmt.Fprintf(stdout, "  %s/%s: %s\n", c.Agent, c.Path, c.ChangedAt.Format(time.RFC3339))
		}
	}

	var unmerged []string
	unmerged, err = e.st.QueryStrings(ctx, `SELECT key FROM project WHERE key LIKE '%:%' ORDER BY key LIMIT 20`)
	if err != nil {
		return reportError(stderr, err)
	}
	if len(unmerged) > 0 {
		fmt.Fprintf(stdout, "\n未合并项目（路径只能靠 slug 猜，保持独立）:\n")
		for _, k := range unmerged {
			fmt.Fprintf(stdout, "  %s\n", k)
		}
	}

	var needFull int
	needFull, _ = e.st.QueryInt(ctx, `SELECT count(*) FROM source WHERE kind = 'memory' AND last_error <> ''`)
	if needFull > 0 {
		fmt.Fprintf(stdout, "\n提示: %d 个源上次解析失败，修好后跑 agent-memory scan --full\n", needFull)
	}
	return 0
}
```

- [ ] **Step 9: 写集成测试 `internal/cli/integration_test.go`**

```go
package cli

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// isolateHome 把用户主目录指向临时目录，并在那里放一个假的 Qoder home。
// Windows 下 os.UserHomeDir 读 USERPROFILE，Linux/macOS 读 HOME，两个都设。
func isolateHome(t *testing.T) (home, qoderHome string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	qoderHome = filepath.Join(home, ".qoder-cn")
	mustWrite(t, filepath.Join(qoderHome, "memory", "user.md"),
		"---\nname: user\ndescription: 画像\nmetadata:\n  type: user\n---\n\n沟通用中文，提交信息也用中文。\n")
	mustWrite(t, filepath.Join(qoderHome, "memory", "MEMORY.md"), "- [画像](user.md) — 索引行\n")

	repo := filepath.Join(home, "code", "demo")
	mustMkdir(t, repo)
	slug := "demo"
	mustWrite(t, filepath.Join(qoderHome, "projects", slug, "s.jsonl"),
		fmt.Sprintf("{\"type\":\"workspace-directories\",\"directories\":[%q]}\n", repo))
	mustWrite(t, filepath.Join(qoderHome, "projects", slug, "memory", "state.md"),
		"---\nmetadata:\n  type: project\n---\n\n余额扣减需乐观锁。\n")
	return home, qoderHome
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func run(t *testing.T, out *bytes.Buffer, args ...string) int {
	t.Helper()
	code := Run(args, out, out)
	t.Logf("agent-memory %s -> %d\n%s", strings.Join(args, " "), code, out.String())
	return code
}

func TestEndToEnd_InitScanSearchAddDoctor(t *testing.T) {
	home, _ := isolateHome(t)
	var out bytes.Buffer

	if code := run(t, &out, "init"); code != 0 {
		t.Fatalf("init exit %d", code)
	}
	cfgPath := filepath.Join(home, ".agent-memory", "config.toml")
	dbPath := filepath.Join(home, ".agent-memory", "memory.db")
	for _, p := range []string{cfgPath, dbPath} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("%s not created: %v", p, err)
		}
	}

	out.Reset()
	if code := run(t, &out, "scan"); code != 0 {
		t.Fatalf("scan exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "created=2") {
		t.Fatalf("want created=2 (MEMORY.md 索引被跳过), got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "search", "偏好"); code != 0 {
		t.Fatalf("search exit %d", code)
	}
	if !strings.Contains(out.String(), "沟通用中文") {
		t.Fatalf("中文两字词搜不到: %q", out.String())
	}
	if !strings.Contains(out.String(), "qoder:") {
		t.Fatalf("来源 agent 未展示: %q", out.String())
	}

	out.Reset()
	run(t, &out, "scan")
	if !strings.Contains(out.String(), "unchanged=2") {
		t.Fatalf("第二次扫描应全部 unchanged, got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "add", "接口返回字段一律蛇形命名", "--type", "convention"); code != 0 {
		t.Fatalf("add exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "新建") {
		t.Fatalf("want 新建, got %q", out.String())
	}

	out.Reset()
	run(t, &out, "memory", "add", "接口返回字段一律蛇形命名", "--type", "convention")
	if !strings.Contains(out.String(), "合并到已有条目") {
		t.Fatalf("same text should merge, got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "memory", "list"); code != 0 {
		t.Fatalf("list exit %d", code)
	}
	if !strings.Contains(out.String(), "共 3 条") {
		t.Fatalf("want 共 3 条, got %q", out.String())
	}

	out.Reset()
	if code := run(t, &out, "doctor"); code != 0 {
		t.Fatalf("doctor exit %d", code)
	}
	for _, want := range []string{"memory.db", "qoder:", "写入目标"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("doctor missing %q:\n%s", want, out.String())
		}
	}
}

func TestMemorySearch_ProjectScopeRequiresKnownProject(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "search", "中文", "--project", `Z:\nope`); code != 1 {
		t.Fatalf("unknown project should exit 1, got %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "unknown project") {
		t.Fatalf("want explanation, got %q", out.String())
	}
}

func TestScanRejectsUnknownAgent(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "scan", "--agent", "cursor"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
	if !strings.Contains(out.String(), "cursor") {
		t.Fatalf("want agent name in error, got %q", out.String())
	}
}

func TestMemoryAddRejectsBadType(t *testing.T) {
	isolateHome(t)
	var out bytes.Buffer
	if code := run(t, &out, "memory", "add", "x", "--type", "vibes"); code != 2 {
		t.Fatalf("want exit 2, got %d", code)
	}
}
```

- [ ] **Step 10: 跑全部测试**

Run: `go test ./... -v 2>&1 | tail -40`
Expected: 全部 PASS

- [ ] **Step 11: 手工冒烟**

Run: `go run ./cmd/agent-memory help`
Expected: 列出 init / memory / scan / version 四个命令

Run: `go run ./cmd/agent-memory memory`
Expected: 打印 usage，退出码 2（`echo $?` 验证）

- [ ] **Step 12: 提交**

```bash
git add internal/cli internal/store/source.go internal/store/source_test.go
git commit -m "feat: init/scan/memory/doctor 命令行与端到端集成测试"
```

---

## Task 16: 格式与静态检查、真实数据验收、README

**Files:**
- Create: `README.md`
- Modify: `.gitignore`

- [ ] **Step 1: 静态检查**

Run: `gofmt -l . && go vet ./...`
Expected: 两条都无输出。若 `gofmt -l` 列出文件，跑 `gofmt -w .` 后复跑测试。

- [ ] **Step 2: 全量测试与构建**

Run: `go test ./... && go build ./cmd/agent-memory && echo BUILD_OK`
Expected: PASS + `BUILD_OK`

- [ ] **Step 3: 真实数据只读验收**

本步骤会读你机器上真实的 `~/.qoder-cn`，输出只写到项目内的 `.am-local/`（下一步加进 .gitignore）。

先记录被读目录的哈希基线：

```bash
find ~/.qoder-cn/memory ~/.qoder-cn/projects/*/memory -name '*.md' -print0 2>/dev/null | xargs -0 md5sum > /tmp/am-before.txt
wc -l /tmp/am-before.txt
```

再跑：

```bash
go run ./cmd/agent-memory init --data-dir "$(pwd)/.am-local" --force
go run ./cmd/agent-memory scan
go run ./cmd/agent-memory scan
go run ./cmd/agent-memory memory search 偏好
go run ./cmd/agent-memory memory search 命名 --limit 5
go run ./cmd/agent-memory doctor
```

期望：第一次 `scan` 输出 `created>=10`；第二次输出 `unchanged=<与第一次 units 相同的数>` 且没有 `staled` 行；`memory search 偏好` 至少命中一条来自 `~/.qoder-cn/memory/` 的条目；`doctor` 里 `未发现` 只允许出现在未配置的 agent 上。

- [ ] **Step 4: 证明没改动原生文件**

```bash
find ~/.qoder-cn/memory ~/.qoder-cn/projects/*/memory -name '*.md' -print0 2>/dev/null | xargs -0 md5sum > /tmp/am-after.txt
diff /tmp/am-before.txt /tmp/am-after.txt && echo NATIVE_UNTOUCHED
```

Expected: `NATIVE_UNTOUCHED`。这一步与 `doctor` 的写入目标清单共同构成验收标准 3：哈希相同证明「没被改」，写入目标清单封闭证明「无处可改」。

- [ ] **Step 5: 加 .gitignore 条目**

```
/.am-local/
```

- [ ] **Step 6: 写 `README.md`**

```markdown
# agent-memory

让 Qoder、Trae、WorkBuddy、OpenCode 共用一份记忆。切到另一个 agent 时不必从头描述任务。

设计文档见 `docs/superpowers/specs/`，实施计划见 `docs/superpowers/plans/`。

## 现在能做什么（M1）

- 读取 Qoder 的用户级与项目级记忆（只读，不改你的 agent 文件）
- 归并去重后存进一个共享 SQLite 库，中文两字词也能检索
- 命令行查、写、列、清理

## 安装

    go build ./cmd/agent-memory
    ./agent-memory init

## 用法

    agent-memory scan                     # 扫描导入，重复执行是幂等的
    agent-memory memory search 偏好        # 中文检索
    agent-memory memory add "..." --type convention
    agent-memory memory list --project /abs/path
    agent-memory memory purge --yes        # 删除已过时条目
    agent-memory doctor                    # 库状态、agent 发现结果、写入目标清单

## 它不会做什么

- 不修改任何 agent 的原生记忆文件
- 不保存完整对话内容
- 不自动裁决互相冲突的记忆

## 数据位置

- 配置：`~/.agent-memory/config.toml`
- 记忆库：`~/.agent-memory/memory.db`
- 检索：FTS5 + 自切分中文二元组；`trigram` 对两字中文词召回为 0，所以没用
```

- [ ] **Step 7: 提交**

```bash
git add README.md .gitignore
git commit -m "docs: 使用说明确认只读边界与命令用法"
```

---

## 完成标准（M1）

全部满足才算 M1 结束：

1. `go test ./...` 与 `go vet ./...` 干净。
2. 第二次 `scan` 输出 `unchanged=N` 且 `created=0 merged=0`，库内容 dump 不变。
3. `memory search 偏好` 与 `memory search 命名` 在假目录测试和真实数据上都命中。
4. Task 16 Step 4 打印 `NATIVE_UNTOUCHED`。
5. 低置信 slug 项目独立存在，`doctor` 列出它们。
