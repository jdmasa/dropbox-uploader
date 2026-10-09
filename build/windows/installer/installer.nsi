; Dropbox Uploader installer (NSIS 3, Unicode).
;
; Supports Windows 7 SP1 / 8.1 / 10 / 11, 32-bit and 64-bit, and installs the
; Microsoft WebView2 runtime when it is missing. (Wails' own template requires
; Windows 10, so this script replaces it.)
;
; Build (from this folder):
;   makensis /DVERSION=1.2.3 /DX64_BINARY=..\..\bin\DropboxUploader-x64.exe \
;            /DX86_BINARY=..\..\bin\DropboxUploader-x86.exe /DOUTFILE=..\..\bin\Setup.exe installer.nsi
; It also expects tmp\MicrosoftEdgeWebview2Setup.exe (the WebView2 bootstrapper).

Unicode true
ManifestDPIAware true
ManifestSupportedOS all
RequestExecutionLevel admin
SetCompressor /SOLID lzma

!ifndef VERSION
  !define VERSION "0.0.0"
!endif
!ifndef OUTFILE
  !define OUTFILE "..\..\bin\DropboxUploader-Setup.exe"
!endif
!define PRODUCT "Dropbox Uploader"
!define EXE "DropboxUploader.exe"
!define UNINST_KEY "Software\Microsoft\Windows\CurrentVersion\Uninstall\DropboxUploader"
!define WEBVIEW2_GUID "{F3017226-FE2A-4295-8BDF-00C3A9A7E4C5}"

Name "${PRODUCT}"
OutFile "${OUTFILE}"
InstallDir "$PROGRAMFILES\${PRODUCT}"
InstallDirRegKey HKLM "${UNINST_KEY}" "InstallLocation"
ShowInstDetails show
BrandingText "${PRODUCT} ${VERSION}"

VIProductVersion "${VERSION}.0"
VIFileVersion "${VERSION}.0"
VIAddVersionKey /LANG=0 "ProductName" "${PRODUCT}"
VIAddVersionKey /LANG=0 "FileDescription" "${PRODUCT} installer"
VIAddVersionKey /LANG=0 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=0 "LegalCopyright" "José D. Masa"

!include "MUI2.nsh"
!include "x64.nsh"
!include "WinVer.nsh"
!include "LogicLib.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
!define MUI_ABORTWARNING
!define MUI_FINISHPAGE_RUN "$INSTDIR\${EXE}"

!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH
!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

; The first language is the default; Windows' UI language picks the others automatically.
!insertmacro MUI_LANGUAGE "Catalan"
!insertmacro MUI_LANGUAGE "Spanish"
!insertmacro MUI_LANGUAGE "English"

LangString OldWindows ${LANG_CATALAN} "Dropbox Uploader necessita Windows 7 SP1, Windows 8.1 o una versió més nova."
LangString OldWindows ${LANG_SPANISH} "Dropbox Uploader necesita Windows 7 SP1, Windows 8.1 o una versión más reciente."
LangString OldWindows ${LANG_ENGLISH} "Dropbox Uploader needs Windows 7 SP1, Windows 8.1 or newer."
LangString InstallingWebView ${LANG_CATALAN} "Instal·lant Microsoft WebView2 (pot trigar uns minuts)…"
LangString InstallingWebView ${LANG_SPANISH} "Instalando Microsoft WebView2 (puede tardar unos minutos)…"
LangString InstallingWebView ${LANG_ENGLISH} "Installing Microsoft WebView2 (this may take a few minutes)…"
LangString WebViewFailed ${LANG_CATALAN} "No s'ha pogut instal·lar Microsoft WebView2. Comprova la connexió a internet i torna a executar l'instal·lador."
LangString WebViewFailed ${LANG_SPANISH} "No se ha podido instalar Microsoft WebView2. Comprueba la conexión a internet y vuelve a ejecutar el instalador."
LangString WebViewFailed ${LANG_ENGLISH} "Microsoft WebView2 could not be installed. Check the internet connection and run the installer again."

