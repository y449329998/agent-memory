//go:build !windows

package config

import (
	"os"
	"path/filepath"
)

func SaveLLMKey(path, key string) error {
	if key == "" {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(key), 0o600)
}

func LoadLLMKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", nil
	}
	return string(raw), err
}
