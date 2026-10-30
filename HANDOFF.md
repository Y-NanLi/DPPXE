# HANDOFF — DP-PXE（Go 重构版）

> 交接说明。生成时间：2026-10-30 20:47 ｜ 最近更新：2026-10-30 21:40（v1.5 发布后）
> 工程根：`D:\wb\h-go\go`（Git 仓库根也是这里）

---

## 1. 项目目标

把原 Python 版 PXE 服务端（`D:\wb\h-go\rebuild`）重构为 Go 版 **DP-PXE**，单个可执行文件承担多种角色：

| 角色 | 可执行文件 | 说明 |
|---|---|---|
| 控制端（服务端） | `bin\DP-PXE.exe` | Wails + WebView2 图形界面，内置 DHCP / TFTP / HTTP / 控制通道(6003) / VNC 查看器 |
| 被控端 | `bin\DP-PXEClient.exe` | 装进 PE 或已装系统，连控制端执行安装、分区、备份、注入等指令 |
| 模拟器 / 自检 | 同上 | `--sim` / `--selftest` |

角色靠**文件名前缀** `dp-pxeclient` 判定，不靠参数。

**范围约束（用户明确要求）**：只改 Go 工程 `D:\wb\h-go\go`，Python 工程 `D:\wb\h-go\rebuild` 不要动。

**本次会话的主线**：把被控端做出类似原版的图形界面（先已装系统、再 PE），并修一串 PE 实战问题。

---

## 2. 当前分支 / HEAD

```
分支：main
HEAD：v2.0 发布提交（tag `v2.0`）
提交：release: v2.0 — 左侧导航栏改版 + 布局自适应 + 产物体积优化
标签：v2.0（已发布正式 Release）
远程：https://github.com/Y-NanLi/DPPXE.git
```

**工作区干净，已与远程同步（0/0）**。近期版本节奏：v1.4 → v1.5 → **v2.0**：

```
git log --oneline -4
<hash> release: v2.0 — 左侧导航栏改版 + 布局自适应 + 产物体积优化
661e04a release: v1.5 — 软件图标 + 蓝白主题皮肤 + 修复 walk typed-nil 崩溃
1c2bedd release: v1.4 — 被控端图形界面（已装系统 + PE 原生）+ 命令回显 + 网络启动降噪
48f93f1 docs: 开源化 README 改版（GPL-3.0 许可证 / 下载 / 贡献）+ 修正与 v1.3 不符的描述
```

已入库的新增文件：`internal/client/uigui_windows.go`（仅 `clientgui` 构建）、
**`internal/client/uigui_stub_windows.go`（控制端的占位，v2.0 新增）**、`pe/DP-PXEClient.exe.manifest`、
**`rsrc.syso`（必须入库，含 comctl32 清单 + 图标）**、`netboot/EFI/Microsoft/Boot/Fonts/`（4 个 ttf）、
`cmd/icongen/`、`assets/`（`icon-src.webp` + `app.ico`）、`internal/client/appicon.png`、
**`AGENTS.md`（长期规则，v2.0 入库）**。

> 注意：`pe/Pecmd.ini` **无改动**（曾被写坏，已 `git checkout` 恢复原状）。它是 GBK 编码，禁止用文本编辑器修改。

---

## 3. 已完成文件及说明

### 3.1 被控端界面

| 文件 | 说明 |
|---|---|
| `internal/client/uigui_windows.go`（新） | **PE 专用原生界面**。用 `github.com/lxn/walk`（纯 user32/gdi32 封装）画 700×430 窗口：大字机器号（未连接红 / 已连接绿）、进度百分比、客户端与服务端 IP/版本、只读滚动日志、右下角 wimlib 版本；400ms 刷新。`runtime.LockOSThread()` + `guiMW.Show()` + `win.ShowWindow(SW_SHOWNORMAL)` 双保险防止窗口被隐藏。 |
| `internal/webui/dist/client.html` / `client.css` / `client.js` | **已装系统**用的界面（Wails 无边框窗口）。按原版截图重做：标题 `DP-PXEClient: 1.3.0`、白色主体、42px 红色机器号、圆环进度（r=39，`RING=245.04`）、深蓝加粗信息标签、日志行点击高亮、右下角绿色 `Wimlib Ver:x.x`。 |
| `internal/client/state.go` | `Info` 新增 `WimlibVer`（`json:"wimlib_ver"`）。 |
| `main.go` | `wantGUI` 追加 `&& !client.IsPE()`（PE 里不走 Wails）；PE 分支改调 `client.RunNativeGUI()`，失败自动退回控制台；`ensureConsole()` 补控制台。 |

