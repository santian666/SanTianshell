Unicode true

####
## Please note: Template replacements don't work in this file. They are provided with default defines like
## mentioned underneath.
## If the keyword is not defined, "wails_tools.nsh" will populate them with the values from ProjectInfo.
## If they are defined here, "wails_tools.nsh" will not touch them. This allows to use this project.nsi manually
## from outside of Wails for debugging and development of the installer.
##
## For development first make a wails nsis build to populate the "wails_tools.nsh":
## > wails build --target windows/amd64 --nsis
## Then you can call makensis on this file with specifying the path to your binary:
## For a AMD64 only installer:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app.exe
## For a ARM64 only installer:
## > makensis -DARG_WAILS_ARM64_BINARY=..\..\bin\app.exe
## For a installer with both architectures:
## > makensis -DARG_WAILS_AMD64_BINARY=..\..\bin\app-amd64.exe -DARG_WAILS_ARM64_BINARY=..\..\bin\app-arm64.exe
####
## The following information is taken from the ProjectInfo file, but they can be overwritten here.
####
## !define INFO_PROJECTNAME    "MyProject" # Default "{{.Name}}"
## !define INFO_COMPANYNAME    "MyCompany" # Default "{{.Info.CompanyName}}"
## !define INFO_PRODUCTNAME    "MyProduct" # Default "{{.Info.ProductName}}"
## !define INFO_PRODUCTVERSION "1.0.0"     # Default "{{.Info.ProductVersion}}"
## !define INFO_COPYRIGHT      "Copyright" # Default "{{.Info.Copyright}}"
###
## !define PRODUCT_EXECUTABLE  "Application.exe"      # Default "${INFO_PROJECTNAME}.exe"
## !define UNINST_KEY_NAME     "UninstKeyInRegistry"  # Default "${INFO_COMPANYNAME}${INFO_PRODUCTNAME}"
####
## !define REQUEST_EXECUTION_LEVEL "admin"            # Default "admin"  see also https://nsis.sourceforge.io/Docs/Chapter4.html
!define WAILS_WIN10_REQUIRED "SanTianShell 仅支持 Windows 10、Windows 11 及更高版本。"
!define WAILS_ARCHITECTURE_NOT_SUPPORTED "当前安装包不支持这台电脑的处理器架构。"
!define WAILS_INSTALL_WEBVIEW_DETAILPRINT "正在检查 Microsoft WebView2 Runtime"
####
## Include the wails tools
####
!include "wails_tools.nsh"

# The version information for this two must consist of 4 parts
VIProductVersion "${INFO_PRODUCTVERSION}.0"
VIFileVersion    "${INFO_PRODUCTVERSION}.0"

VIAddVersionKey "CompanyName"     "${INFO_COMPANYNAME}"
VIAddVersionKey "FileDescription" "${INFO_PRODUCTNAME} Installer"
VIAddVersionKey "ProductVersion"  "${INFO_PRODUCTVERSION}"
VIAddVersionKey "FileVersion"     "${INFO_PRODUCTVERSION}"
VIAddVersionKey "LegalCopyright"  "${INFO_COPYRIGHT}"
VIAddVersionKey "ProductName"     "${INFO_PRODUCTNAME}"

# Enable HiDPI support. https://nsis.sourceforge.io/Reference/ManifestDPIAware
ManifestDPIAware true

!include "MUI.nsh"

!define MUI_ICON "..\icon.ico"
!define MUI_UNICON "..\icon.ico"
# !define MUI_WELCOMEFINISHPAGE_BITMAP "resources\leftimage.bmp" #Include this to add a bitmap on the left side of the Welcome Page. Must be a size of 164x314
!define MUI_FINISHPAGE_NOAUTOCLOSE # Wait on the INSTFILES page so the user can take a look into the details of the installation steps
!define MUI_FINISHPAGE_RUN "$INSTDIR\${PRODUCT_EXECUTABLE}"
!define MUI_FINISHPAGE_RUN_TEXT "安装完成后运行 SanTianShell"
!define MUI_ABORTWARNING # This will warn the user if they exit from the installer.

!insertmacro MUI_PAGE_WELCOME # Welcome to the installer page.
# !insertmacro MUI_PAGE_LICENSE "resources\eula.txt" # Adds a EULA page to the installer
!insertmacro MUI_PAGE_DIRECTORY # In which folder install page.
!insertmacro MUI_PAGE_INSTFILES # Installing page.
!insertmacro MUI_PAGE_FINISH # Finished installation page.

