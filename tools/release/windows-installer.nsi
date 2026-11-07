# -*- coding: utf-8 -*-
Unicode true
!include "MUI2.nsh"
!include "LogicLib.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "FileFunc.nsh"
!include "nsDialogs.nsh"

!ifndef AppVersion
  !error "AppVersion is required"
!endif
!ifndef NumericVersion
  !error "NumericVersion is required"
!endif
!ifndef BuildDir
  !error "BuildDir is required"
!endif
!ifndef InstallerDir
  !error "InstallerDir is required"
!endif
!ifndef InstallerName
  !error "InstallerName is required"
!endif

!define APP_NAME "Codex Usage Monitor"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexUsageMonitor"
Name "${APP_NAME}"
OutFile "${InstallerDir}/${InstallerName}.exe"
InstallDir ""
RequestExecutionLevel user
ManifestDPIAware true
ManifestSupportedOS all
SetCompressor /SOLID lzma
SetOverwrite on
AllowSkipFiles off
ShowInstDetails show
ShowUninstDetails show
VIProductVersion "${NumericVersion}"
VIAddVersionKey /LANG=1033 "ProductName" "${APP_NAME}"
VIAddVersionKey /LANG=1033 "ProductVersion" "${AppVersion}"
VIAddVersionKey /LANG=1033 "FileDescription" "${APP_NAME} Setup"
VIAddVersionKey /LANG=1033 "FileVersion" "${AppVersion}"
VIAddVersionKey /LANG=1033 "LegalCopyright" "Codex Usage Monitor contributors"

!define MUI_PAGE_CUSTOMFUNCTION_PRE SkipHandoffWelcome
!insertmacro MUI_PAGE_WELCOME
Page custom ScopePage ScopePageLeave
!define MUI_PAGE_CUSTOMFUNCTION_LEAVE CheckInstallLocation
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_FUNCTION LaunchMonitor
!define MUI_PAGE_CUSTOMFUNCTION_SHOW ConfigureFinishPage
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES
!insertmacro MUI_UNPAGE_FINISH
!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

LangString StopFailed ${LANG_ENGLISH} "Close Codex Usage Monitor and retry."
LangString StopFailed ${LANG_SIMPCHINESE} "请退出 Codex Usage Monitor 后重试。"
LangString SetupRunning ${LANG_ENGLISH} "Another installation or uninstallation is running."
LangString SetupRunning ${LANG_SIMPCHINESE} "另一个安装或卸载程序正在运行。"
LangString WrongPlatform ${LANG_ENGLISH} "Windows 10 or later (x64) is required."
LangString WrongPlatform ${LANG_SIMPCHINESE} "需要 Windows 10 或更高版本（x64）。"
LangString MoveInstallation ${LANG_ENGLISH} "An existing installation was found at $PreviousDir. Uninstall it before choosing a different folder; your settings will be kept."
LangString MoveInstallation ${LANG_SIMPCHINESE} "已安装在 $PreviousDir。若需更换目录，请先卸载旧版本；用户设置会保留。"
LangString ScopeTitle ${LANG_ENGLISH} "Installation scope"
LangString ScopeTitle ${LANG_SIMPCHINESE} "安装范围"
LangString ScopeDescription ${LANG_ENGLISH} "Choose who can use Codex Usage Monitor."
LangString ScopeDescription ${LANG_SIMPCHINESE} "选择哪些用户可以使用 Codex Usage Monitor。"
LangString ScopeAll ${LANG_ENGLISH} "Install for all users (requires administrator permission)"
LangString ScopeAll ${LANG_SIMPCHINESE} "为所有用户安装（需要管理员权限）"
LangString ScopeCurrent ${LANG_ENGLISH} "Install only for me"
LangString ScopeCurrent ${LANG_SIMPCHINESE} "仅为我安装"
LangString ElevationFailed ${LANG_ENGLISH} "Administrator permission was not granted. Retry or choose installation only for you."
LangString ElevationFailed ${LANG_SIMPCHINESE} "未获得管理员权限。请重试，或选择仅为我安装。"
LangString OtherScopeInstalled ${LANG_ENGLISH} "A copy is already installed with a different scope. Uninstall it before changing the installation scope; user settings will be kept."
LangString OtherScopeInstalled ${LANG_SIMPCHINESE} "已存在另一安装范围的版本。请先卸载，再切换安装范围；用户设置会保留。"

