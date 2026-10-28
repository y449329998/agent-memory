package surface

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"
)

// UpsertJSONKey 在 <path> 的 JSON 文档里把 doc[root][name] 设为 entry。
// 返回 changed=false 表示内容已相同，不写文件也不留备份——每次 link 都塞一个
// 备份文件，比不备份更烦人。
//
// 解析失败直接拒绝：宁可不动用户配置，也不猜一个结构覆盖掉它。
func UpsertJSONKey(path, root, name string, entry map[string]any) (changed bool, backup string, err error) {
	return patchJSONKey(path, root, name, entry, true)
}

// RemoveJSONKey 删掉 doc[root][name]，其余键一概不动。
func RemoveJSONKey(path, root, name string) (changed bool, backup string, err error) {
	return patchJSONKey(path, root, name, nil, false)
}

func patchJSONKey(path, root, name string, entry map[string]any, upsert bool) (changed bool, backup string, err error) {
	data, err := os.ReadFile(path)
	doc := map[string]any{}
	emptyInput := false
	if os.IsNotExist(err) {
		emptyInput = true
		if !upsert {
			return false, "", nil
		}
	} else if err != nil {
		return false, "", fmt.Errorf("read %s: %w", path, err)
	} else {
		if e := json.Unmarshal(data, &doc); e != nil {
			return false, "", fmt.Errorf("%s is not valid json; refusing to modify it: %w", path, e)
		}
		// 顶层是 JSON null 或别的非对象值时，写回只能靠猜，直接拒绝。
		if doc == nil {
			return false, "", fmt.Errorf("%s: top-level value is not a JSON object; refusing to modify it", path)
		}
	}

	rootValue, hasRoot := doc[root]
	if hasRoot {
		// 根键存在但类型不对（例如 mcpServers 被写成了数组）：拒绝，不静默替换
		if _, ok := rootValue.(map[string]any); !ok {
			return false, "", fmt.Errorf("%s: key %q is not an object; fix it manually", path, root)
		}
	}
	if !hasRoot {
		if !upsert {
			return false, "", nil
		}
		doc[root] = map[string]any{}
	}
	servers := doc[root].(map[string]any)

	if upsert {
		if existing, ok := servers[name]; ok && jsonEqual(existing, entry) {
			return false, "", nil
		}
		servers[name] = entry
	} else {
		if _, ok := servers[name]; !ok {
			return false, "", nil
		}
		delete(servers, name)
	}

	if !emptyInput {
		bak, err := backupFile(path)
		if err != nil {
			return false, "", err
		}
		backup = bak
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, backup, err
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return false, backup, err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return false, backup, err
	}
	return true, backup, nil
}

func jsonEqual(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return reflect.DeepEqual(a, b)
	}
	return string(ab) == string(bb)
}

// backupFile 备份原文件并保证不覆盖既有备份。
// 时间戳只到秒：同一秒内 link 后立刻 unlink 会命中同一个文件名，
// 那会把「原始配置」的备份覆盖成「已链接」的版本，正是备份要防的事。
func backupFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	stamp := time.Now().Format("20060102-150405")
	for i := 0; i < 100; i++ {
		target := fmt.Sprintf("%s.bak-%s", path, stamp)
		if i > 0 {
			target = fmt.Sprintf("%s.bak-%s-%d", path, stamp, i)
		}
		f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if os.IsExist(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if _, err := f.Write(data); err != nil {
			f.Close()
			return "", err
		}
		if err := f.Close(); err != nil {
			return "", err
		}
		return target, nil
	}
	return "", fmt.Errorf("too many backups for %s in one second", path)
}
