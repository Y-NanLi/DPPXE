# DP-PXE —— Go 重构版（v1.0）

把 `rebuild/`（Python + PySide6 版）按模块一一对应地重写成 Go，
界面改用 **Wails v2 + WebView2**（前端是手写的 HTML/CSS/JS，没有 npm 依赖），
编译产物是单个 exe，不依赖 Python / PySide6 / 任何运行库。

`hh/` 仍然是被调用的程序目录（`netboot` 引导文件 + `AutoCommand` 脚本），本程序只负责
「发什么、怎么发」——DHCP / TFTP / HTTP / 控制通道全部真实实现。

---

## 1. 编译与运行

```bat
build.bat      :: 一键编译（自动用 ..\.tools\go 里的便携版 Go）
run.bat        :: 启动图形界面
test.bat       :: 内置自检（33 项）
```

产物：

| 文件 | 子系统 | 用途 |
|---|---|---|
| `bin\DP-PXE.exe` | GUI（`-H windowsgui`） | 图形界面服务端，双击即用 |
| `bin\DP-PXEClient.exe` | 控制台 | 被控端 / 模拟器 / 无界面服务端（**PE 里用这个**） |

两者是同一份代码，参数不同角色不同：

```bat
bin\DP-PXE.exe                                     :: 图形界面
bin\DP-PXEClient.exe --nogui --root D:\hh\netboot  :: 无界面跑全部服务（PE / 服务器）
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

环境要求：Windows 10/11 + WebView2（系统自带；没有就用 `--web` 走浏览器）。

---

## 2. 目录结构（与 Python 版对照）

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
│  └─ client/                   ← client/（被控端）
│     ├─ client.go             连接 / 握手 / 心跳 / 重连 / 指令分发 / 文件拉取
│     ├─ engine.go             脚本引擎（ALL/PE/SYS、变量、GOTO、IF、FOR、WHILE + 全部指令）
│     ├─ win.go                Win32 磁盘枚举（DeviceIoControl）+ 网卡 MAC
│     ├─ service.go            安装/卸载（计划任务 + Run 键）
│     └─ sim.go                客户端模拟器（控制台）
└─ bin/                        编译产物
```

**架构上的一处变化**：Python 版是「Qt 控件直接被后台线程回调」，Go 版改成
「后台服务只维护状态 + 环形日志，界面每 500ms 拉一次 `/api/state`」。
好处是彻底没有跨线程改 UI 的崩溃风险（Python 版为此专门加了 4 个 Signal），
而且同一套前端既能跑在 WebView2 里，也能跑在浏览器里。

---

## 3. 界面

八个页签、顶部三列、底部状态灯与日志，全部按 Python 版 1:1 复刻：

| 页签 | 功能 |
|---|---|
| DHCP 设置 | 地址池 / 掩码 / 网关 / DNS / 租约 + 快速响应、安全引导、代理 DHCP |
| 远程CMD命令 | `SHELL` / `SHELLWAIT`，勾"等待执行完成"则阻塞并回传全部输出 |
| 发送文件 | 保存路径 + 文件/文件夹列表 + 单播/P2P + 进度条 |
| 高级分区 | 磁盘列表（读被控端上报的真实磁盘）、分区数目、GPT/MBR、ESP/MSR、实时 DiskPart 预览 |
| 系统安装 | 还原(WIM/ESD+卷+压缩)、备份(增量)、注入驱动、智能分区、重建主引导、引导修复、注入应答 |
| 流式安装 | 镜像流地址 + 8 个开关 + 硬盘号，生成 `STREAMINSTALL` 参数串 |
| 自动化指令 | 脚本编辑器 + 模板 + 前缀校验 + zlib 压缩下发 |
| 其它设置 | 重启客户端、注入客户端/VNC/PE驱动、下次启动、改名、关机/重启/WOL、清缓存、保存设置 |

顶部：服务端 IP 下拉、5 个服务开关、「仅仅选择中客户端接收指令」、引导文件配置、
客户端表（编号/IP/MAC/状态/网卡/根/引导）。