### 3.2 命令回显（用户核心诉求：能看到操作了什么命令）

| 位置 | 日志文案 |
|---|---|
| `internal/client/client.go` `runCommand` 入口 | `收到指令 <OP>`（SHELL 附带命令原文） |
| `internal/client/engine.go` `ExecRaw` | `执行指令 <指令名> <参数>`（超 160 字截断） |
| `internal/client/engine.go` `Run` | `> <实际命令行>`（原来只在失败/超时才打印） |

### 3.3 协议与数据修复

| 文件 | 改动 |
|---|---|
| `internal/services/control.go` | `OpHelloAck` 版本原来**硬编码 `1,0,0`**，导致界面「服务端版本」永远 1.0 → 改用 `config.VersionParts()` 回真实版本。`PushFile` 不再静默失败：目标为空 / 全部发送失败都返回错误；逐个目标检查 `Send` 并记 `#CID 发送文件种子失败`；成功记 `已向 N 台客户端发送文件种子`。 |
| `internal/config/config.go` | 新增 `VersionParts()`，把 `AppVersion`（`"1.3"`）拆成 `(1,3,0)`。 |
| `internal/server/server.go` | `PushFiles` 发出种子后起 goroutine，10 秒内 `pushSent==0` 则告警「没有任何分片请求，请检查被控端日志」。 |
| `internal/client/client.go` `pullFile` | 目标目录不可写时（PE 里 `D:` 常是光驱/不存在）**自动改写**到 `maxFreeDrive():\DPPXE_Recv\<name>` 并说明原因（原来直接 return，整批静默失败）。 |
| `internal/client/client.go` `probeWimlib` | 启动时异步 `wimlib-imagex --version`，正则取版本存进 `Info.WimlibVer`。 |

### 3.4 TFTP 日志降噪

`internal/services/tftp.go`：
- 新增 `isOptionalTftpFile()`：`\BCD`、`*.p7b`、`*.pol`、`*.mui`、`*.ttf` 的 404 从 `err` 降为 `info`（`TFTP 跳过 …（可选文件，PE 里没有属正常）`）。这些是 UEFI 启动的正常探测，不是故障。
- 新增 `sendWaitAckT(attempts, timeout)`：OACK 等待从固定 4×2s=8s 改为 **2×1s**，日志改「OACK 无应答，放弃本次请求（客户端通常会重发）」；数据块仍 4×2s。

### 3.5 PE 构建与启动链

| 文件 | 说明 |
|---|---|
| `build.bat` | 两个产物都改为 `-tags desktop,production -ldflags "-s -w -H windowsgui"`（被控端原来漏 tags，一开 GUI 就弹 build tags 错误）；新增 `rsrc.syso` 缺失时自动生成。 |
| `rsrc.syso`（新） | 内嵌 `pe/DP-PXEClient.exe.manifest`（comctl32 v6 + dpiAware）。**必须入库**，否则重编后 walk 会 `TTM_ADDTOOL failed`。 |
| `pe/DP-PXEClient.exe.manifest`（新） | 清单源文件。 |
| `pe/start_client.cmd` | 纯 ASCII 注释（cmd 按 ANSI 936 读 .cmd，UTF-8 中文会被解码出 `|` 去执行乱码命令）；`start "" "%APP%" --gui` 后**立即 `exit /b 0`**（不留黑框）。**已移除 tasklist 单实例检查**（精简 PE 上不可靠）。 |
| `pe/startnet.cmd` | 注释改 ASCII，fallback 改 `--gui`。 |
| `netboot/EFI/Microsoft/Boot/Fonts/`（新） | `wgl4_boot.ttf` / `chs_boot.ttf` / `msyh_boot.ttf` / `msyhn_boot.ttf`。原来完全没有 Fonts 目录，启动菜单中文是方块。 |

### 3.6 其它（本次会话之外但同样在未提交区）

`internal/client/engine.go` 里还包含：`cmdSTREAMINSTALL` 改为「先下载再 apply」（wimlib 不支持 http）；`cmdAUTOPARTITION` 按容量自动选方案；`cmdINJECTCLIENT` / `cmdINJECTVNC` 写目标盘离线注册表 + Run 键开机启动；`cmdUNCOMPRESS` 路径不加引号 + NTFS/空间预检。

### 3.7 图标 / 皮肤（v1.5）

