package client

import (
	"encoding/binary"
	"fmt"
	"math"
	"net"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"

	"dppxe/internal/services"
)

// IOCTL 常量（与 client/disks.py 一致）。
const (
	ioctlDiskGetLengthInfo      = 0x0007405C
	ioctlDiskGetDriveLayoutEx   = 0x00070050
	ioctlDiskGetDriveGeometryEx = 0x000700A0
	ioctlStorageQueryProperty   = 0x002D1400
)

var styleNames = map[uint32]string{0: "MBR", 1: "GPT", 2: "RAW"}

// ListDisks 枚举物理磁盘：序号 / 型号 / 容量 / 分区表类型。
//
// 纯 Win32 API（CreateFile + DeviceIoControl），不依赖 wmic / WMI，WinPE 里也能用。
func ListDisks() ([]services.DiskInfo, error) {
	var out []services.DiskInfo
	for i := 0; i < 16; i++ {
		path := fmt.Sprintf(`\\.\PhysicalDrive%d`, i)
		ptr, err := windows.UTF16PtrFromString(path)
		if err != nil {
			continue
		}
		share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE)
		h, err := windows.CreateFile(ptr, windows.GENERIC_READ, share, nil,
			windows.OPEN_EXISTING, 0, 0)
		if err != nil {
			// 没有读权限时退回 0 权限
			h, err = windows.CreateFile(ptr, 0, share, nil, windows.OPEN_EXISTING, 0, 0)
			if err != nil {
				continue
			}
		}
		func() {
			defer windows.CloseHandle(h)
			total := diskSize(h)
			if total <= 0 {
				return
			}
			out = append(out, services.DiskInfo{
				Index:     i,
				Model:     diskModel(h),
				SizeBytes: total,
				SizeGB:    int(math.Round(float64(total) / (1024 * 1024 * 1024))),
				Style:     diskStyle(h),
			})
		}()
	}
	return out, nil
}

func deviceIoControl(h windows.Handle, code uint32, in []byte, outSize int) []byte {
	buf := make([]byte, outSize)
	var ret uint32
	var inPtr *byte
	if len(in) > 0 {
		inPtr = &in[0]
	}
	err := windows.DeviceIoControl(h, code, inPtr, uint32(len(in)),
		&buf[0], uint32(len(buf)), &ret, nil)
	if err != nil || ret == 0 {
		return nil
	}
	return buf[:ret]
}

func diskSize(h windows.Handle) int64 {
	if raw := deviceIoControl(h, ioctlDiskGetLengthInfo, nil, 8); len(raw) >= 8 {
		return int64(binary.LittleEndian.Uint64(raw))
	}
	// 没有读权限时退回几何信息（DISK_GEOMETRY_EX: Geometry(24) + DiskSize(8)）
	raw := deviceIoControl(h, ioctlDiskGetDriveGeometryEx, nil, 256)
	if len(raw) < 32 {
		return 0
	}
	return int64(binary.LittleEndian.Uint64(raw[24:32]))
}

func diskModel(h windows.Handle) string {
	// STORAGE_PROPERTY_QUERY: PropertyId=StorageDeviceProperty(0), QueryType=Standard(0)
	query := make([]byte, 12)
	raw := deviceIoControl(h, ioctlStorageQueryProperty, query, 1024)
	if len(raw) < 20 {
		return ""
	}
	off := int(binary.LittleEndian.Uint32(raw[16:20])) // ProductIdOffset
	if off <= 0 || off >= len(raw) {
		return ""
	}
	end := len(raw)
	for i := off; i < len(raw); i++ {
		if raw[i] == 0 {
			end = i
			break
		}
	}
	return strings.Join(strings.Fields(string(raw[off:end])), " ")
}

func diskStyle(h windows.Handle) string {
	raw := deviceIoControl(h, ioctlDiskGetDriveLayoutEx, nil, 4096)
	if len(raw) < 4 {
		return ""
	}
	return styleNames[binary.LittleEndian.Uint32(raw[:4])]
}

// ---------------------------------------------------------------- 网络工具

// netInterfaces 返回所有物理网卡的 MAC（整数形式）。
func netInterfaces() ([]uint64, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []uint64
	for _, ifc := range ifaces {
		if len(ifc.HardwareAddr) != 6 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		var v uint64
		for _, b := range ifc.HardwareAddr {
			v = v<<8 | uint64(b)
		}
		out = append(out, v)
	}
	return out, nil
}

func dialTCP(host string, port int, timeout time.Duration) (net.Conn, error) {
	return net.DialTimeout("tcp4", net.JoinHostPort(host, fmt.Sprint(port)), timeout)
}

func localAddr(conn net.Conn) string {
	if conn == nil || conn.LocalAddr() == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(conn.LocalAddr().String())
	if err != nil {
		return conn.LocalAddr().String()
	}
	return host
}

// hideWindow 启动子进程时不弹黑窗口。
func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

// hideWindowCmdLine 启动子进程不弹窗，并用**原样命令行**交给 CreateProcess。
//
// 为什么需要它：exec.Command("cmd", "/c", cmdline) 时，Go 会把 cmdline 里
// 的引号转义成 \"，而 cmd.exe 不认反斜杠转义，结果带空格/引号的路径（如
// diskpart /s "X:\Windows\Temp\x.txt"）会被吃坏，diskpart 报"无法打开脚本文件"。
// 直接给 SysProcAttr.CmdLine 可以绕过 Go 的转义，由我们自行保证引号正确。
func hideWindowCmdLine(cmd *exec.Cmd, cmdline string) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: 0x08000000,
		CmdLine:       cmdline,
	}
}
