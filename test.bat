@echo off
chcp 65001 >nul
cd /d "%~dp0"
if not exist "bin\DP-PXEClient.exe" (
    echo [X] 还没编译，先运行 build.bat
    pause
    exit /b 1
)
echo == 自检：协议 / 控制通道 / 文件分片 / DHCP / HTTP / TFTP / 脚本引擎 ==
bin\DP-PXEClient.exe --selftest
pause
