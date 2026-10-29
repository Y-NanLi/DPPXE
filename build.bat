@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

rem ---------------------------------------------------------------------------
rem  DP-PXE (Go) 一键编译
rem  输出：
rem    bin\DP-PXE.exe        图形界面版（Wails + WebView2，无控制台窗口）
rem    bin\DP-PXEClient.exe  控制台版（被控端 / 模拟器 / 无界面服务端，PE 里用这个）
rem ---------------------------------------------------------------------------

set "GO=go"
if exist "%~dp0..\.tools\go\bin\go.exe" set "GO=%~dp0..\.tools\go\bin\go.exe"
"%GO%" version >nul 2>nul
if errorlevel 1 (
    echo [X] 找不到 Go 工具链。
    echo     请安装 Go 1.21+（https://golang.google.cn/dl/），
    echo     或把便携版解压到 ..\.tools\go，让 ..\.tools\go\bin\go.exe 存在。
    exit /b 1
)

set GOPROXY=https://goproxy.cn,direct
set GOSUMDB=off

echo == 拉取依赖 ==
"%GO%" mod tidy || goto :fail

echo == 编译图形界面版 ==
"%GO%" build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o bin\DP-PXE.exe . || goto :fail

echo == 编译控制台版（被控端 / 模拟器）==
"%GO%" build -trimpath -ldflags "-s -w" -o bin\DP-PXEClient.exe . || goto :fail

echo.
echo 编译完成：
dir /b bin\*.exe
echo.
echo   双击 bin\DP-PXE.exe 启动界面（需要 WebView2，Win10/11 自带）
echo   被控端：bin\DP-PXEClient.exe --client --server 10.0.0.133
echo   自检：  bin\DP-PXEClient.exe --selftest
exit /b 0

:fail
echo.
echo [X] 编译失败，请查看上面的错误输出。
exit /b 1
