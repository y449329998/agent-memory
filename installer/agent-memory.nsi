; agent-memory 安装包（NSIS 3，按用户安装，无需管理员）
; 由 scripts/package.ps1 调用；版本、载荷目录与输出路径由命令行注入。

Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"

!ifndef APP_VERSION
  !define APP_VERSION "0.0.0"
!endif
!ifndef APP_FILE_VERSION
  !define APP_FILE_VERSION "0.0.0.0"
!endif
!ifndef APP_SOURCE
  !error "APP_SOURCE 未定义：请通过 scripts/package.ps1 调用"
!endif
!ifndef APP_SIZE_KB
  !define APP_SIZE_KB "0"
!endif
!ifndef OUT_FILE
  !define OUT_FILE "..\out\agent-memory-setup-x64.exe"
!endif
!ifndef APP_ICON
  !define APP_ICON "..\internal\desktop\assets\app.ico"
!endif

!define APP_NAME "agent-memory"
!define APP_DISPLAY_NAME "agent-memory 共享记忆"
!define APP_EXE "agent-memory-app.exe"
!define APP_CLI "agent-memory.exe"
!define APP_MENU "${APP_NAME}"
!define APP_REG_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_NAME}"
!define APP_CFG_KEY "Software\${APP_NAME}"
!define APP_RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"

Name "${APP_DISPLAY_NAME}"
OutFile "${OUT_FILE}"
InstallDir "$LOCALAPPDATA\Programs\agent-memory"
InstallDirRegKey HKCU "${APP_CFG_KEY}" "InstallDir"
RequestExecutionLevel user
SetCompressor /SOLID lzma
SetCompressorDictSize 32
BrandingText "agent-memory"

Icon "${APP_ICON}"
UninstallIcon "${APP_ICON}"

VIProductVersion "${APP_FILE_VERSION}"
VIAddVersionKey /LANG=2052 "ProductName" "${APP_DISPLAY_NAME}"
VIAddVersionKey /LANG=2052 "FileDescription" "${APP_DISPLAY_NAME}安装程序"
VIAddVersionKey /LANG=2052 "FileVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=2052 "ProductVersion" "${APP_VERSION}"
VIAddVersionKey /LANG=2052 "Publisher" "agent-memory"
VIAddVersionKey /LANG=2052 "LegalCopyright" "agent-memory"

!define MUI_ICON "${APP_ICON}"
!define MUI_UNICON "${APP_ICON}"
!define MUI_ABORTWARNING
!define MUI_WELCOMEPAGE_TITLE "安装 ${APP_DISPLAY_NAME}"
!define MUI_WELCOMEPAGE_TEXT "把 Qoder、Trae、WorkBuddy 与 OpenCode 的长期记忆、会话任务接到同一份本地库。$\r$\n$\r$\n安装到当前用户目录，不需要管理员权限；已有的记忆库与配置不会被覆盖。"
!define MUI_FINISHPAGE_RUN "$INSTDIR\${APP_EXE}"
!define MUI_FINISHPAGE_RUN_TEXT "启动 ${APP_DISPLAY_NAME}"
!define MUI_FINISHPAGE_TEXT "安装完成。首次启动后可在「设置 → 服务与托盘」确认 MCP 启动程序指向安装目录。"
!define MUI_UNCONFIRMPAGE_TEXT_TOP "将删除程序文件、开始菜单和桌面快捷方式。$\r$\n$\r$\n用户目录下的 .agent-memory（记忆库、配置与日志）会保留。"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_UNPAGE_FINISH

!insertmacro MUI_LANGUAGE "SimpChinese"
!insertmacro MUI_LANGUAGE "English"

Var ExistingAutostart

Function StopRunningApp
  ; 服务是 agent-memory-app.exe --serve，更新时先结束它，避免 exe 被锁。
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM ${APP_EXE} /F'
  Pop $0
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM ${APP_CLI} /F'
  Pop $0
FunctionEnd

Section "主程序" SecMain
  Call StopRunningApp

  ReadRegStr $ExistingAutostart HKCU "${APP_RUN_KEY}" "${APP_NAME}"

  SetOutPath "$INSTDIR"
  File "${APP_SOURCE}\${APP_EXE}"
  File "${APP_SOURCE}\${APP_CLI}"
  File "${APP_SOURCE}\README.md"
  SetOutPath "$INSTDIR\docs"
  File "${APP_SOURCE}\docs\usage.md"
  File "${APP_SOURCE}\docs\client-guide.md"
  SetOutPath "$INSTDIR\docs\images\client"
  File "${APP_SOURCE}\docs\images\client\*.webp"

  WriteUninstaller "$INSTDIR\uninstall.exe"

  SetOutPath "$INSTDIR"
  Delete "$SMPROGRAMS\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\卸载 ${APP_DISPLAY_NAME}.lnk"
  RMDir /r "$SMPROGRAMS\${APP_NAME}"
  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\${APP_DISPLAY_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\使用说明.lnk" "$INSTDIR\docs\client-guide.md" "" "$INSTDIR\docs\client-guide.md" 0
  CreateShortcut "$SMPROGRAMS\${APP_NAME}\卸载 ${APP_DISPLAY_NAME}.lnk" "$INSTDIR\uninstall.exe"
  CreateShortcut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${APP_EXE}" "" "$INSTDIR\${APP_EXE}" 0

  ${If} $ExistingAutostart != ""
    WriteRegStr HKCU "${APP_RUN_KEY}" "${APP_NAME}" '"$INSTDIR\${APP_EXE}" --tray-only'
  ${EndIf}

  WriteRegStr HKCU "${APP_CFG_KEY}" "InstallDir" "$INSTDIR"
  WriteRegStr HKCU "${APP_REG_KEY}" "DisplayName" "${APP_DISPLAY_NAME}"
  WriteRegStr HKCU "${APP_REG_KEY}" "DisplayVersion" "${APP_VERSION}"
  WriteRegStr HKCU "${APP_REG_KEY}" "Publisher" "agent-memory"
  WriteRegStr HKCU "${APP_REG_KEY}" "DisplayIcon" "$INSTDIR\${APP_EXE},0"
  WriteRegStr HKCU "${APP_REG_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${APP_REG_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKCU "${APP_REG_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${APP_REG_KEY}" "NoRepair" 1
  WriteRegDWORD HKCU "${APP_REG_KEY}" "EstimatedSize" "${APP_SIZE_KB}"

  ; 刷新资源管理器图标缓存，升级后快捷方式能立即显示新图标。
  System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0x0000, p 0, p 0)'
SectionEnd

Section "Uninstall"
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM ${APP_EXE} /F'
  Pop $0
  nsExec::Exec '"$SYSDIR\taskkill.exe" /IM ${APP_CLI} /F'
  Pop $0

  Delete "$DESKTOP\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\${APP_DISPLAY_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\使用说明.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\卸载 ${APP_DISPLAY_NAME}.lnk"
  RMDir "$SMPROGRAMS\${APP_NAME}"
  Delete "$SMPROGRAMS\${APP_NAME}.lnk"

  RMDir /r "$INSTDIR\docs"
  Delete "$INSTDIR\${APP_EXE}"
  Delete "$INSTDIR\${APP_CLI}"
  Delete "$INSTDIR\README.md"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  DeleteRegKey HKCU "${APP_REG_KEY}"
  DeleteRegKey HKCU "${APP_CFG_KEY}"
  DeleteRegValue HKCU "${APP_RUN_KEY}" "${APP_NAME}"
  ; ~/.agent-memory 中的记忆库、配置与日志保留，卸载不删除
SectionEnd
