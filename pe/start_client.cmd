@echo off
chcp 65001 >nul 2>nul
rem ===========================================================================
rem  DP-PXE 被控端启动脚本（PE 开机自启）
rem  由 Pecmd.ini / startnet.cmd 调用：等网络就绪 → 拉起被控端（控制台模式）
rem  PE 里没有 WebView2，所以用 --nogui；连不上会自动重连，进程退了会自动重启
rem ===========================================================================
setlocal
set "APP=X:\Program Files\DP-PXEClient\DP-PXEClient.exe"

if not exist "%APP%" (
  echo [DPPXE] 找不到被控端：%APP%
  ping -n 5 127.0.0.1 >nul
  exit /b 1
)

rem 等网络就绪（最多约 60 秒）
set /a N=0
:waitnet
ipconfig | findstr /i "IPv4" >nul && goto run
set /a N+=1
if %N% GEQ 30 goto run
ping -n 2 127.0.0.1 >nul
goto waitnet

:run
echo [DPPXE] 启动被控端…
"%APP%" --nogui
rem 进程退出（异常/被关）后 3 秒重启，保证 PE 里一直在线
ping -n 4 127.0.0.1 >nul
goto run