Var SetupMutex
Var PreviousDir
Var InstallMode
Var RequestedDir
Var ElevatedHandoff
Var AccountType
Var ScopeAllRadio
Var ScopeCurrentRadio

!macro TrimInstallDir
  ; Preserve new directories: GetFullPathName clears paths that do not exist yet.
  ; The NSIS uninstaller's child command adds a trailing slash to $INSTDIR.
  StrLen $0 $INSTDIR
  ${DoWhile} $0 > 3
    StrCpy $1 $INSTDIR 1 -1
    ${If} $1 != "\"
      ${ExitDo}
    ${EndIf}
    StrCpy $INSTDIR $INSTDIR -1
    StrLen $0 $INSTDIR
  ${Loop}
!macroend

!macro SetupLock PREFIX
Function ${PREFIX}AcquireSetupLock
  System::Call 'kernel32::CreateMutexW(p 0, i 0, w "Local\CodexUsageMonitor.Setup") p.r0 ?e'
  Pop $1
  StrCpy $SetupMutex $0
  ${If} $0 = 0
  ${OrIf} $1 = 183
    MessageBox MB_OK|MB_ICONSTOP "$(SetupRunning)" /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}
FunctionEnd
!macroend
!insertmacro SetupLock ""
!insertmacro SetupLock "un."

; Relaunch only the all-users operation with elevation. A standard-user install
; stays in its original account, including when administrator credentials differ.
!macro Elevate PREFIX EXECUTABLE PARAMETERS
Function ${PREFIX}EnsureInstallPrivilege
  ${If} $InstallMode != "AllUsers"
  ${OrIf} $AccountType == "Admin"
    Return
  ${EndIf}
  StrCpy $2 '${PARAMETERS}'
  IfSilent 0 +2
    StrCpy $2 "$2 /S"
  !if "${PREFIX}" == ""
    StrCpy $2 "$2 /D=$INSTDIR"
  !endif
  System::Call 'kernel32::CloseHandle(p $SetupMutex)'
  StrCpy $SetupMutex 0
  ClearErrors
  HideWindow
  ExecShellWait "runas" '${EXECUTABLE}' "$2" SW_SHOWNORMAL
  ${If} ${Errors}
    ShowWindow $HWNDPARENT ${SW_SHOW}
    Call ${PREFIX}AcquireSetupLock
    MessageBox MB_OK|MB_ICONSTOP "$(ElevationFailed)" /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}
  Quit
FunctionEnd
!macroend
!insertmacro Elevate "" "$EXEPATH" "/AllUsers /Elevated"
!insertmacro Elevate "un." "$INSTDIR\Uninstall.exe" "/AllUsers"

Function SetInstallMode
  ${If} $InstallMode == "AllUsers"
    SetShellVarContext all
    StrCpy $INSTDIR "$PROGRAMFILES64\CodexUsageMonitor"
  ${Else}
    SetShellVarContext current
    StrCpy $INSTDIR "$LOCALAPPDATA\Programs\CodexUsageMonitor"
  ${EndIf}
  ReadRegStr $PreviousDir SHCTX "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $PreviousDir != ""
    StrCpy $INSTDIR $PreviousDir
  ${EndIf}
  ${If} $RequestedDir != ""
    StrCpy $INSTDIR $RequestedDir
  ${EndIf}
  !insertmacro TrimInstallDir
FunctionEnd

Function .onInit
  SetRegView 64
  UserInfo::GetAccountType
  Pop $AccountType
  Call AcquireSetupLock
  ${IfNot} ${RunningX64}
  ${OrIfNot} ${AtLeastWin10}
    MessageBox MB_OK|MB_ICONSTOP "$(WrongPlatform)" /SD IDOK
    SetErrorLevel 1
    Abort
  ${EndIf}
  ; NSIS has already applied /D; preserve an explicit path through scope selection.
  StrCpy $RequestedDir $INSTDIR
  StrCpy $InstallMode "AllUsers"
  ReadRegStr $0 HKLM "${UNINSTALL_KEY}" "InstallLocation"
  ReadRegStr $1 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 == ""
  ${AndIf} $1 != ""
    StrCpy $InstallMode "CurrentUser"
  ${EndIf}
  ClearErrors
  ${GetOptions} $CMDLINE "/CurrentUser" $0
  ${IfNot} ${Errors}
    StrCpy $InstallMode "CurrentUser"
  ${EndIf}
  ClearErrors
  ${GetOptions} $CMDLINE "/AllUsers" $0
  ${IfNot} ${Errors}
    StrCpy $InstallMode "AllUsers"
  ${EndIf}
  ClearErrors
  ${GetOptions} $CMDLINE "/Elevated" $0
  ${IfNot} ${Errors}
    StrCpy $ElevatedHandoff 1
  ${EndIf}
  Call SetInstallMode
  IfSilent 0 init_done
    Call CheckInstallLocation
    Call EnsureInstallPrivilege
  init_done:
