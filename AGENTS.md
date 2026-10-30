# AGENTS.md — DP-PXE（Go 版）长期规则

> 本文件是**给 AI 代理和协作者看的长期约定**，不是使用文档。
> 使用说明见 `README.md`，版本变更见 `更新记录.md`，当前进度见 `HANDOFF.md`。
> 工程根 = Git 仓库根 = `D:\wb\h-go\go`。

---

## 0. 一句话定位

把原 Python 版 PXE（`D:\wb\h-go\rebuild`）重构成 Go 版。**一个可执行文件承担多种角色**，
角色由**文件名前缀** `dp-pxeclient` 判定，不靠参数：

- `bin\DP-PXE.exe` —— 控制端（Wails + WebView2 GUI），内置 DHCP / TFTP / HTTP / 控制通道 6003 / VNC 查看器
- `bin\DP-PXEClient.exe` —— 被控端（PE 用原生 Win32 窗口，已装系统用 Wails），执行安装/分区/备份/注入

---

## 1. 常用命令

```bash
# 构建（两个产物 tags 不同，别搞混，见 §5.1）
build.bat
# 等价手工命令：
# 控制端：不带 clientgui → 不链接 walk/win，省约 2.6MB
go build -tags desktop,production -trimpath -ldflags "-s -w -H windowsgui" -o bin/DP-PXE.exe .
# 被控端：带 clientgui → 含 PE 原生界面（walk）+ 已装系统 Wails 界面
go build -tags "desktop,production,clientgui" -trimpath -ldflags "-s -w -H windowsgui" -o bin/DP-PXEClient.exe .

# 自检（协议 / 控制通道 / 文件分片 / DHCP / HTTP / TFTP / 脚本引擎）
bin/DP-PXEClient.exe --selftest        # 期望：33 通过 / 0 失败，退出码 0

# 单元测试 / 静态检查
go test ./...
go vet ./...

# 前端语法检查（Go 编译不校验 JS）
node --check internal/webui/dist/app.js
node --check internal/webui/dist/client.js

# 注入 PE（先备份 WIM！见 §6）
# wimlib 工具在 Python 工程里：D:\wb\h-go\rebuild\tools\wimlib\wimlib-imagex.exe
cd pe && /d/wb/h-go/rebuild/tools/wimlib/wimlib-imagex.exe update ../netboot/boot/win11x64.WIM 1 < update_cmds.txt
```

代理环境注意：Go 工具链在 `..\.tools\go\bin\go.exe`，需要
`export GOPROXY=https://goproxy.cn,direct GOSUMDB=off`。

---

## 2. 目录职责与禁改范围

| 路径 | 职责 | 可否修改 |
|---|---|---|
| `main.go` / `helpers.go` / `selftest.go` | 入口、角色分发、控制台/窗口处理、自检 | 可 |
| `internal/protocol/` | 报文格式（296 字节头 + payload） | 可，但**改动必须同步自检** |
| `internal/services/` | DHCP / TFTP / HTTP / 控制通道 / 发现 / VNC 抓屏 | 可 |
| `internal/tasks/` | 把 UI 参数翻译成 diskpart / wimlib / bcdboot 命令 | 可，**必须补单测** |
| `internal/client/` | 被控端：连接、指令执行引擎、两套界面（`uigui_windows.go` 仅 `clientgui` 构建，`uigui_stub_windows.go` 是控制端的占位） | 可 |
| `internal/server/` | 服务端状态、API 命令分发、日志 | 可 |
| `internal/webui/dist/` | 控制端与被控端前端（原生 HTML/CSS/JS，`go:embed`） | 可 |
| `pe/` | PE 启动脚本、注入清单、manifest | 可，但**读 §5.5 / §5.6** |
| `netboot/` | TFTP 根目录（引导文件、字体） | 可（WIM 除外，见下） |
| `cmd/icongen/` | 图标生成工具（webp → ico/png） | 可 |
| `assets/` | 图标源图与产物（`icon-src.webp`、`app.ico`） | 可 |
| `rsrc.syso` | **内嵌 manifest + 图标的资源文件** | **必须入库**，不要删 |
| `bin/` `logs/` `tmp-*/` | 构建与运行产物 | 不入库，不要提交 |
| `netboot/boot/win11x64.WIM` 及 `.pre-*` | 600MB+ 的 PE 镜像与备份 | **不入库**，只在本地 |

**禁止修改（用户明确要求）**

- `D:\wb\h-go\rebuild` —— Python 原版，只读参考，**任何情况下不改**
- `D:\wb\h-go\hh` —— 旧资源副本，不作为规范位置

---

## 3. 技术栈约定

- **语言**：Go 1.27.3，模块名 `dppxe`
- **GUI**：控制端与已装系统被控端用 `wailsapp/wails/v2` v2.10.2；
  **PE 被控端用 `lxn/walk`**（纯 Win32，无 WebView2 依赖）
