@echo off
chcp 65001 >nul
setlocal

set "VERSION=0.1.38"
set "RELEASE_DIR=release"
set "X64_NAME=SanTianShell-%VERSION%-Windows-x64"
set "IPERF3_SOURCE=build\windows\tools\iperf3\x64"
set "NSIS_DIR=%ProgramFiles(x86)%\NSIS"

where makensis.exe >nul 2>nul
if errorlevel 1 if exist "%NSIS_DIR%\makensis.exe" set "PATH=%NSIS_DIR%;%PATH%"
where makensis.exe >nul 2>nul
if errorlevel 1 (
    echo 缺少 NSIS，无法生成 Windows 安装程序。
    echo 请先执行：winget install --id NSIS.NSIS --exact
    exit /b 1
)

wails build -clean -trimpath -platform windows/amd64 -webview2 embed -nsis
if errorlevel 1 exit /b %errorlevel%

if not exist "%RELEASE_DIR%" mkdir "%RELEASE_DIR%"
if exist "%RELEASE_DIR%\%X64_NAME%" rmdir /s /q "%RELEASE_DIR%\%X64_NAME%"
if exist "%RELEASE_DIR%\%X64_NAME%.zip" del /q "%RELEASE_DIR%\%X64_NAME%.zip"
if exist "%RELEASE_DIR%\%X64_NAME%.exe" del /q "%RELEASE_DIR%\%X64_NAME%.exe"
if exist "%RELEASE_DIR%\%X64_NAME%-Setup.exe" del /q "%RELEASE_DIR%\%X64_NAME%-Setup.exe"

mkdir "%RELEASE_DIR%\%X64_NAME%\tools\iperf3\licenses"

if exist "build\bin\SanTianShell-amd64.exe" (
    copy /y "build\bin\SanTianShell-amd64.exe" "%RELEASE_DIR%\%X64_NAME%\SanTianShell.exe" >nul
) else (
    copy /y "build\bin\SanTianShell.exe" "%RELEASE_DIR%\%X64_NAME%\SanTianShell.exe" >nul
)
if errorlevel 1 exit /b %errorlevel%

if exist "build\bin\SanTianShell-amd64-installer.exe" (
    copy /y "build\bin\SanTianShell-amd64-installer.exe" "%RELEASE_DIR%\%X64_NAME%-Setup.exe" >nul
) else (
    copy /y "build\bin\SanTianShell-installer.exe" "%RELEASE_DIR%\%X64_NAME%-Setup.exe" >nul
)
if errorlevel 1 exit /b %errorlevel%

for %%A in (iperf3.exe cygwin1.dll THIRD-PARTY-NOTICES.txt) do (
    copy /y "%IPERF3_SOURCE%\%%A" "%RELEASE_DIR%\%X64_NAME%\tools\iperf3\%%A" >nul
    if errorlevel 1 exit /b %errorlevel%
)
for %%A in (iperf3-LICENSE.txt Cygwin-Licensing.html Cygwin-COPYING.LIB.txt) do (
    copy /y "%IPERF3_SOURCE%\licenses\%%A" "%RELEASE_DIR%\%X64_NAME%\tools\iperf3\licenses\%%A" >nul
    if errorlevel 1 exit /b %errorlevel%
)

tar.exe -a -c -f "%RELEASE_DIR%\%X64_NAME%.zip" -C "%RELEASE_DIR%" "%X64_NAME%"
exit /b %errorlevel%