Function .onInit
  ${IfNot} ${AtLeastWin7}
    MessageBox MB_OK|MB_ICONSTOP "$(OldWindows)"
    Abort
  ${EndIf}
  ${If} ${IsWin7}
  ${AndIfNot} ${AtLeastServicePack} 1
    MessageBox MB_OK|MB_ICONSTOP "$(OldWindows)"
    Abort
  ${EndIf}
  ${If} ${RunningX64}
    SetRegView 64
    StrCpy $INSTDIR "$PROGRAMFILES64\${PRODUCT}"
  ${EndIf}
  ; Reuse the previous install location if there is one.
  ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
  ${If} $0 != ""
    StrCpy $INSTDIR $0
  ${EndIf}
FunctionEnd

; WebView2 is installed when its EdgeUpdate client key has a version ("pv").
Function HasWebView2
  StrCpy $0 ""
  ReadRegStr $0 HKLM "SOFTWARE\WOW6432Node\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKLM "SOFTWARE\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  ${If} $0 == ""
  ${OrIf} $0 == "0.0.0.0"
    ReadRegStr $0 HKCU "Software\Microsoft\EdgeUpdate\Clients\${WEBVIEW2_GUID}" "pv"
  ${EndIf}
  ${If} $0 == "0.0.0.0"
    StrCpy $0 ""
  ${EndIf}
FunctionEnd

Section "Install"
  ; Stop a running copy so its files can be replaced.
  nsExec::Exec 'taskkill /F /IM "${EXE}"'

  Call HasWebView2
  ${If} $0 == ""
    DetailPrint "$(InstallingWebView)"
    InitPluginsDir
    SetOutPath "$PLUGINSDIR"
    File "tmp\MicrosoftEdgeWebview2Setup.exe"
    ExecWait '"$PLUGINSDIR\MicrosoftEdgeWebview2Setup.exe" /silent /install'
    Call HasWebView2
    ${If} $0 == ""
      MessageBox MB_OK|MB_ICONEXCLAMATION "$(WebViewFailed)"
    ${EndIf}
  ${EndIf}

  SetOutPath "$INSTDIR"
  ${If} ${RunningX64}
    File "/oname=${EXE}" "${X64_BINARY}"
  ${Else}
    File "/oname=${EXE}" "${X86_BINARY}"
  ${EndIf}

  SetShellVarContext all
  CreateShortcut "$SMPROGRAMS\${PRODUCT}.lnk" "$INSTDIR\${EXE}"
  CreateShortcut "$DESKTOP\${PRODUCT}.lnk" "$INSTDIR\${EXE}"

  WriteUninstaller "$INSTDIR\uninstall.exe"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayName" "${PRODUCT}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "${UNINST_KEY}" "DisplayIcon" "$INSTDIR\${EXE}"
  WriteRegStr HKLM "${UNINST_KEY}" "Publisher" "José D. Masa"
  WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"
  WriteRegStr HKLM "${UNINST_KEY}" "UninstallString" '"$INSTDIR\uninstall.exe"'
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoModify" 1
  WriteRegDWORD HKLM "${UNINST_KEY}" "NoRepair" 1
SectionEnd

Function un.onInit
  ${If} ${RunningX64}
    SetRegView 64
  ${EndIf}
FunctionEnd

Section "Uninstall"
  nsExec::Exec 'taskkill /F /IM "${EXE}"'
  SetShellVarContext all
  Delete "$SMPROGRAMS\${PRODUCT}.lnk"
  Delete "$DESKTOP\${PRODUCT}.lnk"
  Delete "$INSTDIR\${EXE}"
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"
  DeleteRegKey HKLM "${UNINST_KEY}"
  ; Settings and the upload queue in %APPDATA% / %LOCALAPPDATA% are kept on purpose.
SectionEnd