!insertmacro MUI_UNPAGE_CONFIRM # Confirm uninstalling page.
!insertmacro MUI_UNPAGE_INSTFILES # Uinstalling page

!insertmacro MUI_LANGUAGE "SimpChinese" # Set the Language of the installer

## The following two statements can be used to sign the installer and the uninstaller. The path to the binaries are provided in %1
#!uninstfinalize 'signtool --file "%1"'
#!finalize 'signtool --file "%1"'

Name "${INFO_PRODUCTNAME}"
OutFile "..\..\bin\${INFO_PROJECTNAME}-${ARCH}-installer.exe" # Name of the installer's file.
InstallDir "$PROGRAMFILES64\SanTianShell" # Default installing folder ($PROGRAMFILES is Program Files folder).
InstallDirRegKey HKLM "${UNINST_KEY}" "InstallLocation"
ShowInstDetails show # This will always show the installation details.

Var ExistingInstallDetected
Var ClearOrphanData

Function .onInit
   !insertmacro wails.checkArchitecture
   StrCpy $ExistingInstallDetected "0"
   StrCpy $ClearOrphanData "0"

   SetRegView 64
   ReadRegStr $0 HKLM "${UNINST_KEY}" "InstallLocation"
   ${If} $0 == ""
      IfFileExists "$PROGRAMFILES64\SanTianShell\${PRODUCT_EXECUTABLE}" 0 +2
      StrCpy $0 "$PROGRAMFILES64\SanTianShell"
   ${EndIf}
   ${If} $0 == ""
      IfFileExists "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}\${PRODUCT_EXECUTABLE}" 0 +2
      StrCpy $0 "$PROGRAMFILES64\${INFO_COMPANYNAME}\${INFO_PRODUCTNAME}"
   ${EndIf}
   ${If} $0 != ""
      StrCpy $INSTDIR $0
      IfFileExists "$INSTDIR\${PRODUCT_EXECUTABLE}" 0 initDone
      StrCpy $ExistingInstallDetected "1"
   ${EndIf}

   ${If} $ExistingInstallDetected == "1"
      MessageBox MB_ICONQUESTION|MB_OKCANCEL|MB_DEFBUTTON1 "检测到已安装的 SanTianShell。$\r$\n$\r$\n点击“确定”将关闭正在运行的旧版程序，保留服务器、节点、凭证、AI 配置和快捷命令，并覆盖升级程序文件。$\r$\n点击“取消”退出安装。" IDOK initDone
      Abort
   ${EndIf}

   SetShellVarContext current
   IfFileExists "$APPDATA\SanTianShell\*.*" 0 initDone
   IfSilent clearOrphanData askOrphanData
   askOrphanData:
      MessageBox MB_ICONQUESTION|MB_YESNO|MB_DEFBUTTON1 "检测到之前卸载后保留的 SanTianShell 数据。$\r$\n$\r$\n点击“是”将清空旧服务器、节点、凭证、AI 配置、快捷命令和日志后进行纯净安装。$\r$\n点击“否”保留旧数据继续安装。" IDYES clearOrphanData IDNO initDone
   clearOrphanData:
      StrCpy $ClearOrphanData "1"
   initDone:
FunctionEnd

