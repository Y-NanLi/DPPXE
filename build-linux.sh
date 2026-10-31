#!/usr/bin/env bash
# ---------------------------------------------------------------------------
#  DP-PXE (Go) Linux 一键编译
#
#  输出：bin/dp-pxe-linux       控制端（DHCP / TFTP / HTTP / 控制通道 / VNC 查看器）
#
#  用法：
#    ./build-linux.sh              # 编 amd64
#    ./build-linux.sh arm64        # 编 arm64
#    ./build-linux.sh both         # 两个架构都编
#
#  说明：
#    - Linux 版只含控制端，被控端（DP-PXEClient）仍然只在 Windows / PE 里跑。
#    - 不带 CGO：控制端默认走浏览器界面（--web / 默认），不依赖 webkit2gtk。
#    - 交叉编译也能用：在 Windows 上执行
#        GOOS=linux GOARCH=amd64 go build -o bin/dp-pxe-linux .
# ---------------------------------------------------------------------------
set -euo pipefail
cd "$(dirname "$0")"

GO="${GO:-go}"
if [ -x "../.tools/go/bin/go" ]; then
    GO="../.tools/go/bin/go"
fi
if ! "$GO" version >/dev/null 2>&1; then
    echo "[X] 找不到 Go 工具链，请安装 Go 1.21+ 或把便携版放到 ../.tools/go" >&2
    exit 1
fi

export CGO_ENABLED=0
export GOOS=linux
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
export GOSUMDB="${GOSUMDB:-off}"

mkdir -p bin

build() {
    local arch="$1"
    local out="bin/dp-pxe-linux"
    [ "$arch" = "amd64" ] || out="bin/dp-pxe-linux-$arch"
    echo "== 编译 Linux/$arch =="
    GOARCH="$arch" "$GO" build -trimpath -ldflags "-s -w" -o "$out" .
    printf '   %s  %s\n' "$(du -h "$out" | cut -f1)" "$out"
}

case "${1:-amd64}" in
    both)
        build amd64
        build arm64
        ;;
    amd64|arm64)
        build "$1"
        ;;
    *)
        echo "用法: $0 [amd64|arm64|both]" >&2
        exit 2
        ;;
esac

cat <<'EOF'

编译完成。运行方式（控制端需要 root：DHCP 67 / TFTP 69 / HTTP 80 都是特权端口）：

  sudo ./bin/dp-pxe-linux                 # 起服务 + 浏览器打开界面（127.0.0.1:8788）
  sudo ./bin/dp-pxe-linux --nogui         # 无界面模式，适合 systemd 托管
  sudo ./bin/dp-pxe-linux --nogui --ip 192.168.1.10
  ./bin/dp-pxe-linux --selftest           # 自检（不需要 root）

可选依赖：
  tcpdump      抓包诊断（没有则「抓包诊断」不可用）
  zenity 或 kdialog   界面上的「选择文件夹/文件」对话框
  firewalld 或 ufw    一键放行端口（没有则提示手动配置）
EOF
