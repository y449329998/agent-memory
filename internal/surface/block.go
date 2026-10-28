package surface

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// InstallBlock 在 markdown 文件里安装或更新一段受管区块。
// 文件不存在时创建；存在时只在标记之间改动，标记外的字节原样保留。
func InstallBlock(path, begin, end, content string) (changed bool, err error) {
	block := begin + "\n" + strings.TrimRight(content, "\n") + "\n" + end + "\n"
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return false, err
		}
		return true, os.WriteFile(path, []byte(block), 0o644)
	}
	if err != nil {
		return false, err
	}

	existing := string(data)
	start, stop, ok, err := locateBlock(existing, begin, end)
	if err != nil {
		return false, fmt.Errorf("%s: %w; fix it manually", path, err)
	}
	if !ok {
		out := existing
		if out != "" && !strings.HasSuffix(out, "\n") {
			out += "\n"
		}
		out += "\n" + block
		return true, os.WriteFile(path, []byte(out), 0o644)
	}

	// 替换区间连同 end 标记后那一行换行一起吃掉，否则每次重装都会多留一个空行，
	// 相同内容也就永远判不出"未变化"。
	if stop < len(existing) && existing[stop] == '\n' {
		stop++
	}
	replaced := existing[:start] + block + existing[stop:]
	if replaced == existing {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(replaced), 0o644)
}

// RemoveBlock 精确删掉自己写入的区块，并尽量还原原始字节：
// 标记前后由我们插入的空行也一并清掉。
func RemoveBlock(path, begin, end string) (changed bool, err error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	existing := string(data)
	start, stop, ok, err := locateBlock(existing, begin, end)
	if err != nil {
		return false, fmt.Errorf("%s: %w; fix it manually", path, err)
	}
	if !ok {
		return false, nil
	}
	for stop < len(existing) && existing[stop] == '\n' {
		stop++
	}
	head := strings.TrimRight(existing[:start], "\n")
	tail := existing[stop:]
	var out string
	switch {
	case head == "":
		out = strings.TrimLeft(tail, "\n")
	case tail == "":
		out = head + "\n"
	default:
		out = head + "\n" + strings.TrimLeft(tail, "\n")
	}
	if out == existing {
		return false, nil
	}
	return true, os.WriteFile(path, []byte(out), 0o644)
}

// locateBlock 定位 begin/end 之间的受管区块。
// 标记缺失时 ok=false；不成对、重复或顺序颠倒时返回错误——这种文件按猜测重写
// 会连同用户的正文一起吃掉，只能让人手工修。
func locateBlock(text, begin, end string) (start, stop int, ok bool, err error) {
	beginCount := strings.Count(text, begin)
	endCount := strings.Count(text, end)
	if beginCount == 0 && endCount == 0 {
		return 0, 0, false, nil
	}
	if beginCount != 1 || endCount != 1 {
		return 0, 0, false, fmt.Errorf("found %d begin and %d end markers, want exactly one of each", beginCount, endCount)
	}
	start = strings.Index(text, begin)
	endIdx := strings.Index(text, end)
	if endIdx < start {
		return 0, 0, false, fmt.Errorf("end marker appears before begin marker")
	}
	return start, endIdx + len(end), true, nil
}

// BlockExists 供 doctor 检查区块是否还在（用户可能手动删了）。
func BlockExists(path, begin string) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return bytes.Contains(data, []byte(begin))
}