FunctionEnd

Function un.onInit
  SetRegView 64
  !insertmacro TrimInstallDir
  UserInfo::GetAccountType
  Pop $AccountType
  Call un.AcquireSetupLock
  ReadRegStr $0 HKLM "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 == $INSTDIR
    StrCpy $InstallMode "AllUsers"
    SetShellVarContext all
  ${Else}
    StrCpy $InstallMode "CurrentUser"
    SetShellVarContext current
  ${EndIf}
  Call un.EnsureInstallPrivilege
FunctionEnd

Function SkipHandoffWelcome
  ${If} $ElevatedHandoff == 1
    Abort
  ${EndIf}
FunctionEnd

Function ScopePage
  ${If} $ElevatedHandoff == 1
    Abort
  ${EndIf}
  !insertmacro MUI_HEADER_TEXT "$(ScopeTitle)" "$(ScopeDescription)"
  nsDialogs::Create 1018
  Pop $0
  ${NSD_CreateRadioButton} 0 20u 100% 20u "$(ScopeAll)"
  Pop $ScopeAllRadio
  ${NSD_CreateRadioButton} 0 50u 100% 20u "$(ScopeCurrent)"
  Pop $ScopeCurrentRadio
  ${If} $InstallMode == "AllUsers"
    ${NSD_Check} $ScopeAllRadio
  ${Else}
    ${NSD_Check} $ScopeCurrentRadio
  ${EndIf}
  nsDialogs::Show
FunctionEnd

Function ScopePageLeave
  ${NSD_GetState} $ScopeAllRadio $0
  StrCpy $1 "CurrentUser"
  ${If} $0 == ${BST_CHECKED}
    StrCpy $1 "AllUsers"
  ${EndIf}
  ${If} $1 != $InstallMode
    StrCpy $InstallMode $1
    Call SetInstallMode
  ${EndIf}
  Call CheckInstallLocation
  Call EnsureInstallPrivilege
  SetErrorLevel 0
FunctionEnd

Function ConfigureFinishPage
  ; The elevated setup must not launch the monitor under the administrator token.
  ${If} $AccountType == "Admin"
    ${NSD_Uncheck} $mui.FinishPage.Run
    ShowWindow $mui.FinishPage.Run ${SW_HIDE}
  ${EndIf}
FunctionEnd

Function LaunchMonitor
  ${If} $AccountType != "Admin"
    Exec '"$INSTDIR\CodexUsageMonitor.exe"'
  ${EndIf}
FunctionEnd

Function CheckInstallLocation
  !insertmacro TrimInstallDir
  ${If} $InstallMode == "AllUsers"
    ReadRegStr $0 HKCU "${UNINSTALL_KEY}" "InstallLocation"
  ${Else}
    ReadRegStr $0 HKLM "${UNINSTALL_KEY}" "InstallLocation"
  ${EndIf}
  ${If} $0 != ""
    MessageBox MB_OK|MB_ICONSTOP "$(OtherScopeInstalled)" /SD IDOK
    Abort
  ${EndIf}
  ${If} $PreviousDir != ""
  ${AndIf} $PreviousDir != $INSTDIR
    MessageBox MB_OK|MB_ICONSTOP "$(MoveInstallation)" /SD IDOK
    Abort
  ${EndIf}
FunctionEnd