- **前端**：原生 HTML/CSS/JS，`//go:embed all:dist`，**不引入任何前端框架/构建工具**
- **Windows API**：`golang.org/x/sys/windows`；只在必要时直接 syscall
- **新增第三方依赖**：先论证必要性。当前依赖已覆盖需求，**不要为了一个小功能引入新库**
- **不支持跨平台**：大量 Windows 专有调用，只构建 amd64

---

## 4. 代码风格

1. **注释用中文**，面向"下一个接手的人"写清楚**为什么**，而不只是"做了什么"。
2. 每个包顶部有包注释，若对应 Python 模块则注明（例：`与 Python 版 app/tasks.py 一一对应`）。
3. 结构体字段：`json` tag + 行内中文注释说明单位/取值范围。
4. 函数注释以函数名开头；导出的类型/函数必须有注释。
5. **不要静默吞掉错误**：宁可多打一行日志，也不要让操作"看起来成功但什么都没做"
   （历史教训：`PushFile` 目标为空时静默返回，用户完全无法定位）。
6. 日志文案面向用户可读，带上下文（文件名、盘符、返回码）。
7. 提交前 `gofmt`（`go fmt ./...`）；不要手写对齐空格。
8. 文件名小写下划线；平台相关文件用 `_windows.go` 后缀。

---

## 5. 硬性技术约束（踩过的坑，必须遵守）

### 5.1 编译必须带 `-tags desktop,production`
**两个产物都要**（被控端还要额外加 `clientgui`）。漏了 `desktop,production` 的话 Wails runtime 是空壳，
被控端一开 GUI 就弹「Wails applications will not build without the correct build tags」——
这是**模态框，进程不退出**，不要只看"进程存活"就当正常。

- 控制端 `-tags desktop,production`：**不加 `clientgui`**，于是 `uigui_windows.go` 被排除，
  `uigui_stub_windows.go` 顶上（`RunNativeGUI` 返回错误），walk/win 不进二进制，省约 2.6MB。
- 被控端 `-tags "desktop,production,clientgui"`：含 PE 原生界面。

### 5.2 PE 里绝不能走 Wails
PE 没有 WebView2，`wails.Run` 会弹「The WebView2 runtime is required」，不点掉就卡死，
被控端等于完全离线。`main.go` 的 `wantGUI` 必须 `&& !client.IsPE()`。

### 5.3 walk 必须内嵌 manifest（comctl32 v6）
没有清单时初始化控件直接失败（`TTM_ADDTOOL failed`）。外部 `<exe>.manifest` 在 Win10 **不生效**，
必须内嵌：

```bash
rsrc -manifest pe/DP-PXEClient.exe.manifest -ico assets/app.ico -o rsrc.syso -arch amd64
```

`rsrc.syso` **必须入库**；`build.bat` 在它缺失时会用 `go run` 重新生成。

### 5.4 walk 的 `Icon` 字段不能是 nil（typed-nil 陷阱）
declarative 的 `Icon` 是 `interface{}`。把 `(*walk.Icon)(nil)` 赋进去会变成"有类型的 nil"，
walk 内部 `if src == nil` 判不住 → 解引用 panic → **进程退出码 2，PE 里表现为"被控端完全没启动"**。
必须保证非 nil（用 `walk.IconApplication()` 兜底）。`RunNativeGUI` 已加 `defer recover()` 退回控制台。

### 5.5 `pe/Pecmd.ini` 是 GBK 编码
**禁止用文本编辑器/Edit 工具修改**，会把中文写成 U+FFFD 损坏整个文件。
正确做法：`git checkout -- pe/Pecmd.ini` 恢复 → 用 Python 二进制读写替换 ASCII 子串。

### 5.6 PE 启动脚本（`.cmd`）规则
- **注释必须纯 ASCII**：cmd.exe 按 ANSI 代码页（中文 PE 是 936）读 `.cmd`，
  UTF-8 中文可能被解码出 `|` 等字节，导致它去执行乱码命令
- **禁止用 `tasklist` / `find` 管道做逻辑判断**：精简 PE 上会挂住或误判，客户端直接起不来
- 启动方式：`start "" "%APP%" --gui` 后**立即 `exit /b 0`**（不留黑框；客户端自己会重连）
- `Pecmd.ini` 用普通 `EXEC`，**不要加 `!`**（隐藏语义在 PECMD 上不可靠）

### 5.7 wimlib 不支持 http/https
流式安装必须**先下载到本地再 apply**，失败再退回 `dism /Apply-Image`。
还原目标路径**不加引号**（`C:\`），`"C:\"` 结尾的 `\"` 会被当成转义引号。

