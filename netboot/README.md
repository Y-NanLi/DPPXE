# netboot 目录说明

本目录存放 PXE 启动所需的引导资源：EFI / BIOS / IPXE 引导文件、
BCD、BOOT.SDI、菜单（`menu.txt`）等。

## ⚠️ win11x64.WIM 不在本仓库内

`boot/win11x64.WIM`（一个约 585MB 的 Windows PE 镜像）**未提交到仓库**。

原因：GitHub 对普通仓库的单文件硬性上限是 100MB，该文件远超此限制，
且作为大体积二进制也不适合纳入 Git 版本控制。它已在仓库根目录的
`.gitignore` 中被显式排除：

    netboot/boot/win11x64.WIM

### 使用前请自行准备 PE 镜像

仓库克隆下来后，`netboot/boot/` 下**没有** `win11x64.WIM`。请自行获取
Windows PE 镜像并放到该路径：

    netboot/boot/win11x64.WIM

- 可自行基于 Windows ADK（或第三方 PE 制作工具）生成 PE，再导出为 WIM；
  也可使用你已有的 PE 镜像。
- 文件名 / 路径需与 DP-PXE 服务配置中对 PE 镜像的引用保持一致；
  若使用其它名称或位置，请相应修改服务端的路径配置。

## 其它被排除的文件

仓库既有 `.gitignore` 包含 `*.exe`，因此以下二进制也不入库，由本地运行
环境提供：

- `netboot/bootmgr.exe`（Windows 引导管理器）
- `netboot/vnc/tvnserver.exe`、`netboot/vnc/tvnviewer.exe`（VNC 工具）

这些文件体积不大，但属于二进制引导/工具，按仓库统一策略不纳入版本控制。
