@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

rem ---------------------------------------------------------------------------
rem  DP-PXE (Go) 一键编译
rem  输出：
rem    bin\DP-PXE.exe        控制端图形界面（Wails + WebView2，无控制台窗口）
rem    bin\DP-PXEClient.exe  被控端（双击/--gui 出图形界面，--nogui 走控制台，PE 里用 --nogui）
rem
rem  注意：两个产物都必须带 -tags desktop,production，否则 Wails 的 runtime 是空壳，
rem        一调用 wails.Run 就会弹 "Wails applications will not build without the
rem        correct build tags"。
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

rem 被控端的原生界面（walk）依赖 comctl32 v6 视觉样式，必须把 manifest 嵌进 exe；
rem 否则 PE 里开窗口会直接失败（TTM_ADDTOOL failed）。rsrc.syso 已入库，正常不用重新生成。
if not exist "rsrc.syso" (
    echo == 生成程序集清单 rsrc.syso ==
    "%GO%" run github.com/akavel/rsrc@latest -manifest pe\DP-PXEClient.exe.manifest -o rsrc.syso -arch amd64 || goto :fail
)

echo == 编译图形界面版 ==
"%GO%" build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o bin\DP-PXE.exe . || goto :fail

echo == 编译被控端（图形界面 + 控制台，--nogui 走控制台）==
"%GO%" build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o bin\DP-PXEClient.exe . || goto :fail

echo.
echo 编译完成：
dir /b bin\*.exe
echo.
echo   双击 bin\DP-PXE.exe 启动控制端界面（需要 WebView2，Win10/11 自带）
echo   被控端：bin\DP-PXEClient.exe --client --server 10.0.0.133
echo          双击 = 图形界面；--nogui = 控制台（PE 里用）
echo   自检：  bin\DP-PXEClient.exe --selftest
exit /b 0

:fail
echo.
echo [X] 编译失败，请查看上面的错误输出。
exit /b 1