| 文件 | 说明 |
|---|---|
| `cmd/icongen/main.go`（新） | 图标转换工具：`golang.org/x/image/webp` 解码 + CatmullRom 缩放，把 `assets/icon-src.webp` 转成多尺寸 `assets/app.ico`（16/24/32/48/64/128/256，PNG 压缩帧）；`-png` 另出 `internal/client/appicon.png`（256×256） |
| `assets/`（新） | `icon-src.webp`（源图）+ `app.ico`（生成物） |
| `internal/client/appicon.png`（新） | `//go:embed` 进二进制，供 walk 用 `walk.IconFrom` 设窗口图标 |
| `rsrc.syso` | 改为 `rsrc -manifest pe/DP-PXEClient.exe.manifest -ico assets/app.ico -o rsrc.syso -arch amd64`，**清单与图标一起内嵌**（1.4KB → 147KB） |
| `internal/webui/dist/style.css` | 蓝白主题（深蓝 `#2c4a86` / 亮蓝 `#6fa8e8` / 白），全部走 `:root` 变量 |

**⚠️ walk 的 typed-nil 坑（v1.5 修复的崩溃）**：`walk.NewIconFromResourceId(1)` 失败会返回 `(nil, err)`，
`icon, _ :=` 得到 `(*walk.Icon)(nil)` 赋给 declarative 的 `Icon`（类型 `interface{}`）→ 变成**有类型的 nil**，
walk 内部 `if src == nil` 判不住 → `handleForDPIWithError(0x0)` 直接 panic（退出码 2，PE 里表现为「被控端完全没启动」）。
**必须保证传给 Icon 的值非 nil**（现在用嵌入 PNG + `walk.IconFrom`，失败兜底 `walk.IconApplication()`）；
`RunNativeGUI` 已加 `defer recover()` 退回控制台。

### 3.8 PE 镜像当前状态

`go\netboot\boot\win11x64.WIM` 已注入 v2.0 客户端，校验 MD5 与 `bin\DP-PXEClient.exe` 一致
（`07C561ACF3A15D4BF1FB168CB29D99DC`），wimlib verify 通过。

注入备份链（都在 `netboot\boot\`，已被 `.gitignore` 排除，磁盘紧张时可只留最近 1~2 个）：

```
win11x64.WIM.pre-pegui / pre-recvfix / pre-nativegui / pre-hidecmd /
pre-hidecmd2 / pre-showfix / pre-launchfix / pre-v1.4 / pre-icon / pre-v1.5 / pre-v2.0 ...
```

> 注：`netboot\boot\` 下累计已有 20+ 个 `.pre-*` 备份，每个约 600~700MB（合计 12GB+）。

### 3.9 控制端界面重构与体积优化（v2.0）

| 项 | 说明 |
|---|---|
| 左侧导航栏 | 160px，三页：首页 / 日志 / 软件设置（`.nav-item` 选中蓝底白字，**不加 transition**——渐变背景无法插值，会出现"白字无底色"中间帧） |
| 首页 | 只留「选择服务端IP」+「已连接客户端」两张卡片，8 个功能页签在下方；顶部开关改两列 grid（248px→180px） |
| 日志页 | 独立整页占满内容区，进页面自动滚到底 |
| 软件设置页 | 引导文件 + 运行目录（配置目录 / 日志目录） |
| 布局 | `.body` 用 CSS Grid（`auto minmax(0,1fr)`）；`.panels` 必须 **block**（flex 容器的 `scrollHeight` 不含溢出子项 → 不出滚动条）；`.panel` 不能 `height:100%` |
| 体积 | `uigui_windows.go` 加 `//go:build windows && clientgui`，新增 `uigui_stub_windows.go`（`!clientgui`）；控制端不带 `clientgui` → 不链接 walk/win，**14.55MB → 12.65MB** |

**已验证无效的优化**（别重复试）：`-ldflags "-s -w -buildid="` 体积无变化；`internal` 下没有可删的死代码。

---

## 4. 未完成事项

1. ~~**【最高优先级】PE 里被控端能否正常启动，尚未确认。**~~
   ✅ **已确认（2026-10-30 20:58 用户反馈）**：PE 里被控端可正常启动并上线控制端。
   最后一版启动链（`start_client.cmd` 去掉 tasklist、`Pecmd.ini` 恢复不带 `!`、`start` + 立即 `exit`）验证有效，**不要再改动这条链**。
