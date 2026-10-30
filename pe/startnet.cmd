@echo off
chcp 65001 >nul 2>nul
rem Called by winpeshl.exe at WinPE startup (only if this PE uses startnet, not PECMD).
rem Keep comments ASCII: cmd.exe reads .cmd files with the ANSI codepage, and UTF-8
rem Chinese can be decoded into bytes like '|', making cmd run garbage commands.
if exist "X:\DPPXE\start_client.cmd" (
  call "X:\DPPXE\start_client.cmd"
) else (
  if exist "X:\Program Files\DP-PXEClient\DP-PXEClient.exe" (
    "X:\Program Files\DP-PXEClient\DP-PXEClient.exe" --gui
  )
)
