@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

rem ---------------------------------------------------------------------------
rem  DP-PXE (Go) 一键编译 —— 同时产出 x64 / x86 两套
rem  输出：
rem    bin\x64\DP-PXE.exe        控制端（Windows: Wails+WebView2；PE: walk 原生界面）
rem    bin\x64\DP-PXEClient.exe  被控端（PE 原生界面 + 已装系统 Wails 界面）
rem    bin\x86\...               同上，32 位版本（给 32 位系统用）
rem
rem  注意：两个产物都必须带 -tags desktop,production，否则 Wails 的 runtime 是空壳，
rem        一调用 wails.Run 就会弹 "Wails applications will not build without the
rem        correct build tags"。
rem        控制端另加 servergui（PE 原生界面）、被控端另加 clientgui。
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
set CGO_ENABLED=0
set GOOS=windows

echo == 拉取依赖 ==
"%GO%" mod tidy || goto :fail

rem 原生界面（walk）依赖 comctl32 v6 视觉样式，必须把 manifest 嵌进 exe，
rem 否则 PE 里开窗口直接失败（TTM_ADDTOOL failed）。
rem Go 会按 GOARCH 自动挑选 rsrc_amd64.syso / rsrc_386.syso，两个都要有。
rem 注意：必须同时带 -ico，否则重新生成会把 exe 的文件/任务栏图标丢掉。
if not exist "rsrc_amd64.syso" (
    echo == 生成 x64 资源 rsrc_amd64.syso ==
    "%GO%" run github.com/akavel/rsrc@latest -manifest pe\DP-PXEClient.exe.manifest -ico assets\app.ico -o rsrc_amd64.syso -arch amd64 || goto :fail
)
if not exist "rsrc_386.syso" (
    echo == 生成 x86 资源 rsrc_386.syso ==
    "%GO%" run github.com/akavel/rsrc@latest -manifest pe\DP-PXEClient.exe.manifest -ico assets\app.ico -o rsrc_386.syso -arch 386 || goto :fail
)

if not exist "bin\x64" mkdir "bin\x64"
if not exist "bin\x86" mkdir "bin\x86"

echo == 编译 x64 控制端 ==
set GOARCH=amd64
"%GO%" build -tags "desktop,production,servergui" -trimpath -ldflags "-s -w -H windowsgui" -o bin\x64\DP-PXE.exe . || goto :fail
echo == 编译 x64 被控端 ==
"%GO%" build -tags "desktop,production,clientgui" -trimpath -ldflags "-s -w -H windowsgui" -o bin\x64\DP-PXEClient.exe . || goto :fail

echo == 编译 x86 控制端 ==
set GOARCH=386
"%GO%" build -tags "desktop,production,servergui" -trimpath -ldflags "-s -w -H windowsgui" -o bin\x86\DP-PXE.exe . || goto :fail
echo == 编译 x86 被控端 ==
"%GO%" build -tags "desktop,production,clientgui" -trimpath -ldflags "-s -w -H windowsgui" -o bin\x86\DP-PXEClient.exe . || goto :fail

echo.
echo 编译完成：
dir /b bin\x64\*.exe
dir /b bin\x86\*.exe
echo.
echo   x64：双击 bin\x64\DP-PXE.exe 启动控制端（需要 WebView2，Win10/11 自带）
echo   x86：bin\x86\DP-PXE.exe，给 32 位系统用（需要 32 位 WebView2 运行时）
echo   被控端：DP-PXEClient.exe --client --server 10.0.0.133
echo          双击 = 图形界面；--nogui = 控制台（PE 里用）
echo   自检：  DP-PXEClient.exe --selftest
exit /b 0

:fail
echo.
echo [X] 编译失败，请查看上面的错误输出。
exit /b 1
