; NSIS installer for the TunnelMesh Windows tray client.
;
; Per-user by design: RequestExecutionLevel user plus $LOCALAPPDATA means no elevation, which
; is the same promise the macOS disk image makes (drag into ~/Applications, no administrator).
; It also keeps the installer honest about the start-up entry, which lives in HKCU and would
; be a machine-wide promise if the install were machine-wide.
;
; The executable, the version and the output path come in on the command line from
; scripts/package-windows-tray.sh; nothing here names a build directory, so the same script
; packages a locally built binary and the one a release job cross-compiled.
;
; Path separators are not cosmetic here. `File`, `OutFile` and `!define MUI_ICON` are resolved
; by the *compiler host*, which in CI is Linux, so they use forward slashes; every `$INSTDIR`
; and `$SMPROGRAMS` string is a path the *installed* Windows system reads, so those keep
; backslashes. One spelling for both fails in exactly one of the two directions.

Unicode true
!include "MUI2.nsh"

!define APP_NAME "TunnelMesh Client"
!define EXE_NAME "TunnelMeshClient.exe"
!define APP_ID "com.tunnelmesh.client-tray"
!define UNINSTALL_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\${APP_ID}"
!define RUN_KEY "Software\Microsoft\Windows\CurrentVersion\Run"
!define RUN_VALUE "TunnelMeshClient"

!ifndef VERSION
  !define VERSION "0.0.0-dev"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif
!ifndef BUILD_DIR
  !define BUILD_DIR "."
!endif
!ifndef ICON_FILE
  !define ICON_FILE "TunnelMeshClient.ico"
!endif
!ifndef OUTFILE
  !define OUTFILE "TunnelMeshClient-${VERSION}-windows-${ARCH}-setup.exe"
!endif

Name "${APP_NAME} ${VERSION}"
OutFile "${OUTFILE}"
InstallDir "$LOCALAPPDATA\Programs\${APP_NAME}"
InstallDirRegKey HKCU "${UNINSTALL_KEY}" "InstallLocation"
RequestExecutionLevel user
SetCompressor /SOLID lzma
ShowInstDetails nevershow
; The brand tile, in the installer window as well as on the exe: an installer that shows the
; generic NSIS icon is indistinguishable from any other downloaded executable.
!define MUI_ICON "${ICON_FILE}"
!define MUI_UNICON "${ICON_FILE}"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE_NAME}"
!define MUI_FINISHPAGE_RUN_TEXT "Start ${APP_NAME} now"
!define MUI_FINISHPAGE_SHOWREADME ""
!define MUI_FINISHPAGE_SHOWREADME_NOTCHECKED
!define MUI_FINISHPAGE_SHOWREADME_TEXT "Add a shortcut on the desktop"
!define MUI_FINISHPAGE_SHOWREADME_FUNCTION CreateDesktopShortcut

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "English"
!insertmacro MUI_LANGUAGE "SimpChinese"

Var DesktopShortcut

Section "-Application" SecApplication
  SetShellVarContext current
  SetOutPath "$INSTDIR"
  File "${BUILD_DIR}/${EXE_NAME}"

  ; The tray rewrites this value itself through HKCU\...\Run, but only when it next starts,
  ; so the installer has to state it now: an install that moves the executable would
  ; otherwise leave the login entry pointing at the previous path for a whole session.
  WriteRegStr HKCU "${RUN_KEY}" "${RUN_VALUE}" `"$INSTDIR\${EXE_NAME}"`

  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayName" "${APP_NAME}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "Publisher" "TunnelMesh"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "URLInfoAbout" "https://github.com/nnworld/TunnelMesh"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "DisplayIcon" "$INSTDIR\${EXE_NAME},0"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKCU "${UNINSTALL_KEY}" "UninstallString" '"$INSTDIR\Uninstall.exe"'
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoModify" 1
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "NoRepair" 1
  ; Add/Remove Programs estimates from the directory it just wrote; this only refines the
  ; number shown before that scan finishes, and is 100 KB * 1000, i.e. ~100 MB.
  WriteRegDWORD HKCU "${UNINSTALL_KEY}" "EstimatedSize" 102400

  WriteUninstaller "$INSTDIR\Uninstall.exe"

  CreateDirectory "$SMPROGRAMS\${APP_NAME}"
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk" "$INSTDIR\${EXE_NAME}" "" "$INSTDIR\${EXE_NAME}" 0
  CreateShortCut "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk" "$INSTDIR\Uninstall.exe"
SectionEnd

Function CreateDesktopShortcut
  StrCpy $DesktopShortcut "1"
  CreateShortCut "$DESKTOP\${APP_NAME}.lnk" "$INSTDIR\${EXE_NAME}" "" "$INSTDIR\${EXE_NAME}" 0
FunctionEnd

Section "Uninstall"
  SetShellVarContext current
  ; The tray holds its own mutex and its client.lock while running, and both belong to the
  ; process, so a delete that fails because the file is in use is reported by the installer
  ; rather than prevented by a machine-wide service stop.
  Delete "$INSTDIR\${EXE_NAME}"
  Delete "$INSTDIR\Uninstall.exe"
  RMDir "$INSTDIR"

  Delete "$SMPROGRAMS\${APP_NAME}\${APP_NAME}.lnk"
  Delete "$SMPROGRAMS\${APP_NAME}\Uninstall ${APP_NAME}.lnk"
  RMDir "$SMPROGRAMS\${APP_NAME}"
  Delete "$DESKTOP\${APP_NAME}.lnk"

  ; The start-up value is the one side effect that outlives the files, so it goes first: an
  ; interrupted uninstall that is re-run simply repeats it.
  DeleteRegValue HKCU "${RUN_KEY}" "${RUN_VALUE}"
  DeleteRegKey HKCU "${UNINSTALL_KEY}"
SectionEnd
