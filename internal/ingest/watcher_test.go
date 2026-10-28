package ingest

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func countCalls(t *testing.T, w *Watcher, d time.Duration) int {
	t.Helper()
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go w.Run(ctx)

	time.Sleep(d)
	cancel()
	time.Sleep(50 * time.Millisecond)
	return int(atomic.LoadInt32(&calls))
}

func TestWatcher_FiresOnFileCreate(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{
		Dirs:        []string{dir},
		Debounce:    20 * time.Millisecond,
		PollSeconds: 3600, // 故意把轮询拉长，确认这条路径靠的是 fsnotify
	})

	var calls int32
	done := make(chan struct{})
	w.SetHandler(func(context.Context) {
		if atomic.AddInt32(&calls, 1) == 1 {
			close(done)
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)

	// 等 watcher 就绪再写文件，否则第一个事件可能落在注册之前
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatalf("fsnotify 没触发，calls=%d", atomic.LoadInt32(&calls))
	}
}

func TestWatcher_CoalescesBurstIntoOneScan(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{
		Dirs:        []string{dir},
		Debounce:    100 * time.Millisecond,
		PollSeconds: 3600,
	})
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	for i := 0; i < 12; i++ {
		name := filepath.Join(dir, "f"+string(rune('a'+i))+".md")
		if err := os.WriteFile(name, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(1500 * time.Millisecond)

	// 12 次写应该被去抖成很少几次：一次扫描的成本远大于一次事件
	if got := atomic.LoadInt32(&calls); got > 3 {
		t.Fatalf("debounce ineffective: %d scans for a 12-file burst", got)
	}
}

func TestWatcher_PollFiresWithoutFileEvents(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{
		Dirs:        []string{dir},
		Debounce:    10 * time.Millisecond,
		PollSeconds: 1,
	})
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(2500 * time.Millisecond)

	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("轮询兜底必须独立触发——Windows 上 fsnotify 会静默漏事件")
	}
}

func TestWatcher_PicksUpDirectoriesCreatedLater(t *testing.T) {
	root := t.TempDir()
	sub := filepath.Join(root, "projects", "newslug", "memory")
	w := NewWatcher(WatchConfig{
		Dirs:        []string{root},
		Debounce:    20 * time.Millisecond,
		PollSeconds: 1,
	})
	var calls int32
	w.SetHandler(func(context.Context) { atomic.AddInt32(&calls, 1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Run(ctx)
	time.Sleep(200 * time.Millisecond)

	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "m.md"), []byte("新目录里的记忆"), 0o644); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(4 * time.Second)
	for atomic.LoadInt32(&calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("运行中新建的目录应当被纳入监听")
	}
}

func TestWatcher_StopsWithContext(t *testing.T) {
	dir := t.TempDir()
	w := NewWatcher(WatchConfig{Dirs: []string{dir}, Debounce: 10 * time.Millisecond, PollSeconds: 1})
	w.SetHandler(func(context.Context) {})
	ctx, cancel := context.WithCancel(context.Background())
	returned := make(chan error, 1)
	go func() { returned <- w.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case err := <-returned:
		if err != nil {
			t.Fatalf("Run should exit cleanly on cancel, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestWatcher_BadDirIsNotFatal(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	w := NewWatcher(WatchConfig{Dirs: []string{missing, t.TempDir()}, Debounce: 10 * time.Millisecond, PollSeconds: 1})
	w.SetHandler(func(context.Context) {})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errs := make(chan error, 1)
	go func() { errs <- w.Run(ctx) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-errs; err != nil {
		t.Fatalf("一个不存在的目录不该让监听退出: %v", err)
	}
}