Section
    !insertmacro wails.setShellContext

    ${If} $ExistingInstallDetected == "1"
        DetailPrint "正在关闭旧版 SanTianShell 进程"
        nsExec::ExecToStack '"$SYSDIR\taskkill.exe" /F /T /IM "${PRODUCT_EXECUTABLE}"'
        Pop $0
        Pop $1
    ${EndIf}

    ${If} $ClearOrphanData == "1"
        DetailPrint "正在清理上次卸载后保留的用户数据"
        SetShellVarContext current
        RMDir /r /REBOOTOK "$APPDATA\SanTianShell"
        RMDir /r /REBOOTOK "$LOCALAPPDATA\SanTianShell"
        RMDir /r /REBOOTOK "$PROFILE\.santianshell"
        RMDir /r /REBOOTOK "$TEMP\SanTianShell"
        DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "SanTianShell"
        !insertmacro wails.setShellContext
    ${EndIf}

    !insertmacro wails.webview2runtime

    SetOverwrite on
    SetOutPath $INSTDIR

    !insertmacro wails.files
    File "/oname=${INFO_PRODUCTNAME}.ico" "..\icon.ico"

    DetailPrint "正在安装 IPerf3 测速组件"
    SetOutPath "$INSTDIR\tools\iperf3"
    File "..\tools\iperf3\x64\iperf3.exe"
    File "..\tools\iperf3\x64\cygwin1.dll"
    File "..\tools\iperf3\x64\THIRD-PARTY-NOTICES.txt"
    SetOutPath "$INSTDIR\tools\iperf3\licenses"
    File "..\tools\iperf3\x64\licenses\iperf3-LICENSE.txt"
    File "..\tools\iperf3\x64\licenses\Cygwin-Licensing.html"
    File "..\tools\iperf3\x64\licenses\Cygwin-COPYING.LIB.txt"
    SetOutPath $INSTDIR

    !insertmacro wails.writeUninstaller
    WriteRegStr HKLM "${UNINST_KEY}" "InstallLocation" "$INSTDIR"

    SetShellVarContext current
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    !insertmacro wails.setShellContext
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    CreateShortCut "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${INFO_PRODUCTNAME}.ico" 0 SW_SHOWNORMAL "" "${INFO_PRODUCTNAME}"
    CreateShortCut "$DESKTOP\${INFO_PRODUCTNAME}.lnk" "$INSTDIR\${PRODUCT_EXECUTABLE}" "" "$INSTDIR\${INFO_PRODUCTNAME}.ico" 0 SW_SHOWNORMAL "" "${INFO_PRODUCTNAME}"

    !insertmacro wails.associateFiles
    !insertmacro wails.associateCustomProtocols
    System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, p 0, p 0)'
SectionEnd

Section "uninstall"
    !insertmacro wails.setShellContext

    DetailPrint "正在关闭 SanTianShell 进程"
    nsExec::ExecToStack '"$SYSDIR\taskkill.exe" /F /T /IM "${PRODUCT_EXECUTABLE}"'
    Pop $0
    Pop $1

    Delete /REBOOTOK "$INSTDIR\${PRODUCT_EXECUTABLE}"
    Delete /REBOOTOK "$INSTDIR\${INFO_PRODUCTNAME}.ico"
    RMDir /r /REBOOTOK "$INSTDIR\tools"

    SetShellVarContext current
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"
    !insertmacro wails.setShellContext
    Delete "$SMPROGRAMS\${INFO_PRODUCTNAME}.lnk"
    Delete "$DESKTOP\${INFO_PRODUCTNAME}.lnk"

    !insertmacro wails.unassociateFiles
    !insertmacro wails.unassociateCustomProtocols

    SetShellVarContext current
    DeleteRegValue HKCU "Software\Microsoft\Windows\CurrentVersion\Run" "SanTianShell"
    IfSilent uninstallRemoveData uninstallAskData
    uninstallAskData:
        MessageBox MB_ICONQUESTION|MB_YESNO|MB_DEFBUTTON1 "是否同时删除 SanTianShell 的全部用户数据？$\r$\n$\r$\n选择“是”将删除服务器、节点、凭证、AI 配置、快捷命令和日志，之后重新安装将是纯净状态。$\r$\n选择“否”仅卸载程序并保留数据。" IDYES uninstallRemoveData IDNO uninstallKeepData
    uninstallRemoveData:
        DetailPrint "正在删除 SanTianShell 用户数据"
        RMDir /r /REBOOTOK "$APPDATA\SanTianShell"
        RMDir /r /REBOOTOK "$LOCALAPPDATA\SanTianShell"
        RMDir /r /REBOOTOK "$PROFILE\.santianshell"
        RMDir /r /REBOOTOK "$TEMP\SanTianShell"
    uninstallKeepData:

    !insertmacro wails.deleteUninstaller
    SetRegView 32
    DeleteRegKey HKLM "${UNINST_KEY}"
    DeleteRegKey HKCU "${UNINST_KEY}"
    SetRegView 64
    DeleteRegKey HKCU "${UNINST_KEY}"
    RMDir /REBOOTOK "$INSTDIR"
    System::Call 'shell32::SHChangeNotify(i 0x08000000, i 0, p 0, p 0)'
SectionEnd