2. ~~**git 未提交**~~ ✅ 已提交并发布 **v1.4**（`1c2bedd`，tag `v1.4`，CI success，Release 含 4 产物）。
3. ~~**版本号仍是 1.3**~~ ✅ 已升 **1.4**（`AppVersion`/`ClientVersion`/`VersionWord`/界面标题/README 全部同步）。
4. **圆环进度未做**：PE 原生界面用的是大号百分比文字，没还原原版的圆环（需要 GDI `Arc` 自绘）。
5. **单实例保护缺失**：因为 tasklist 不可靠被移除，若 `Pecmd.ini` 与 `startnet.cmd` 同时执行会起两个客户端。替代方案未实现（建议用文件锁：启动时 `CreateFile` 独占 `X:\DPPXE\client.lock`）。
6. **PE 里 GUI 与黑框的最终形态**：现在是「黑框一闪 → 只剩 GUI」，用户暂未提出异议。
7. **被控端崩溃后不会自动重启**：脚本 `exit` 后没有守护（客户端自身有断线重连，但没有进程守护）。

---

## 5. 验收标准

### 5.1 PE 被控端（✅ 已通过，2026-10-30）
- [x] PXE 启动进入 PE 后，被控端正常启动（原生窗口）
- [x] 控制端界面能看到该机器上线（用户确认「可以正常启动上线」）
- [ ] 窗口内显示：机器号、客户端 IP、服务端 IP、双方版本、`Wimlib Ver:1.14.x`、实时滚动日志（细节未逐项确认）
- [ ] 窗口在 400ms 内跟随状态变化（进度、机器号颜色）

### 5.2 已装系统被控端
- [x] 双击 `DP-PXEClient.exe` 出现 Wails 界面，标题 `DP-PXEClient: 1.5.0`（已实测窗口标题 `DP-PXEClient 1.5`）
- [ ] 日志区可点击某行高亮

### 5.3 命令回显
- [ ] 下发任意指令后，被控端日志依次出现 `收到指令 X` / `执行指令 X ...` / `> 实际命令行`

