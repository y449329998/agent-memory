package surface

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLinkTargetsCoverConfirmedAgents(t *testing.T) {
	got := LinkTargets()
	want := map[string]bool{"qoder": true, "opencode": true, "claude": true}
	if len(got) != len(want) {
		t.Fatalf("targets = %v, want exactly the three confirmed formats", got)
	}
	for _, name := range got {
		if !want[name] {
			t.Fatalf("unexpected target %q: trae and workbuddy config formats are not confirmed", name)
		}
	}
}

func TestPlanLink_QoderEntryShape(t *testing.T) {
	plan, err := PlanLink("qoder", LinkOptions{Binary: `C:\bin\agent-memory.exe`, Token: "tok", Port: 47111})
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(plan.MCPEntry)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(body, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["command"] != `C:\bin\agent-memory.exe` {
		t.Fatalf("command = %v", doc["command"])
	}
	args, _ := doc["args"].([]any)
	if len(args) != 3 || args[0] != "mcp" || args[1] != "--port" || args[2] != "47111" {
		t.Fatalf("args = %v, want [mcp --port 47111]", args)
	}
	if doc["env"] == nil {
		t.Fatal("token must be passed via env, not the command line (it would land in shell history and ps output)")
	}
	env := doc["env"].(map[string]any)
	if env["AGENT_MEMORY_TOKEN"] != "tok" {
		t.Fatalf("env = %v", env)
	}
}

func TestPlanLink_OpenCodeUsesLocalTypeAndCommandArray(t *testing.T) {
	plan, err := PlanLink("opencode", LinkOptions{Binary: "/usr/local/bin/agent-memory", Token: "tok", Port: 47111})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPRoot != "mcp" {
		t.Fatalf("opencode root key = %q, want mcp", plan.MCPRoot)
	}
	if plan.MCPEntry["type"] != "local" {
		t.Fatalf("opencode entry needs type=local, got %v", plan.MCPEntry["type"])
	}
	cmd, ok := plan.MCPEntry["command"].([]string)
	if !ok || len(cmd) != 4 || cmd[0] != "/usr/local/bin/agent-memory" || cmd[1] != "mcp" || cmd[2] != "--port" || cmd[3] != "47111" {
		t.Fatalf("opencode command should be the full argv [binary mcp --port 47111], got %v", plan.MCPEntry["command"])
	}
	if _, hasArgs := plan.MCPEntry["args"]; hasArgs {
		t.Fatalf("opencode entry must not use the qoder-style args key: %v", plan.MCPEntry)
	}
}

func TestPlanLink_RejectsUnknownTarget(t *testing.T) {
	if _, err := PlanLink("trae", LinkOptions{Binary: "x", Token: "t", Port: 1}); err == nil {
		t.Fatal("trae must be refused until its config format is confirmed")
	}
}

func TestBlockContentMentionsHandoffAndProjectKey(t *testing.T) {
	body := BlockContent("f:/go/demo")
	for _, want := range []string{".agent-memory/HANDOFF.md", "f:/go/demo"} {
		if !strings.Contains(body, want) {
			t.Fatalf("block content missing %q:\n%s", want, body)
		}
	}
	if len([]rune(body)) > 600 {
		t.Fatalf("block content should stay a pointer, not the whole brief: %d runes", len([]rune(body)))
	}
}

func TestLinkWritesAndUnlinkRestores(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	project := filepath.Join(dir, "repo")
	for _, d := range []string{home, project} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mcpPath := filepath.Join(home, ".qoder-cn", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}

	opts := LinkOptions{
		Target: "qoder", Home: home, ProjectDir: project,
		Binary: "agent-memory", Token: "tok1234567890tok1234567890", Port: 47111,
	}
	plan, err := PlanLink(opts.Target, opts)
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPPath != mcpPath {
		t.Fatalf("plan mcp path = %q want %q", plan.MCPPath, mcpPath)
	}
	report, err := ApplyLink(plan, opts)
	if err != nil {
		t.Fatal(err)
	}
	if !report.MCPChanged || report.Backup == "" {
		t.Fatalf("link report wrong: %+v", report)
	}
	if !report.BlockAdded {
		t.Fatalf("block should be installed: %+v", report)
	}
	if _, err := os.Stat(report.Backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}

	data, _ := os.ReadFile(mcpPath)
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["mcpServers"].(map[string]any)["agent-memory"] == nil {
		t.Fatalf("not linked: %s", data)
	}
	blockPath := filepath.Join(project, "AGENTS.md")
	blockBody, err := os.ReadFile(blockPath)
	if err != nil {
		t.Fatalf("block file should be installed: %v", err)
	}
	if !strings.Contains(string(blockBody), BeginMarker) {
		t.Fatalf("marker missing: %s", blockBody)
	}

	undo, err := ApplyUnlink(opts)
	if err != nil {
		t.Fatal(err)
	}
	if !undo.MCPChanged {
		t.Fatalf("unlink should report a change: %+v", undo)
	}
	data, _ = os.ReadFile(mcpPath)
	doc = map[string]any{}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc["mcpServers"].(map[string]any)["agent-memory"] != nil {
		t.Fatalf("unlink left our entry behind: %s", data)
	}
	finalBlock, err := os.ReadFile(blockPath)
	if err == nil && strings.Contains(string(finalBlock), BeginMarker) {
		t.Fatalf("block marker still present after unlink: %s", finalBlock)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	dir := t.TempDir()
	home := filepath.Join(dir, "home")
	mcpPath := filepath.Join(home, ".qoder-cn", "mcp.json")
	if err := os.MkdirAll(filepath.Dir(mcpPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mcpPath, []byte(`{"mcpServers":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(mcpPath)

	plan, err := PlanLink("qoder", LinkOptions{Binary: "agent-memory", Token: "t0123456789t0123456789t0123456789", Port: 1})
	if err != nil {
		t.Fatal(err)
	}
	if plan.MCPRoot == "" {
		t.Fatal("plan should carry the root key so --dry-run can describe it")
	}
	after, _ := os.ReadFile(mcpPath)
	if string(before) != string(after) {
		t.Fatal("PlanLink must not write anything")
	}
}
