package daemon

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// LoadOrCreateToken 读取或首次生成访问令牌。
// 空文件是错误而不是「顺手重建」：静默换 token 会让已接入的 agent 全部失效，
// 而现场看起来一切正常。
func LoadOrCreateToken(path string) (string, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return createToken(path)
	}
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	token := strings.TrimSpace(string(data))
	if len(token) < 32 {
		return "", fmt.Errorf("token file %s is empty or too short; delete it and restart serve", path)
	}
	return token, nil
}

func createToken(path string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(token+"\n"), 0o600); err != nil {
		return "", err
	}
	return token, nil
}

func WritePort(path string, port int) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strconv.Itoa(port)+"\n"), 0o644)
}

func ReadPort(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("no running agent-memory serve? (%s missing: %v)", path, err)
	}
	port, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || port <= 0 || port > 65535 {
		return 0, fmt.Errorf("bad port file %s: %q", path, strings.TrimSpace(string(data)))
	}
	return port, nil
}