!macro StopMonitor PREFIX
Function ${PREFIX}StopMonitor
  StrCpy $3 0
  stop_poll:
    System::Call 'kernel32::OpenMutexW(i 0x100000, i 0, w "Local\CodexUsageMonitor.Watcher") p.r0 ?e'
    Pop $1
    ${If} $0 = 0
      ${If} $1 = 2
        Return
      ${EndIf}
      Goto stop_failed
    ${EndIf}
    ; Do not hold the mutex ourselves while waiting for the watcher to release it.
    System::Call 'kernel32::CloseHandle(p r0)'
    System::Call 'kernel32::OpenEventW(i 2, i 0, w "Local\CodexUsageMonitor.Stop") p.r0 ?e'
    Pop $1
    ${If} $0 != 0
      System::Call 'kernel32::SetEvent(p r0) i.r2'
      System::Call 'kernel32::CloseHandle(p r0)'
      ${If} $2 = 0
        Goto stop_failed
      ${EndIf}
    ${ElseIf} $1 != 2
      Goto stop_failed
    ${EndIf}
    ; The mutex is released only after the existing overlay worker has stopped.
    Sleep 100
    IntOp $3 $3 + 1
    IntCmp $3 150 stop_failed stop_poll stop_failed
  stop_failed:
    MessageBox MB_OK|MB_ICONSTOP "$(StopFailed)" /SD IDOK
    SetErrorLevel 1
    Abort
FunctionEnd
!macroend
!insertmacro StopMonitor ""
!insertmacro StopMonitor "un."

; One ownership list drives both extraction and uninstall; never delete user files recursively.
!macro PayloadFile MODE DIRECTORY FILENAME
  !if "${MODE}" == "install"
    SetOutPath "$INSTDIR${DIRECTORY}"
    File "${BuildDir}${DIRECTORY}\${FILENAME}"
  !else
    Delete "$INSTDIR${DIRECTORY}\${FILENAME}"
  !endif
!macroend
!macro Payload MODE
  !insertmacro PayloadFile ${MODE} "" "CodexUsageMonitor.exe"
  !insertmacro PayloadFile ${MODE} "" "LICENSE"
  !insertmacro PayloadFile ${MODE} "" "THIRD-PARTY-NOTICES.md"
  !insertmacro PayloadFile ${MODE} "" "CHANGELOG.md"
  !insertmacro PayloadFile ${MODE} "\LICENSES" "codex-statusbar-MIT.txt"
  !insertmacro PayloadFile ${MODE} "\LICENSES" "codex-token-overlay-MIT.txt"
  !insertmacro PayloadFile ${MODE} "\LICENSES" "golang-x-sys-BSD.txt"
  !insertmacro PayloadFile ${MODE} "\LICENSES" "klauspost-compress-LICENSE.txt"
!macroend

Section "Install"
  Call CheckInstallLocation
  Call EnsureInstallPrivilege
  Call StopMonitor
  !insertmacro Payload "install"
  SetOutPath "$INSTDIR"
  WriteUninstaller "$INSTDIR\Uninstall.exe"
  CreateShortcut "$SMPROGRAMS\${APP_NAME}.lnk" "$INSTDIR\CodexUsageMonitor.exe" "" "$INSTDIR\CodexUsageMonitor.exe" 0
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "DisplayVersion" "${AppVersion}"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "DisplayIcon" '"$INSTDIR\CodexUsageMonitor.exe"'
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe" /$InstallMode'
  WriteRegStr SHCTX "${UNINSTALL_KEY}" "QuietUninstallString" '"$INSTDIR\Uninstall.exe" /$InstallMode /S'
  WriteRegDWORD SHCTX "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD SHCTX "${UNINSTALL_KEY}" "NoRepair" 1
SectionEnd

Section "Uninstall"
  Call un.StopMonitor
  ReadRegStr $0 HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "CodexUsageMonitor"
  StrCpy $1 '"$INSTDIR\CodexUsageMonitor.exe" '
  StrLen $2 $1
  StrCpy $0 $0 $2
  ${If} $0 == $1
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "CodexUsageMonitor"
  ${EndIf}
  ReadRegStr $0 SHCTX "${UNINSTALL_KEY}" "InstallLocation"
  ${If} $0 == $INSTDIR
    Delete "$SMPROGRAMS\${APP_NAME}.lnk"
    DeleteRegKey SHCTX "${UNINSTALL_KEY}"
  ${EndIf}
  !insertmacro Payload "uninstall"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR\LICENSES"
  SetOutPath "$TEMP"
  RMDir "$INSTDIR"
SectionEnd
