@echo off
chcp 65001 >nul
cd /d "%~dp0"
if not exist "bin\DP-PXE.exe" (
    echo [X] 还没编译，先运行 build.bat
    pause
    exit /b 1
)
start "" "bin\DP-PXE.exe"
