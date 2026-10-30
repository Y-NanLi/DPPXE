# DP-PXE —— Go 重构版（v1.5）

[![License: GPL v3](https://img.shields.io/badge/License-GPLv3-blue.svg)](LICENSE)
[![Version](https://img.shields.io/badge/version-1.5-brightgreen.svg)](https://github.com/Y-NanLi/DPPXE/releases)
[![Platform](https://img.shields.io/badge/platform-Windows%2010%2F11-lightgrey.svg)](#系统要求)

**DP-PXE** 是一个 Windows 平台的 **PXE 网启 + 批量装机** 服务端 / 被控端工具：
一套程序同时提供 DHCP、TFTP、HTTP、控制通道与图形界面，把客户机拉进 WinPE 后
完成分区、还原镜像、注入驱动、修复引导等操作。

- 仓库地址：<https://github.com/Y-NanLi/DPPXE>
- 下载发布版：<https://github.com/Y-NanLi/DPPXE/releases>
- 许可证：**GNU GPL v3.0**（见 [LICENSE](LICENSE)）
- 交流群（HHXS 项目 QQ 群）：**103494722**

> 本项目是根据 **HHXSPxe** 仿照改写的，编写主力是 DeepSeek。
> 本版本可能存在未知 bug，如无法使用请到 HHXS 项目群下载稳定版本。

---

## 目录

- [1. 这是什么](#1-这是什么)
- [2. 快速开始](#2-快速开始)
- [3. 目录结构](#3-目录结构与-python-版对照)
- [4. 界面](#4-界面)
- [5. 验证结果](#5-验证结果)
- [6. 与 Python 版的差异](#6-与-python-版的差异)
- [7. 网启链路](#7-网启链路与-python-版一致)
- [8. 常见问题](#8-常见问题)
- [9. 部署到 WinPE](#9-部署到-winpefyboot--被控端已内置)
- [10. 许可证](#10-许可证)
- [11. 致谢与免责声明](#11-致谢与免责声明)

---

## 1. 这是什么

把 `rebuild/`（Python + PySide6 版）按模块一一对应地重写成 Go，
界面改用 **Wails v2 + WebView2**（前端是手写的 HTML/CSS/JS，没有 npm 依赖），
编译产物是单个 exe，不依赖 Python / PySide6 / 任何运行库。

`netboot/`（引导文件）与 `AutoCommand/`（自动安装脚本）已并入工程 `go/` 目录下，
本程序只负责「发什么、怎么发」——DHCP / TFTP / HTTP / 控制通道全部真实实现。

**核心能力**

| 能力 | 说明 |
| --- | --- |
| 网启 | DHCP（含代理模式）/ TFTP / iPXE 菜单 / HTTP Range 分发 WIM |
| 装机 | 高级分区（GPT/MBR、ESP/MSR、主分区逐行勾选）、还原/备份镜像、流式安装、注入驱动 |
| 控制 | 远程 CMD、发送文件（分片 + 真实进度）、自动化脚本引擎、WOL 开机、重启/关机 |
| 运维 | 内置 VNC 查看器（无需外部 viewer）、客户端列表台账、抓包诊断（pktmon） |
| 交付 | 注入被控端 / VNC 到目标系统并设开机自启，PE 镜像可反复注入升级 |

### 系统要求

- Windows 10 / 11（x64）
- WebView2 Runtime（Win11 自带；没有就用 `--web` 走浏览器模式）
- **管理员权限**：67 / 69 / 80 是特权端口，跑完整服务必须管理员
- 客户机需支持 PXE 网卡启动（UEFI 或 Legacy BIOS）

---

## 2. 快速开始

### 2.1 直接用发布版（推荐）

到 [Releases](https://github.com/Y-NanLi/DPPXE/releases) 下载最新版，每个版本附带 4 个产物：

| 产物 | 用途 |
| --- | --- |
| `DP-PXE.exe` | 控制端（图形界面），双击即用 |
| `DP-PXEClient.exe` | 被控端 / 模拟器 / 无界面服务端（**PE 里用这个**） |
| `netboot.zip` | PXE 引导资源（EFI/BIOS/iPXE/BCD/菜单等） |
| `AutoCommand.zip` | 自动安装脚本示例 |

> ⚠️ **PE 镜像需自备**：`netboot/boot/win11x64.WIM` 体积约 600MB，超出 GitHub
> 单文件 100MB 上限，**不在仓库和 zip 内**。请自行制作/获取 PE 镜像放到
> `netboot/boot/win11x64.WIM`，详见 [netboot/README.md](netboot/README.md)。

### 2.2 从源码编译

```bat
build.bat      :: 一键编译（优先用 ..\.tools\go 里的便携版 Go 1.27.3，没有则用 PATH 里的 go）
run.bat        :: 启动图形界面
test.bat       :: 内置自检（33 项）
```

产物：

| 文件 | 子系统 | 用途 |
| --- | --- | --- |
| `bin\DP-PXE.exe` | GUI（`-H windowsgui`） | 图形界面服务端，双击即用 |
| `bin\DP-PXEClient.exe` | 控制台 | 被控端 / 模拟器 / 无界面服务端（**PE 里用这个**） |

两者是同一份代码，参数不同角色不同：

```bat
bin\DP-PXE.exe                                     :: 图形界面
bin\DP-PXEClient.exe --nogui --root D:\wb\h-go\go\netboot  :: 无界面跑全部服务（PE / 服务器）
bin\DP-PXEClient.exe --web --web-port 8788         :: 不开窗口，浏览器里用界面（WebView2 缺失时兜底）
bin\DP-PXE.exe       --client --gui --server 10.0.0.133  :: 被控端（图形界面）
bin\DP-PXEClient.exe --client --server 10.0.0.133          :: 被控端（控制台，PE 用）
bin\DP-PXEClient.exe --sim    --server 10.0.0.133  :: 客户端模拟器（只记录不执行）
bin\DP-PXEClient.exe --selftest                    :: 自检
bin\DP-PXEClient.exe --client --install --server 10.0.0.133   :: 装成开机自启（SYSTEM）
bin\DP-PXEClient.exe --client --uninstall
```

> 67 / 69 / 80 是特权端口，**必须管理员运行**。没有管理员权限时程序照常打开，
> 只有对应服务启动失败并写进日志；控制通道（6003）不受影响。

---

## 3. 目录结构（与 Python 版对照）

```
go/
├─ main.go                     入口：GUI / --nogui / --web / --client / --sim / --selftest
├─ selftest.go                 内置自检（33 项）
├─ build.bat / run.bat / test.bat
├─ internal/
│  ├─ config/config.go         ← app/config.py        配置模型 + 网段推导 + 自检 + 自动迁移旧配置
│  ├─ protocol/protocol.go     ← app/protocol.py      296 字节头 + 操作码 + 收发封装（逐字节兼容）
│  ├─ tasks/tasks.go           ← app/tasks.py         DiskPart 脚本 / bcdboot / 流式参数 / 脚本规整
│  ├─ services/
│  │  ├─ dhcp.go               ← services/dhcp.py     DHCP + 代理 DHCP（option 66/67/60/43/175）
│  │  ├─ tftp.go               ← services/tftp.py     TFTP（RFC1350 + blksize/tsize）
│  │  ├─ httpsrv.go            ← services/httpsrv.py  HTTP（Range 206 + 流量统计）
│  │  ├─ control.go            ← services/control.py  控制/文件服务（TCP 6003，会话 + 拉模式分片）
│  │  ├─ discovery.go          ← services/discovery.py  UDP 6004 服务发现
│  │  ├─ wol.go                ← services/wol.py      WOL 魔术包
│  │  ├─ firewall.go           ← services/firewall.py netsh 放行规则
│  │  └─ capture.go            ← services/capture.py  pktmon 抓包 + pcap/pcapng 解析
│  ├─ server/                   ← app/ui/mainwindow.py 里的业务逻辑（设备台账 / 下发 / 右键动作）
│  │  ├─ server.go             服务编排 + 设备台账 + 日志环形缓冲
│  │  ├─ api.go                给界面的命令表（等价于八个页签的按钮）
│  │  ├─ dialogs_windows.go    文件夹/文件/另存为 对话框（PowerShell + WinForms）
│  │  └─ admin_windows.go      管理员检测
│  ├─ webui/                    ← app/ui/（theme + widgets + tabs + mainwindow 的界面部分）
│  │  ├─ webui.go              内嵌资源 + /api/state、/api/cmd
│  │  └─ dist/                 index.html / style.css / app.js（八个页签全在这里）
│  ├─ vnc/                     内置 RFB 客户端（CopyRect/Hextile/Raw，无第三方库）
│  └─ client/                   ← client/（被控端）
│     ├─ client.go             连接 / 握手 / 心跳 / 重连 / 指令分发 / 文件拉取
│     ├─ engine.go             脚本引擎（ALL/PE/SYS、变量、GOTO、IF、FOR、WHILE + 全部指令）
│     ├─ win.go                Win32 磁盘枚举（DeviceIoControl）+ 网卡 MAC
│     ├─ letters.go            diskpart 盘符冲突处理（腾出 / 重映射）
│     ├─ service.go            安装/卸载（计划任务 + Run 键）
│     └─ sim.go                客户端模拟器（控制台）
├─ netboot/                    PXE 引导资源（WIM 不入库，见 netboot/README.md）
├─ AutoCommand/                自动安装脚本示例
├─ assets/                     icon-src.webp（源图）+ app.ico（多尺寸图标，由 icongen 生成）
├─ cmd/icongen/                图标转换小工具（webp/png → 多尺寸 ico）
├─ rsrc.syso                   内嵌资源（comctl32 清单 + 图标），必须入库
├─ HANDOFF.md                  开发交接说明（架构约定 / 踩过的坑 / 待办）
└─ bin/                        编译产物
```

**架构上的一处变化**：Python 版是「Qt 控件直接被后台线程回调」，Go 版改成
「后台服务只维护状态 + 环形日志，界面每 500ms 拉一次 `/api/state`」。
好处是彻底没有跨线程改 UI 的崩溃风险（Python 版为此专门加了 4 个 Signal），
而且同一套前端既能跑在 WebView2 里，也能跑在浏览器里。

---

## 4. 界面

八个页签、顶部三列、底部状态灯与日志，全部按 Python 版 1:1 复刻：

| 页签 | 功能 |
| --- | --- |
| DHCP 设置 | 地址池 / 掩码 / 网关 / DNS / 租约 + 快速响应、安全引导、代理 DHCP |
| 远程CMD命令 | `SHELL` / `SHELLWAIT`，勾"等待执行完成"则阻塞并回传全部输出 |
| 发送文件 | 保存路径 + 文件/文件夹列表 + 单播/P2P + 真实分片进度条 |
| 高级分区 | 磁盘列表（读被控端上报的真实磁盘）、分区数目、GPT/MBR、ESP/MSR、MBR 主分区逐行勾选、实时 DiskPart 预览 |
| 系统安装 | 还原(WIM/ESD+卷+压缩)、备份(增量)、注入驱动、智能分区、重建主引导、引导修复、注入应答 |
| 流式安装 | 镜像流地址 + 8 个开关 + 硬盘号，生成 `STREAMINSTALL` 参数串 |
| 自动化指令 | 脚本编辑器 + 模板 + 前缀校验 + zlib 压缩下发 |
| 其它设置 | 重启客户端、注入客户端/VNC/PE驱动、下次启动、改名、关机/重启/WOL、清缓存、保存设置 |

顶部：服务端 IP 下拉、5 个服务开关、「仅仅选择中客户端接收指令」、引导文件配置、
客户端表（编号/IP/MAC/状态/网卡/根/引导）。

客户端表右键菜单：**连接 VNC（内置查看器）**、用外部 Viewer 连接、查看信息、
修改编号和 IP、从列表删除、远程重启/关机/开机、重启客户端进程、
重启/关机/开机所有客户端、保存客户端列表。

### 4.1 被控端界面（`--client --gui`）

对齐原版 DP-PXEClient：无边框窗口 + 34px 蓝色渐变标题栏（左侧 H 徽标、标题、右侧最小化/关闭），
下方是大字**机器号**（未连接红色「未知」，连上后绿色 `003`）、右上 84px **圆形传输进度**、
两行信息（客户端IP/版本、服务端IP/版本）、带序号和时间戳的日志列表，底部右侧是版本署名。

```
bin\DP-PXE.exe --client --gui --server 10.0.0.133   :: 图形界面被控端
:: 或菜单「☰ → 启动被控端（本机 · 图形界面）」
:: 需要看滚动日志 / 在 PE 里跑：菜单「启动被控端（本机 · 控制台）」或 --client（不带 --gui）
```

- 窗口是无边框的：标题栏整条可拖动（`--wails-draggable: drag`），双击标题栏最大化/还原，
  最小化/关闭按钮通过 `/api/client/cmd` 调 Wails runtime。
- 界面每 400ms 拉 `/api/client/state?since=<seq>`，只增量追加日志，不重复刷屏。
- 握手成功、文件传输、断线重连的进度都会实时反映到圆环和机器号颜色上。
- 界面上能看到「操作了什么命令」：`收到指令 <OP>` → `执行指令 <指令名> <参数>` → `> <实际命令行>`。
- 右下角显示 `Wimlib Ver:x.x`（启动时异步探测 `wimlib-imagex --version`）。

### 4.1.1 PE 里的原生界面（v1.4 新增，不依赖 WebView2）

PE 没有 WebView2，Wails 一旦调用就会弹「The WebView2 runtime is required」的模态框，
不点掉进程卡死、被控端等于完全离线。所以 **PE 里换了一套实现**：

| 项 | 说明 |
| --- | --- |
| 实现 | `internal/client/uigui_windows.go`，用 [walk](https://github.com/lxn/walk)（纯 user32/gdi32 封装）自绘 700×430 窗口，**零运行时依赖**、仍是单 exe |
| 内容 | 大字机器号（未连接红色 / 连接后绿色）、进度百分比、客户端/服务端 IP 与版本、只读滚动日志（带时间戳）、右下角 wimlib 版本 |
| 刷新 | 每 400ms 取一次客户端状态 |
| 兜底 | 若这台 PE 开不了窗口（无桌面 / 被精简），自动退回控制台并打印原因 |

> ⚠️ **必须内嵌 manifest**：walk 依赖 comctl32 v6 视觉样式，Go 编译的 exe 默认没有清单，
> 初始化控件会直接失败（`TTM_ADDTOOL failed`）。外部 `<exe>.manifest` 在 Win10 上不生效，
> 必须用 `rsrc -manifest pe/DP-PXEClient.exe.manifest -o rsrc.syso -arch amd64` 内嵌
> （`rsrc.syso` 已入库，`build.bat` 缺失时会自动生成）。

### 4.2 内置 VNC 查看器（不再依赖外部 tvnviewer）

右键客户端 →「连接 VNC <IP>（内置）」，控制端窗口里直接出现被控端画面，
不用再拉起 `tvnviewer.exe`。实现是自带的 RFB 客户端（`internal/vnc`，约 700 行，无第三方库）：

| 能力 | 说明 |
| --- | --- |
| 协议 | RFB 3.3 / 3.7 / 3.8 握手；安全类型 None 与 VNC 认证（DES 挑战/响应） |
| 编码 | CopyRect / Hextile / Raw，外加 DesktopSize 伪编码（分辨率变化自动重适配） |
| 像素格式 | 统一协商成 32bpp RGBA，Go 侧维护整屏画布，只把**脏矩形**推给前端 |
| 输入 | 鼠标（左/中/右键 + 滚轮）、键盘（X11 keysym，含 F1-F12、方向键、修饰键） |
| 传输 | `GET /api/vnc/frame` 返回二进制帧：`u32 seq + u16 矩形数 + 每块 (x,y,w,h,len) + RGBA` |
| 界面 | 顶部工具栏：适应窗口 / Ctrl+Alt+Del / 全屏 / 断开 / 关闭；Esc 关闭 |

实测（本机 TightVNC 服务端 1600×900）：首帧 5700 个 16×16 瓦片、覆盖 1,440,000 像素，
0.3 秒内出画面；之后的增量帧只有 15–25 KB。

- 密码：服务端设了密码时会弹框输入，填一次记到 `config.json` 的 `vnc_password`。
- PE 里由被控端自动拉起 `tvnserver`，并**先放行防火墙、再启动**（不再弹「已阻止此应用」）；
  PE 场景默认配成无密码直连。
- 需要外部 viewer 时仍保留「用外部 Viewer 连接」，走原来的 tvnviewer/vncviewer 查找逻辑。
- 未实现：Tight/ZRLE 编码（局域网用 Hextile 足够）、剪贴板同步、文件传输、多显示器切换。

状态判定与 Python 版一致：**在线**（6003 会话存在，绿色加粗）、
**PXE**（90 秒内发过 DHCP，橙色）、**离线**（灰色，记录保留，悬停看最后在线时间）。

---

## 5. 验证结果

```
bin\DP-PXEClient.exe --selftest
结果：33 通过 / 0 失败
```

覆盖：协议 296 字节头往返（含中文/超长截断）、zlib 兼容、DiskPart 脚本关键行（先格式化、盘符 `noerr` 收尾）、
MBR 扩展分区与主分区收敛、脚本规整、流式 11 参数位、DHCP DISCOVER 解析（EFI/iPXE/畸形包）、
控制通道（HELLO_ACK / 心跳 / FILE_SEED + 分片拉取 1.05MB / 磁盘上报 / 广播 / 重连复用编号）、
HTTP（200 / Range 206 / 404）、TFTP（RRQ 传完 1216B）、脚本引擎（变量、单行 IF、FOR 循环、
UNCOMPRESS 解析、SETVAR）。

`go test ./...` 另含单元测试：`internal/client`（含 `TestRunHandlesQuotedPath` 带空格路径回归、
`TestDiskpartOpenFailed`）、`internal/tasks`（`TestBuildDiskpartMBRPrimaryCount`）、
`internal/vnc`、`internal/webui`。

端到端（真实进程 + 真实 API）：

```
服务端 --web → 启动服务（HTTP + 文件服务）
被控端 --client → 握手 #001、上报真实磁盘 HD0 ST1000VX013 932GB GPT
推送 4 个文件（含 1.5MB / 6 分片）→ 全部 MD5 一致
下发自动化脚本 → ALL 行执行、PE 行按环境跳过、SYS 行执行
远程 CMD → 实时回传 "DPPXE-GO-OK" 与系统版本行
分区页预览 → 生成 26 行 DiskPart 脚本
```

---

## 6. 与 Python 版的差异

| 项 | 说明 |
| --- | --- |
| 被控端界面 | **已移植**（`--client --gui`，见 4.1）。**PE 里另有一套原生界面**（walk / user32+gdi32，不依赖 WebView2，见 4.1.1）；`--nogui` 仍可强制走控制台。 |
| P2P 分发 | 与 Python 版一样简化成「服务端拉模式单播」，界面上保留单播/P2P 选项但行为相同。 |
| 注入类操作 | **在被控端真实执行**：注入客户端 / VNC 会复制文件到目标盘 `C:\Windows\System32`，并用 `reg load` 写目标盘离线 `SOFTWARE` hive 的 `Run` 键实现开机启动（早期版本只写 PE 自身注册表，重启即失效，已修）。 |
| 抓包 | 依赖系统自带 `pktmon`（Win10 1809+），行为与 Python 版一致；非管理员不会自动开。 |
| 配置 | 首次启动会自动读取 `%APPDATA%\HHXSPxeRebuild\config.json` 并迁移到 `%APPDATA%\DPPXEGo\config.json`，字段名完全相同。 |
| 控制通道 | 与 Python 版**逐字节兼容**（296 字节头、UTF-16LE 文本、zlib 脚本），两边的被控端可以互换使用。 |

---

## 7. 网启链路（与 Python 版一致）

```
DHCP(67) 派 IP + option66/67/43/175
   → TFTP(69) 传 BootX64.EFI / pxeboot.n12（安全引导）或 ipxe.efi / IPXE.BIOS
   → iPXE 执行 menu.txt（${next-server} 现场替换成服务端 IP）
   → HTTP(80) 拉 wimboot + BCD + boot.sdi + *.WIM（支持 Range 206）
   → wimboot 在内存里拼 ramdisk → 进 WinPE
```

- 按网卡分别绑定 67（每块网卡一个 socket），回包源 IP 正确；
- 同时监听 4011（PXE Boot Server），避免部分 PXE ROM 拿到 IP 后放弃；
- 代理 DHCP 模式 `yiaddr=0`，只补引导选项，不动现有 DHCP；
- TFTP 支持 `blksize=65464` 与 `tsize`，按块流式读文件。

---

## 8. 常见问题

**⓪ 被控端启动不起来 / 双击没反应**

被控端有四种启动方式，任选一种：

```bat
:: ① 菜单「☰ → 启动被控端（本机 · 图形界面）」——打开被控端窗口
:: ② 手动指定服务端（图形界面）
bin\DP-PXE.exe --client --gui --server 10.0.0.133
:: ③ 控制台模式（PE / 只看滚动日志）
bin\DP-PXEClient.exe --client --server 10.0.0.133
:: ④ 双击 DP-PXEClient.exe（不带参数 = 图形界面 + 自动广播发现服务端）
```

- 启动后会先打印一行 `DP-PXE 被控端 1.4 启动（主机名，系统/PE）→ 服务端 x.x.x.x:6003`，
  连不上会每 3→6→12→24→30 秒自动重连，并打印具体原因（服务端没开 / 防火墙 / IP 不对）。
- 用 `DP-PXE.exe --client` 也可以，程序会自己补一个控制台窗口（GUI 子系统本来没有 stdout）。
- 老版本菜单传的 `--gui` 参数现在会被忽略，不会再导致参数解析失败退出。

**① 双击没反应 / 白屏**
机器没装 WebView2。用 `DP-PXEClient.exe --web` 走浏览器模式，或装一下
[WebView2 Runtime](https://developer.microsoft.com/microsoft-edge/webview2/)。

**② 启动服务只起来几项**
看日志：`端口 67(UDP) 已被占用或权限不足` → 右键 exe「以管理员身份运行」；
`80 端口被占用` → 关掉 IIS / 改 `config.json` 的 `http_port`。

**③ TFTP 404 ipxe.efi**
「相对根目录」不对。点 `…` 选到真正的 netboot（里面有 `menu.txt`、`ipxe.efi`、`IPXE\`、`boot\`）。
程序启动时会在 exe 所在目录的上级 `netboot`（`..\netboot`）等位置自动找一次。

**④ 客户机拿不到 IP / 引导不动**
「选择服务端IP」要选与客户机同网段的网卡；地址池默认按该 IP 前三段推导。
NAT 网络下必须开「代理DHCP」（vmnet8 自带 DHCP 会抢答）。

**④.5 分区只出来一个 / 其它分区异常**

diskpart 日志里出现：

```
虚拟磁盘服务错误:
指定的驱动器号对于分配不可用。
```

说明脚本要用的盘符（C/D/E）被光驱、U 盘或旧分区占着。**diskpart 遇到这个错误会中止整个脚本**，
所以后面分区、格式化全都不执行 —— 表现就是"选了 3 个分区，只出来 1 个"。

现在的处理（v1.3）：

1. **脚本结构**：先 `create` + `format`，所有分区处理完，**最后统一** `assign letter=X noerr`。
   即使某个盘符分配失败，也不会影响前面的分区与格式化（`noerr` 保证不中断脚本）。
2. **被控端预处理**（`internal/client/letters.go`）：只针对脚本真正要用的那几个盘符先 `remove letter` 腾出来；
   腾不掉的（多半是光驱）尝试改挂到靠后的字母；还不行就自动改用其它空闲盘符，并把脚本里的
   `assign letter` 一起改掉。
3. **不再生成卷标**（`label` 命令已移除），所以不会再有"卷标段跟着盘符变"的问题。

日志里会写明结果，例如：

```
分区前先腾出盘符：C D
  盘符 D: 无法移除（虚拟磁盘服务错误: 指定的驱动器号对于分配不可用。），将改用其它空闲盘符
盘符冲突：数据分区改用 C/E/F（原计划 C/D/E）
```

> 改了盘符之后，「系统安装」页的"指定安装分区"记得选对应的那个（例如 E://）。

**④.6 分区报「DiskPart 无法打开或读取脚本文件」**

```
DiskPart 无法打开或读取脚本文件。
请确定你指定的文件是否存在。
diskpart 返回 2147500037
```

**真因（v1.0 起已修）**：`Engine.Run()` 用 `exec.Command("cmd", "/c", cmdline)` 执行命令，
Go 拼接 Windows 命令行时会把引号转义成 `\"`，而 cmd.exe 不认反斜杠转义，
于是带空格/引号的路径（`diskpart /s "X:\Windows\Temp\x.txt"`）被吃坏，diskpart 自然读不到脚本。

修复方式：

- `internal/client/win.go` 新增 `hideWindowCmdLine()`，用 `SysProcAttr.CmdLine` 把**原样命令行**
  交给 CreateProcess，绕过 Go 的引号转义；
- `runDiskpartText()` 增加兜底：若 diskpart 明确报「打不开 / 读不到脚本文件」
  （说明脚本没执行，重跑安全），自动改用 **stdin 管道**喂脚本，完全不依赖脚本文件；
- 写脚本的目录仍按优先级回退：`%TEMP%` → `%SystemRoot%\Temp`（PE 里一定有）→ 程序目录 → 系统临时目录 → 当前目录，
  并回读确认非空。

**④.7 「重建主引导记录(MBR)」为什么是灰的**

它只对 **MBR 分区表**有效，所以「分区表类型」选 GPT 时这一项自动禁用（ESP/MSR 反过来，选 MBR 时禁用）。
这不是 bug，是跟原软件一致的行为：

- 要重建 MBR 引导记录 → 分区表切到 **MBR**，复选框就会亮；
- GPT 盘要修引导 → 用「系统安装」页的**引导修复**，它会自动找 ESP 写引导文件。

**④.8 引导修复后还是进网络 PE / 一直 PXE**

v1.3 起引导修复的逻辑是：

- **不再格式化引导分区**（之前会先 `Format C:\`，等于把刚装好的系统盘格式化）；
- `bcdboot` **不带 `/f`、不带 `/s`** —— `/f` 必须与 `/s` 一起用，单独 `/f ALL` 是非法参数（返回 87），
  而 ESP 在 PE 里通常没有盘符、加不了 `/s`。让 bcdboot 用"固件标识的系统分区"（UEFI 下就是 ESP）最可靠；
- UEFI 追加 `bcdedit /set {fwbootmgr} displayorder {bootmgr} /addfirst`，
  把 Windows Boot Manager 提到固件启动顺序最前；Legacy 追加 `bootsect /nt60 SYS /mbr`；
- 界面的「引导分区」改为**跟随固件自动检测**（UEFI = 自动检测 ESP，Legacy = 系统分区），不再手动选盘符。

> `bcdedit /set {fwbootmgr}` 在 PE 里常报「无法打开启动配置数据存储」，失败不影响 bcdboot 写引导文件。
> 若仍从 PXE 启动，请进 BIOS 把硬盘调到启动顺序第一位。

**⑤ 想看包**
菜单「开始抓包诊断」（需管理员），停止后自动把 DHCP/TFTP/HTTP 时间线写进日志，
产物是 `bin\logs\run.pcap`，可直接拖进 Wireshark。

**⑥ 编译**
`build.bat` 会优先用 `..\.tools\go\bin\go.exe`（本项目自带的便携版 Go 1.27.3），
没有就退回 PATH 里的 `go`。依赖走 `GOPROXY=https://goproxy.cn`。

---

## 9. 部署到 WinPE（fyboot + 被控端已内置）

> 本程序只负责 `netboot` 这个目录（Go 版配置里的「相对根目录」就指向它）。
> **仓库里没有 `boot\win11x64.WIM`**（约 600MB，超 GitHub 单文件上限），
> 克隆后请按 [netboot/README.md](netboot/README.md) 自行放入 PE 镜像。

`boot\win11x64.WIM` 的内容建议用 **fyboot.wim（飞雨 Win11PE）**，
再把 Go 版被控端注入进去、设成开机自启。

**为什么文件名还叫 win11x64.WIM**：`boot\BCD` 里写死了 `\boot\win11x64.wim`、
`menu.txt` 里写死了 `boot/Win11x64.wim`，改名就得同时改 BCD（二进制）。所以做法是
「保留文件名、替换内容」，BCD 和 iPXE 菜单都不用动。

镜像里新增/修改的文件：

| 路径 | 作用 |
| --- | --- |
| `\Program Files\DP-PXEClient\DP-PXEClient.exe` | Go 版被控端本体 |
| `\DPPXE\start_client.cmd` | 等网络就绪 → `start "" DP-PXEClient.exe --gui` 后立即退出（不留黑框；客户端自身断线重连） |
| `\Windows\System32\Pecmd.ini` | 末尾追加一行 `EXEC %SystemRoot%\System32\cmd.exe /c X:\DPPXE\start_client.cmd`（这个 PE 是 PECMD 启动的，这条才是真正生效的） |
| `\Windows\System32\startnet.cmd` | 兜底：万一某台机器走 winpeshl 启动也能拉起 |
| `\Windows\System32\wimlib-imagex.exe`、`libwim-15.dll` | 还原镜像用（必须在 PATH 里，否则 wimlib 调不到） |
| `\Program Files\DP-PXEClient\Tool\TightVNC\Amd64\tvnserver.EXE` | VNC 服务端（由被控端托管启动，先放行防火墙） |

开机链路：

```
PXE → DHCP → TFTP(BootX64.EFI / pxeboot.n12) → BCD → boot.sdi + win11x64.WIM（内容=fyboot）
  → WinPE 启动 → PECMD 执行 Pecmd.ini → 末尾 EXEC start_client.cmd
  → 等 IPv4 就绪 → DP-PXEClient.exe --gui（原生窗口）→ 连服务端 6003 → 列表里出现 001
```

- 被控端在 PE 里是**原生窗口界面**（walk，不依赖 WebView2，见 4.1.1）；想看纯控制台日志就改用 `--nogui`。
  日志同时回传服务端，服务端日志区能看到 `[#001] ...`。
- 控制台已切到 UTF-8（`SetConsoleOutputCP(65001)`），中文不乱码。

重新注入（改了被控端之后）：

```bat
build.bat
pe\inject.bat                       :: 默认注入 ..\netboot\boot\win11x64.WIM
pe\inject.bat D:\path\xx.wim         :: 指定其它镜像
```

> 注入前建议先备份：`copy boot\win11x64.WIM boot\win11x64.WIM.bak`。
> 注入命令清单在 `pe\update_cmds.txt`，注入后用 `wimlib-imagex verify` 校验镜像完整性。

---

## 10. 许可证

本项目以 **GNU General Public License v3.0** 发布，完整条款见 [LICENSE](LICENSE)。

这意味着你可以：

- ✅ 自由使用、修改、分发本项目（商业或非商业）
- ✅ 分发修改后的版本

但必须遵守：

- 📌 **开源传染**：分发本项目的修改版或衍生作品时，必须同样以 GPL-3.0 开源并提供完整源码
- 📌 **保留声明**：必须保留版权声明与许可证文本，并标明修改内容
- 📌 **无担保**：作者不对使用后果承担任何责任（详见 GPL-3.0 第 15、16 条）

```
Copyright (C) 2026 DP-PXE 项目作者（GitHub: @Y-NanLi）

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
GNU General Public License for more details.
```

> 若你需要把本项目用于闭源商业产品，GPL-3.0 不允许；请联系作者讨论其它授权方式。

---

## 11. 致谢与免责声明

**致谢**

- **HHXSPxe** 原作者 —— 本项目的功能与交互均仿照其实现；QQ 群 **103494722**
- 编写主力：**DeepSeek**
- 依赖：[Wails v2](https://wails.io/)、[WebView2](https://developer.microsoft.com/microsoft-edge/webview2/)、
  [wimlib](https://wimlib.net/)、[TightVNC](https://www.tightvnc.com/)、[iPXE](https://ipxe.org/)

**免责声明**

本工具会执行 **磁盘分区、格式化、镜像还原、引导写入、注册表修改** 等高风险操作。
请在**测试环境先验证**，并**自行做好数据备份**；生产环境使用造成的数据丢失、系统损坏、
业务中断等后果由使用者自行承担，作者不承担任何责任。

**参与贡献**

- 发现 bug 或想要功能：到 [Issues](https://github.com/Y-NanLi/DPPXE/issues) 提交（请附版本号、日志、复现步骤）
- 提交代码：fork → 修改 → 提 PR。提交前请确保 `test.bat`（`--selftest` 33 项）与 `go test ./...` 全部通过
- 改动涉及功能/修复时，请同步更新 [`更新记录.md`](更新记录.md) 并递增版本号
