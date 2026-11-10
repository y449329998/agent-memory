# 发布产物

| 文件 | 说明 |
| --- | --- |
| `agent-memory-setup-x64-0.2.0.exe` | Windows x64 安装包（NSIS 3，per-user，约 7.9 MB） |
| `SHA256.txt` | 上面安装包的 SHA-256，格式为 `<哈希>  <文件名>` |

## 校验

PowerShell：

```powershell
Get-FileHash .\agent-memory-setup-x64-0.2.0.exe -Algorithm SHA256
```

Git Bash / Linux / macOS：

```bash
sha256sum -c SHA256.txt
```

哈希对不上就不要运行，回来这里重新下载。

## 安装与卸载

- 双击安装，默认装到 `%LOCALAPPDATA%\Programs\agent-memory`，**不需要管理员权限**。
- 安装后会创建桌面快捷方式、开始菜单文件夹（程序 / 使用说明 / 卸载）与控制面板卸载项。
- 卸载只删程序与快捷方式，**保留数据目录 `~\.agent-memory`**（记忆库、配置、令牌、备份都在这里）。
- 想彻底清除：卸载后手动删除 `~\.agent-memory`。

## 这个包是怎么来的

```powershell
powershell -ExecutionPolicy Bypass -File scripts\package.ps1
```

脚本会跑 `go test ./...`，以 `CGO_ENABLED=0` 静态编译 CLI 与 GUI 两个 exe，
再把它们连同 README 与 `docs/`（含图解指南与截图）打成上面的安装包，并打印大小与 SHA-256。
产物默认输出到 `out\agent-memory-setup-x64.exe`，发布时按版本号改名放进本目录。

客户端使用方式请看 [../docs/client-guide.md](../docs/client-guide.md)。
