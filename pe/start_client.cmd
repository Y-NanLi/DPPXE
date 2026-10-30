@echo off
chcp 65001 >nul 2>nul
rem ===========================================================================
rem  DP-PXE client launcher (PE autostart)
rem  Called by Pecmd.ini / startnet.cmd.
rem
rem  NOTE: keep ALL comments ASCII. cmd.exe reads .cmd files using the ANSI
rem  codepage (936 on Chinese PE); UTF-8 Chinese in comments can be decoded into
rem  bytes like '|' and make cmd try to execute garbage commands.
rem
rem  PE has no WebView2, so we pass --gui which uses the native Win32 window
rem  (not Wails). If this PE cannot create a window, the client falls back to
rem  console mode automatically. Use --nogui to force console logging.
rem ===========================================================================
setlocal
set "APP=X:\Program Files\DP-PXEClient\DP-PXEClient.exe"

if not exist "%APP%" (
  echo [DPPXE] client not found: %APP%
  ping -n 5 127.0.0.1 >nul
  exit /b 1
)

rem wait for network (max ~60s). Keep this simple: no tasklist/pipe tricks,
rem they can hang on a slimmed-down PE and the client would never start.
set /a N=0
:waitnet
ipconfig | findstr /i "IPv4" >nul && goto run
set /a N+=1
if %N% GEQ 30 goto run
ping -n 2 127.0.0.1 >nul
goto waitnet

:run
echo [DPPXE] starting client...
rem "start" creates a fresh process with a clean STARTUPINFO, so the GUI is not
rem affected by any hidden/show flags of this console. Do NOT use /wait: the
rem client reconnects forever on its own, and leaving this cmd alive would keep
rem a black console window on the PE desktop. Exit immediately so only the GUI
rem remains.
start "" "%APP%" --gui
exit /b 0