客户端表右键菜单：**连接 VNC（内置查看器）**、用外部 Viewer 连接、查看信息、
修改编号和 IP、从列表删除、远程重启/关机/开机、重启客户端进程、
重启/关机/开机所有客户端、保存客户端列表。

### 3.1 被控端界面（`--client --gui`）

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

### 3.2 内置 VNC 查看器（不再依赖外部 tvnviewer）

右键客户端 →「连接 VNC <IP>（内置）」，控制端窗口里直接出现被控端画面，
不用再拉起 `tvnviewer.exe`。实现是自带的 RFB 客户端（`internal/vnc`，约 700 行，无第三方库）：

| 能力 | 说明 |
|---|---|
| 协议 | RFB 3.3 / 3.7 / 3.8 握手；安全类型 None 与 VNC 认证（DES 挑战/响应） |
| 编码 | CopyRect / Hextile / Raw，外加 DesktopSize 伪编码（分辨率变化自动重适配） |
| 像素格式 | 统一协商成 32bpp RGBA，Go 侧维护整屏画布，只把**脏矩形**推给前端 |
| 输入 | 鼠标（左/中/右键 + 滚轮）、键盘（X11 keysym，含 F1-F12、方向键、修饰键） |
| 传输 | `GET /api/vnc/frame` 返回二进制帧：`u32 seq + u16 矩形数 + 每块 (x,y,w,h,len) + RGBA` |
| 界面 | 顶部工具栏：适应窗口 / Ctrl+Alt+Del / 全屏 / 断开 / 关闭；Esc 关闭 |

实测（本机 TightVNC 服务端 1600×900）：首帧 5700 个 16×16 瓦片、覆盖 1,440,000 像素，
0.3 秒内出画面；之后的增量帧只有 15–25 KB。

- 密码：服务端设了密码时会弹框输入，填一次记到 `config.json` 的 `vnc_password`。
- 需要外部 viewer 时仍保留「用外部 Viewer 连接」，走原来的 tvnviewer/vncviewer 查找逻辑。
- 未实现：Tight/ZRLE 编码（局域网用 Hextile 足够）、剪贴板同步、文件传输、多显示器切换。

状态判定与 Python 版一致：**在线**（6003 会话存在，绿色加粗）、
**PXE**（90 秒内发过 DHCP，橙色）、**离线**（灰色，记录保留，悬停看最后在线时间）。

---

## 4. 验证结果

```
bin\DP-PXEClient.exe --selftest
结果：33 通过 / 0 失败
```

覆盖：协议 296 字节头往返（含中文/超长截断）、zlib 兼容、DiskPart 脚本与 Python 版逐行一致、
MBR 扩展分区、脚本规整、流式 11 参数位、DHCP DISCOVER 解析（EFI/iPXE/畸形包）、
控制通道（HELLO_ACK / 心跳 / FILE_SEED + 分片拉取 1.05MB / 磁盘上报 / 广播 / 重连复用编号）、
HTTP（200 / Range 206 / 404）、TFTP（RRQ 传完 1216B）、脚本引擎（变量、单行 IF、FOR 循环、
UNCOMPRESS 解析、SETVAR）。

端到端（真实进程 + 真实 API）：

```
服务端 --web → 启动服务（HTTP + 文件服务）
被控端 --client → 握手 #001、上报真实磁盘 HD0 ST1000VX013 932GB GPT
推送 4 个文件（含 1.5MB / 6 分片）→ 全部 MD5 一致
下发自动化脚本 → ALL 行执行、PE 行按环境跳过、SYS 行执行
远程 CMD → 实时回传 "DPPXE-GO-OK" 与系统版本行
分区页预览 → 生成 26 行 DiskPart 脚本（与 Python 版完全一致）
```

---

## 5. 与 Python 版的差异

