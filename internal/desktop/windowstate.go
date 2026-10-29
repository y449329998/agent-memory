package desktop

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// windowState 是窗口的位置与大小；路径存在 <DataDir>/window.json。
type windowState struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

func windowStatePath(dataDir string) string { return filepath.Join(dataDir, "window.json") }

func loadWindowState(dataDir string) (windowState, bool) {
	data, err := os.ReadFile(windowStatePath(dataDir))
	if err != nil {
		return windowState{}, false
	}
	var st windowState
	if err := json.Unmarshal(data, &st); err != nil {
		return windowState{}, false
	}
	if st.W <= 0 || st.H <= 0 {
		return windowState{}, false
	}
	return st, true
}

func saveWindowState(dataDir string, st windowState) error {
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(windowStatePath(dataDir), data, 0o644)
}

// clampWindowState 把窗口限制在当前屏幕内；越界或过小则回到默认大小并居中。
func clampWindowState(st windowState, screenW, screenH, defaultW, defaultH int) windowState {
	if st.W < 800 || st.H < 600 ||
		st.X < -screenW || st.Y < -screenH ||
		st.X > screenW-200 || st.Y > screenH-200 ||
		st.W > screenW*2 || st.H > screenH*2 {
		return windowState{W: defaultW, H: defaultH, X: (screenW - defaultW) / 2, Y: (screenH - defaultH) / 2}
	}
	return st
}
