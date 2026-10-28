package ingest

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

type WatchConfig struct {
	Dirs        []string
	Debounce    time.Duration
	PollSeconds int
}

// Watcher 把「文件可能变了」压缩成「该扫一次了」。
//
// 两条触发路径并存：fsnotify 负责及时，固定轮询负责兜底——Windows 上的目录通知
// 会静默丢事件（进程被杀、网络盘、编辑器换名写入），只靠 fsnotify 会让人以为
// 记忆没更新。两条路径共用同一个去抖窗口，所以不会有重复扫描风暴。
type Watcher struct {
	cfg     WatchConfig
	handler func(context.Context)
	// lastRun 用于避免刚扫完又立刻被轮询触发一次空扫。
	lastRun time.Time
}

func NewWatcher(cfg WatchConfig) *Watcher {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 2 * time.Second
	}
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 30
	}
	return &Watcher{cfg: cfg}
}

func (w *Watcher) SetHandler(fn func(context.Context)) { w.handler = fn }

func (w *Watcher) Run(ctx context.Context) error {
	dw, err := fsnotify.NewWatcher()
	if err != nil {
		// 起不了监听不是致命错误：退化成纯轮询，功能仍在，只是慢一点
		fmt.Fprintf(os.Stderr, "watcher: fsnotify unavailable (%v), falling back to polling only\n", err)
		dw = nil
	} else {
		defer dw.Close()
		w.addExisting(dw)
	}

	debounce := time.NewTimer(w.cfg.Debounce)
	defer debounce.Stop()
	debounceActive := false

	poll := time.NewTicker(time.Duration(w.cfg.PollSeconds) * time.Second)
	defer poll.Stop()

	fire := func() {
		if w.handler == nil {
			return
		}
		w.lastRun = time.Now()
		w.handler(ctx)
	}
	requestScan := func() {
		if !debounceActive {
			debounceActive = true
			debounce.Reset(w.cfg.Debounce)
		}
	}

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-debounce.C:
			if !debounceActive {
				continue
			}
			debounceActive = false
			fire()
		case <-poll.C:
			if time.Since(w.lastRun) < w.cfg.Debounce {
				continue
			}
			requestScan()
		case ev, ok := <-w.events(dw):
			if !ok {
				continue
			}
			if ev.Op&(fsnotify.Create|fsnotify.Write|fsnotify.Remove|fsnotify.Rename) == 0 {
				continue
			}
			if ev.Op&fsnotify.Create != 0 && w.isDir(ev.Name) {
				w.addDir(dw, ev.Name)
			}
			requestScan()
		case err, ok := <-w.errors(dw):
			if !ok {
				continue
			}
			fmt.Fprintf(os.Stderr, "watcher: %v\n", err)
		}
	}
}

// events/errors 让 fsnotify 为 nil（监听不可用）时不会 panic：
// 返回一个永不就绪的 channel，select 会自然忽略这一路。
func (w *Watcher) events(dw *fsnotify.Watcher) <-chan fsnotify.Event {
	if dw == nil {
		return make(chan fsnotify.Event)
	}
	return dw.Events
}

func (w *Watcher) errors(dw *fsnotify.Watcher) <-chan error {
	if dw == nil {
		return make(chan error)
	}
	return dw.Errors
}

func (w *Watcher) addExisting(dw *fsnotify.Watcher) {
	for _, root := range w.cfg.Dirs {
		w.walkAdd(dw, root, 3)
	}
}

// walkAdd 递归加目录，深度受限：agent 目录下有大量无关子目录
// （缓存、node_modules、IndexedDB），全加进去会浪费句柄并放大事件量。
func (w *Watcher) walkAdd(dw *fsnotify.Watcher, root string, depth int) {
	if depth < 0 || !w.isDir(root) {
		return
	}
	w.addDir(dw, root)
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			w.walkAdd(dw, filepath.Join(root, e.Name()), depth-1)
		}
	}
}

func (w *Watcher) addDir(dw *fsnotify.Watcher, dir string) {
	if dw == nil {
		return
	}
	_ = dw.Add(dir) // 已在监视集里时返回错误，忽略即可
}

func (w *Watcher) isDir(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
