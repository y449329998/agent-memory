// Package assets 存放桌面应用的内嵌资源。
package assets

import _ "embed"

// AppIcon 是托盘与快捷方式使用的 ICO 字节。
//
//go:embed app.ico
var AppIcon []byte
