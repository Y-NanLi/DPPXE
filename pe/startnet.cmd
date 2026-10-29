@echo off
chcp 65001 >nul 2>nul
rem 由 winpeshl.exe 在 WinPE 启动时调用（若这个 PE 走 startnet 而不是 PECMD）
if exist "X:\DPPXE\start_client.cmd" (
  call "X:\DPPXE\start_client.cmd"
) else (
  if exist "X:\Program Files\DP-PXEClient\DP-PXEClient.exe" (
    "X:\Program Files\DP-PXEClient\DP-PXEClient.exe" --nogui
  )
)
