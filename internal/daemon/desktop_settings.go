package daemon

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// desktopAppPathFromFile 读桌面应用写下的 <DataDir>/desktop.json。
// daemon 不 import desktop 包（会成环），所以这里只读那一份约定好的 JSON。
func desktopAppPathFromFile(dataDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(dataDir, "desktop.json"))
	if err != nil {
		return "", false
	}
	var f struct {
		AppPath string `json:"app_path"`
	}
	if err := json.Unmarshal(data, &f); err != nil || f.AppPath == "" {
		return "", false
	}
	return f.AppPath, true
}