| 项 | 说明 |
|---|---|
| 被控端界面 | **已移植**（`--client --gui`，见第 3.1 节）。PE 里没有 WebView2，所以 PE 内仍用控制台模式（`--client`，不带 `--gui`）。 |
| P2P 分发 | 与 Python 版一样简化成「服务端拉模式单播」，界面上保留单播/P2P 选项但行为相同。 |
| 注入类操作 | 只下发指令并记录，不在本机执行磁盘/注册表操作（与 Python 版一致）。 |
| 抓包 | 依赖系统自带 `pktmon`（Win10 1809+），行为与 Python 版一致；非管理员不会自动开。 |
| 配置 | 首次启动会自动读取 `%APPDATA%\HHXSPxeRebuild\config.json` 并迁移到 `%APPDATA%\DPPXEGo\config.json`，字段名完全相同。 |
| 控制通道 | 与 Python 版**逐字节兼容**（296 字节头、UTF-16LE 文本、zlib 脚本），两边的被控端可以互换使用。 |

---

## 6. 网启链路（与 Python 版一致）

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

## 7. 常见问题

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

- 启动后会先打印一行 `DP-PXE 被控端 1.0 启动（主机名，系统/PE）→ 服务端 x.x.x.x:6003`，
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
程序启动时会在 `..\netboot`、`..\hh\netboot` 等位置自动找一次。

**④ 客户机拿不到 IP / 引导不动**
「选择服务端IP」要选与客户机同网段的网卡；地址池默认按该 IP 前三段推导。
NAT 网络下必须开「代理DHCP」（vmnet8 自带 DHCP 会抢答）。

**④.5 分区只出来一个 / 其它两个异常**

diskpart 日志里出现：

```
虚拟磁盘服务错误:
指定的驱动器号对于分配不可用。
```

说明脚本要用的盘符（C/D/E）被光驱、U 盘或旧分区占着。**diskpart 遇到这个错误会中止整个脚本**，
所以后面分区、格式化、卷标全都不执行 —— 表现就是"选了 3 个分区，只出来 1 个"。

被控端现在会自动处理（`internal/client/letters.go`）：

1. 只针对脚本真正要用的那几个盘符（一般是 C/D/E），先 `remove letter` 腾出来；
2. 腾不掉的（多半是光驱）尝试把它改挂到靠后的字母；
3. 还不行就**自动改用其它空闲盘符**，并把脚本里的 `assign letter` 和后面
   `select volume`（卷标段）一起改掉，保证脚本不再中止。

日志里会写明结果，例如：

```
分区前先腾出盘符：C D
  盘符 D: 无法移除（虚拟磁盘服务错误: 指定的驱动器号对于分配不可用。），将改用其它空闲盘符
盘符冲突：数据分区改用 C/E/F（原计划 C/D/E）
分区结果：
  分区 ### 类型       大小    偏移
  -------------  ----------  -------  -------
  分区 1   主要       300 MB   1024 KB
  ...
```

> 改了盘符之后，「系统安装」页的"指定安装分区"记得选对应的那个（例如 E://）。

**④.6 分区报「DiskPart 无法打开或读取脚本文件」**

```
DiskPart 无法打开或读取脚本文件。
请确定你指定的文件是否存在。
diskpart 返回 2147500037
```

这是**脚本文件没写成功**（不是分区参数问题）。PE 里客户端是开机时由 PECMD 以 SYSTEM 拉起的，
此时 `%TEMP%` 可能指向一个还不存在的用户目录，写文件会静默失败，diskpart 自然读不到。

被控端现在按优先级找一个**确实可写**的目录写脚本，并回读确认非空：

```
%TEMP% → %SystemRoot%\Temp（X:\Windows\Temp，PE 一定有）→ 程序所在目录 → 系统临时目录 → 当前目录
```

文件名带纳秒后缀（`DPPXE_DP_123456789.txt`），避免和上一次的 diskpart 抢同一个文件。
日志里会说明：

```
TEMP 目录不可写，diskpart 脚本改写到 X:\Windows\Temp
diskpart 没有任何输出（返回码 2147500037），脚本文件：X:\Windows\Temp\DPPXE_DP_123.txt
```

