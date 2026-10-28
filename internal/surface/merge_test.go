package surface

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUpsertJSONKey_AddsUnderRootAndBacksUp(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte("{\n  \"mcpServers\": {\n    \"existing\": {\"command\": \"other\"}\n  }\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, backup, err := UpsertJSONKey(path, "mcpServers", "agent-memory", map[string]any{
		"command": "agent-memory", "args": []string{"mcp"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed=true")
	}
	if backup == "" {
		t.Fatal("want a backup path")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	data, _ := os.ReadFile(path)
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	servers := doc["mcpServers"].(map[string]any)
	if servers["existing"] == nil {
		t.Fatalf("existing server destroyed: %s", data)
	}
	if servers["agent-memory"] == nil {
		t.Fatalf("our server missing: %s", data)
	}
}

func TestUpsertJSONKey_IdempotentSecondCall(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	if err := os.WriteFile(path, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	entry := map[string]any{"command": "agent-memory", "args": []any{"mcp"}}

	changed1, _, err := UpsertJSONKey(path, "mcpServers", "agent-memory", entry)
	if err != nil {
		t.Fatal(err)
	}
	changed2, backup2, err := UpsertJSONKey(path, "mcpServers", "agent-memory", entry)
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("identical entry must not rewrite the file")
	}
	if backup2 != "" {
		t.Fatalf("no change means no backup, got %q", backup2)
	}
	if !changed1 {
		t.Fatal("first call should change")
	}

	entries, _ := os.ReadDir(filepath.Dir(path))
	var backups int
	for _, e := range entries {
		if strings.Contains(e.Name(), ".bak-") {
			backups++
		}
	}
	if backups != 1 {
		t.Fatalf("want exactly 1 backup, got %d", backups)
	}
}

func TestUpsertJSONKey_CreatesMissingFileWithRoot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "opencode.json")
	changed, _, err := UpsertJSONKey(path, "mcp", "agent-memory", map[string]any{"type": "local", "command": []any{"agent-memory", "mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("creating a file is a change")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["mcp"] == nil {
		t.Fatalf("root key missing: %s", data)
	}
}

func TestUpsertJSONKey_RefusesUnparseableFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.json")
	if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := UpsertJSONKey(path, "mcpServers", "agent-memory", map[string]any{}); err == nil {
		t.Fatal("refuse to touch a file we cannot parse — never guess-overwrite user config")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "{ not json" {
		t.Fatal("broken file was modified")
	}
}

func TestUpsertJSONKey_RefusesWrongRootType(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	original := `{"mcpServers":["not-an-object"]}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := UpsertJSONKey(path, "mcpServers", "agent-memory", map[string]any{}); err == nil {
		t.Fatal("a non-object root key must be refused, not replaced")
	}
	data, _ := os.ReadFile(path)
	if string(data) != original {
		t.Fatalf("file changed despite refusal: %s", data)
	}
}

func TestRemoveJSONKey_OnlyRemovesOurs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	doc := map[string]any{"mcpServers": map[string]any{
		"existing":     map[string]any{"command": "other"},
		"agent-memory": map[string]any{"command": "agent-memory"},
	}}
	body, _ := json.Marshal(doc)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	changed, _, err := RemoveJSONKey(path, "mcpServers", "agent-memory")
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("want changed")
	}
	data, _ := os.ReadFile(path)
	var after map[string]any
	json.Unmarshal(data, &after)
	servers := after["mcpServers"].(map[string]any)
	if _, ok := servers["agent-memory"]; ok {
		t.Fatal("our key still present")
	}
	if _, ok := servers["existing"]; !ok {
		t.Fatal("foreign key removed")
	}
	// 不存在时第二次是 no-op
	changed2, _, err := RemoveJSONKey(path, "mcpServers", "agent-memory")
	if err != nil {
		t.Fatal(err)
	}
	if changed2 {
		t.Fatal("second removal should be a no-op")
	}
}

func TestBackupFile_DoesNotOverwriteWithinSameSecond(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	original := `{"mcpServers":{"other":{"command":"original-server"}}}`
	if err := os.WriteFile(path, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := backupFile(path)
	if err != nil {
		t.Fatal(err)
	}

	// 模拟同一秒内 link 之后又 unlink：文件内容已变
	if err := os.WriteFile(path, []byte(`{"mcpServers":{"agent-memory":{}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := backupFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatalf("second backup overwrote the first: %s", first)
	}
	kept, err := os.ReadFile(first)
	if err != nil {
		t.Fatal(err)
	}
	if string(kept) != original {
		t.Fatalf("original backup was clobbered: %q", kept)
	}
}
