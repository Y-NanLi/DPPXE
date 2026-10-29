@echo off
chcp 65001 >nul
setlocal
cd /d "%~dp0"

rem ===========================================================================
rem  把被控端 + 开机自启脚本注入 WinPE 镜像（用 wimlib，不需要管理员）
rem
rem    inject.bat                      :: 默认注入 ..\netboot\boot\win11x64.WIM
rem    inject.bat D:\path\to\xx.wim    :: 指定镜像
rem ===========================================================================

set "WIM=%~1"
if "%WIM%"=="" set "WIM=%~dp0..\netboot\boot\win11x64.WIM"

set "WIMLIB=%~dp0..\..\rebuild\tools\wimlib\wimlib-imagex.exe"
if not exist "%WIMLIB%" (
  echo [X] 找不到 wimlib: %WIMLIB%
  exit /b 1
)
if not exist "%WIM%" (
  echo [X] 找不到镜像: %WIM%
  exit /b 1
)

echo 镜像   : %WIM%
echo 被控端 : %~dp0..\bin\DP-PXEClient.exe
echo.

"%WIMLIB%" update "%WIM%" 1 < "%~dp0update_cmds.txt"
if errorlevel 1 (
  echo [X] 注入失败
  exit /b 1
)

echo.
echo 校验...
"%WIMLIB%" verify "%WIM%"
echo.
echo 完成。PE 开机后会自动运行：
echo   X:\DPPXE\start_client.cmd  -^>  X:\Program Files\DP-PXEClient\DP-PXEClient.exe --nogui
echo 触发方式：Pecmd.ini 末尾追加的一行（这个 PE 是 PECMD 启动的），
echo           另外也放了 startnet.cmd 兼容 winpeshl 启动的 PE。
