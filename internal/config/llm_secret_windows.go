//go:build windows

package config

import (
	"errors"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modcrypt32    = windows.NewLazySystemDLL("crypt32.dll")
	procProtect   = modcrypt32.NewProc("CryptProtectData")
	procUnprotect = modcrypt32.NewProc("CryptUnprotectData")
)

type dataBlob struct {
	cbData uint32
	pbData *byte
}

func blobFromBytes(b []byte) dataBlob {
	if len(b) == 0 {
		return dataBlob{}
	}
	return dataBlob{cbData: uint32(len(b)), pbData: &b[0]}
}

func (b dataBlob) bytes() []byte {
	if b.pbData == nil || b.cbData == 0 {
		return nil
	}
	out := make([]byte, b.cbData)
	copy(out, unsafe.Slice(b.pbData, b.cbData))
	return out
}

func SaveLLMKey(path, key string) error {
	if key == "" {
		_ = os.Remove(path)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	in := blobFromBytes([]byte(key))
	var out dataBlob
	ok, _, err := procProtect.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if ok == 0 {
		return err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return os.WriteFile(path, out.bytes(), 0o600)
}

func LoadLLMKey(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	in := blobFromBytes(raw)
	var out dataBlob
	ok, _, err := procUnprotect.Call(uintptr(unsafe.Pointer(&in)), 0, 0, 0, 0, 0, uintptr(unsafe.Pointer(&out)))
	if ok == 0 {
		return "", err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.pbData)))
	return string(out.bytes()), nil
}