### 5.8 bcdboot 不带 `/f` 也不带 `/s`
`/f` 必须和 `/s` 一起用，单独 `/f ALL` 是非法参数（返回 87）。
ESP 在 PE 里没有盘符、加不了 `/s`，所以让 bcdboot 用"固件标识的系统分区"。
**不要 format 引导分区**（会把刚装好的系统盘格式化）。

### 5.9 diskpart 脚本顺序
先 `create` + `format`，**最后统一** `assign letter=X noerr`。
`assign` 失败会中止整个脚本，导致后面的分区/格式化全部丢失。

### 5.10 握手版本必须真实
`OpHelloAck` 的 args[1..3] 用 `config.VersionParts()`，不要硬编码（曾导致界面「服务端版本」永远 1.0）。

### 5.11 界面颜色只走 CSS 变量
`internal/webui/dist/style.css` 的 `:root` 是唯一色值来源，`index.html` / `app.js` **不允许出现硬编码颜色**。

---

## 6. 变更流程

改动代码后按顺序做：

1. `go build`（§1）+ `--selftest`（33/33）+ `go test ./...` + `go vet ./...`
2. 改过前端 → `node --check`
3. **更新 `更新记录.md`**（写清现象、根因、修法、验证结果，不要只写"修复了 xxx"）
4. 涉及被控端 → 注入 PE，**注入前先备份**：
   ```bash
   cp netboot/boot/win11x64.WIM netboot/boot/win11x64.WIM.pre-<主题>
   ```
   注入后校验 MD5 与 `bin\DP-PXEClient.exe` 一致
5. 升版本号时**同步 5 处**：
   - `internal/config/config.go` → `AppVersion` / `ClientVersion`
   - `internal/client/client.go` → `VersionWord = (主版本<<16)|次版本`（2.0 → `2<<16`）
   - `internal/webui/dist/client.html` 标题与版本
   - `README.md` 标题
   - `更新记录.md` 新增章节
6. 发版：commit → tag → push（tag 触发正式 Release，push main 触发滚动 latest）
   - **发版前必查 `.github/workflows/build.yml` 里两条 `go build` 的 tags 与 `build.bat` 一致**
     （控制端不带 `clientgui`、被控端带 `clientgui`）。v2.0 曾因为 CI 里被控端漏了所有 tags，
     发出去的 `DP-PXEClient.exe` 既弹 build tags 模态框、也没有 PE 原生界面（v1.0–v1.5 同样受影响）。
   - **Gitee 镜像另有 `.workflow/release-pipeline.yml`**（Gitee Go 流水线），只在推送 `v*` 标签时触发。
     注意它的语法与 GitHub Actions 完全不同：阶段是 `- name:` 直接列（不是 `- stage:`）、
     发版插件是 `release@gitee`（不是 `publish@gitee-release`）、没有 `${GITEE_TAG}` 变量
     （Tag 名在构建阶段算好后写进 `GITEE_PARAMS` 传给发版步骤）。

---

## 7. 测试要求

- 改 `internal/protocol/`、`internal/tasks/` → **必须**补/改单元测试（`*_test.go`）
- 改协议字段 → 同步 `selftest.go` 里的期望，保证 33/33
- 改界面 → `node --check` 两个 js
- 改 PE 相关 → 本机用 `MININT=1` 环境变量模拟 PE 验证
  （`IsPE()` 靠 `MININT` / `SystemDrive=X:` / `X:\Windows\System32` 判定）
- 验证窗口是否真的出现：看 `MainWindowTitle`，**不要只看进程存活**（模态框会让进程活着）

---

## 8. 其它约定

- 配置目录 `%APPDATA%\DPPXEGo`；旧 Python 配置 `%APPDATA%\HHXSPxeRebuild` **必须保留**（首次启动自动迁移）
- 版本号当前 **2.0**，格式 `主.次`（界面显示补足三段 `2.0.0`）
- 备份文件命名：`win11x64.WIM.pre-<改动主题>`，全部被 `.gitignore` 排除
- 提交信息用中文，格式 `类型: 摘要`（如 `fix: 修复 PE 里被控端不启动`）

---

## 9. 出问题先看哪里

| 症状 | 先看 |
|---|---|
| PE 里被控端没启动 | `bin\logs\crash.log`（panic 栈）→ 再确认 `Pecmd.ini` / `start_client.cmd` |
| 界面打不开 / 弹框 | 是否漏了 `-tags desktop,production`；PE 里是否误走 Wails |
| 还原镜像失败 | `wimlib` 是否在 PATH；目标分区是否 NTFS；错误码 48/123 |
| 发送文件没反应 | 是否勾选客户端；目标目录是否可写；10 秒告警是否出现 |
| 网络启动一堆 404 | 是 UEFI 正常探测（见 `更新记录.md` TFTP 降噪一节），不是故障 |

更完整的历史坑与定位手册见 `HANDOFF.md`。