失败时把这行路径发我，就能直接定位。

**④.7 「重建主引导记录(MBR)」为什么是灰的**

它只对 **MBR 分区表**有效，所以「分区表类型」选 GPT 时这一项自动禁用（ESP/MSR 反过来，选 MBR 时禁用）。
这不是 bug，是跟原软件一致的行为：

- 要重建 MBR 引导记录 → 分区表切到 **MBR**，复选框就会亮；
- GPT 盘要修引导 → 用「系统安装」页的**引导修复**（`bcdboot /f UEFI`），或先 `convert gpt` 后再修复。

**⑤ 想看包**
菜单「开始抓包诊断」（需管理员），停止后自动把 DHCP/TFTP/HTTP 时间线写进日志，
产物是 `bin\logs\run.pcap`，可直接拖进 Wireshark。

**⑥ 编译**
`build.bat` 会优先用 `..\.tools\go\bin\go.exe`（本项目自带的便携版 Go 1.27.3），
没有就退回 PATH 里的 `go`。依赖走 `GOPROXY=https://goproxy.cn`。

## 8. 部署到 WinPE（fyboot + 被控端已内置）

> 本程序只负责 **`D:\wb\h-go\hh\netboot`** 这个目录（Go 版配置里的「相对根目录」就指向它）。

`boot\win11x64.WIM` 的内容已经换成 **fyboot.wim（飞雨 Win11PE）**，
并把 Go 版被控端注入进去、设成开机自启。

**为什么文件名还叫 win11x64.WIM**：`boot\BCD` 里写死了 `\boot\win11x64.wim`、
`menu.txt` 里写死了 `boot/Win11x64.wim`，改名就得同时改 BCD（二进制）。所以做法是
「保留文件名、替换内容」，BCD 和 iPXE 菜单都不用动。

镜像里新增/修改了 4 个文件：

| 路径 | 作用 |
|---|---|
| `\Program Files\DP-PXEClient\DP-PXEClient.exe` | Go 版被控端本体（9 MB） |
| `\DPPXE\start_client.cmd` | 等网络就绪 → 拉起被控端 `--nogui`，进程退了自动重启 |
| `\Windows\System32\Pecmd.ini` | 末尾追加一行 `EXEC %SystemRoot%\System32\cmd.exe /c X:\DPPXE\start_client.cmd`（这个 PE 是 PECMD 启动的，这条才是真正生效的） |
| `\Windows\System32\startnet.cmd` | 兜底：万一某台机器走 winpeshl 启动也能拉起 |

开机链路：

```
PXE → DHCP → TFTP(BootX64.EFI / pxeboot.n12) → BCD → boot.sdi + win11x64.WIM（内容=fyboot）
  → WinPE 启动 → PECMD 执行 Pecmd.ini → 末尾 EXEC start_client.cmd
  → 等 IPv4 就绪 → DP-PXEClient.exe --nogui → 连服务端 6003 → 列表里出现 001
```

- 被控端在 PE 里是**控制台模式**（PE 没有 WebView2），日志同时回传服务端，服务端日志区能看到 `[#001] ...`。
- 控制台已切到 UTF-8（`SetConsoleOutputCP(65001)`），中文不乱码。
- 备份：`boot\fyboot.wim`（原始 fyboot）、`boot\win11x64.WIM.pyclient`（换之前的 DP-PXE PE）。

重新注入（改了被控端之后）：

```bat
build.bat
pe\inject.bat                       :: 默认注入 ..\..\hh\netboot\boot\win11x64.WIM
pe\inject.bat D:\path\xx.wim         :: 指定其它镜像
```

还原：

```bat
copy /y boot\fyboot.wim boot\win11x64.WIM               :: 回到未注入的 fyboot
copy /y boot\win11x64.WIM.pyclient boot\win11x64.WIM    :: 回到原来的 DP-PXE PE（含 Python 被控端）
```