### 5.4 文件发送
- [ ] 发送文件时进度条按真实分片推进，传完才 100%
- [ ] 未勾选客户端时提示「没有选中的客户端（在线 N 台…）」
- [ ] 目标目录不可写时自动改用 `X:\DPPXE_Recv\` 并说明

### 5.5 网络启动日志
- [ ] `*.p7b` / `*.pol` / `*.mui` / `*.ttf` 不再显示为红色 404，而是「TFTP 跳过…（可选文件）」
- [ ] 不再出现 8 秒级的 `OACK 超时` 红色警告
- [ ] 启动菜单中文不显示方块（字体已补）

### 5.6 构建
- [x] 一次跑通，产出 `bin\DP-PXE.exe`（**12.65MB**，不带 `clientgui`）+ `bin\DP-PXEClient.exe`（**14.56MB**，带 `clientgui`）
- [x] `DP-PXEClient.exe --selftest` → `33 通过 / 0 失败`，退出码 0
- [x] `go test ./...` 全绿、`go vet ./...` 干净、`node --check` 两个 js 通过
- [x] exe 文件图标 / 任务栏图标 / 窗口图标均生效（`rsrc.syso` 含图标，147KB）
- [x] 控制端窗口标题 `DP-PXE 2.0`、被控端 `DP-PXEClient 2.0`（`MainWindowTitle` 实测）

---

## 6. 试过但失败的方案

| # | 方案 | 结果 | 结论 |
|---|---|---|---|
| 1 | **Wails 在 PE 里开界面** | 弹「The WebView2 runtime is required」模态框，不点掉进程卡死，被控端完全离线 | PE 没有 WebView2，Wails 路线在 PE 里彻底不可行。改用原生 Win32（walk） |
| 2 | **外部 `<exe>.manifest` 文件** | 仍报 `TTM_ADDTOOL failed`（Win10 不加载外部清单） | 必须用 `rsrc` **内嵌**成 `rsrc.syso` |
| 3 | **`Pecmd.ini` 加 `EXEC !` 隐藏 cmd 窗口** | 加了之后 PE 里客户端**完全没启动**，控制端看不到上线 | `EXEC !` 的隐藏语义在 PECMD 上不可靠；已恢复为普通 `EXEC` |
| 4 | **`tasklist` + `find` 做单实例检查** | 精简 PE 上挂住/误判，脚本直接 `exit`，客户端没起来 | PE 脚本里禁止用 tasklist/find 管道做逻辑判断 |
| 5 | **`start "" /wait` 配合 `EXEC !`** | 仍无进程（与 #3/#4 叠加） | 同上，已回退 |
| 6 | **用编辑工具改 `Pecmd.ini`** | 文件是 GBK，被写成 88 处 U+FFFD，`iconv` 直接报错，中文全毁 | 必须 `git checkout` 恢复后用 Python 二进制替换 |
| 7 | **被控端不加 `-tags desktop,production`** | 一开 GUI 就弹「Wails applications will not build without the correct build tags」 | 两个产物都必须带 tags；`build.bat` 已同步 |
| 8 | **判断「进程存活」= 界面正常** | 模态错误框也会让进程存活，误判过一次 | 改用 `MainWindowTitle` 判断窗口是否真的存在 |

---

## 7. 下一步计划

### 第 1 步：确认 PE 启动（阻塞项）— ✅ 已完成
1. ✅ 重启控制端 `DP-PXE.exe`（TFTP 降噪、PushFile 报错在控制端）。
2. ✅ PXE 重启客户机：被控端正常启动，控制端能看到上线（用户 2026-10-30 20:58 确认）。
3. 定位手册（留作以后回归用，本次未触发）：
   - 进 PE 后**手动双击** `X:\Program Files\DP-PXEClient\DP-PXEClient.exe`
     - 能出窗口 → 问题在 `Pecmd.ini` / 脚本执行链
     - 不能出窗口 → 问题在客户端本身（缺 DLL / walk 初始化）
   - 若怀疑脚本，把 `start_client.cmd` 临时改成写 trace 文件（`echo step1 > X:\DPPXE\launch.log`），逐段确认卡在哪
   - 终极兜底：`Pecmd.ini` 直接 `EXEC` 启动 exe（跳过 cmd），代价是失去等待网络与进程守护

### 第 2 步：提交与发版 — ✅ 已完成（v1.4 / v1.5 / v2.0 三版）
1. ✅ 提交并发布 **v1.4**（27 文件）、**v1.5**（18 文件：图标/皮肤/typed-nil）、**v2.0**（导航栏/布局/体积 + `AGENTS.md`）。
2. ✅ 版本号每轮按 `AGENTS.md` §6.5 同步 5 处（1.3→1.4→1.5→2.0）。
3. ✅ 每轮 push main + tag，CI 均 success，Release 各含 4 产物；滚动 `latest` 跟随最新提交。

### 第 3 步：收尾优化（优先级低，均未做）
1. 单实例保护改为**文件锁**（`CreateFile` 独占 `X:\DPPXE\client.lock`），替代不可靠的 tasklist
2. 圆环进度：在 walk 的 `CustomWidget.Paint` 里用 `Canvas.DrawEllipse` + `DrawArc` 自绘
3. PE 里 GUI 与黑框的观感打磨（若「一闪」仍不可接受，可评估 VBS 隐藏启动器或 PECMD 直接启动 exe）
4. 清理 `netboot\boot\` 下过多的 `win11x64.WIM.pre-*` 备份（已累计 18+ 个，每个约 600~700MB）

---

## 8. 关键约定速查（避免踩重复的坑）

> 更完整、更新更及时的规则见 **`AGENTS.md`**（本文件是交接记录，`AGENTS.md` 是长期规则）。

- **编译（v2.0 起两个产物 tags 不同）**：
  - 控制端 `-tags desktop,production`（**不加 `clientgui`**）→ 不链接 walk/win，省 1.9MB；
  - 被控端 `-tags "desktop,production,clientgui"` → 含 PE 原生界面；
  - 两者都加 `-trimpath -ldflags "-s -w -H windowsgui"`。
- **PE 里绝不能走 Wails**：`wantGUI` 必须 `&& !client.IsPE()`。
- **walk 需要内嵌 manifest + 图标**：`rsrc -manifest pe/DP-PXEClient.exe.manifest -ico assets/app.ico -o rsrc.syso -arch amd64`。
- **`Pecmd.ini` 是 GBK**：只能用 `git checkout` 恢复 + Python 二进制替换。
- **`.cmd` 注释必须纯 ASCII**：cmd 按 ANSI 936 读文件。
- **PE 脚本禁用 tasklist/find 管道**；`Pecmd.ini` 用普通 `EXEC`（不加 `!`）。
- **`bcdboot` 不带 `/f` 也不带 `/s`**（`/f` 必须配 `/s`，单独用返回 87）。
- **`wimlib` 不支持 http**，流式安装必须先下载。
- **握手版本必须真实**（`config.VersionParts()`）。
- **配置目录** `%APPDATA%\DPPXEGo`；旧 Python 配置 `%APPDATA%\HHXSPxeRebuild` 必须保留（首次启动自动迁移）。
