package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// desktopFile 让 serve 知道桌面应用在哪里，用于「开机自启」这类需要 exe 路径的操作。
type desktopFile struct {
	AppPath string `json:"app_path"`
}

func desktopFilePath(dataDir string) string { return filepath.Join(dataDir, "desktop.json") }

func writeDesktopFile(dataDir, appPath string) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, _ := json.Marshal(desktopFile{AppPath: appPath})
	return os.WriteFile(desktopFilePath(dataDir), data, 0o644)
}

func readDesktopFile(dataDir string) (desktopFile, bool) {
	data, err := os.ReadFile(desktopFilePath(dataDir))
	if err != nil {
		return desktopFile{}, false
	}
	var f desktopFile
	if err := json.Unmarshal(data, &f); err != nil || f.AppPath == "" {
		return desktopFile{}, false
	}
	return f, true
}
