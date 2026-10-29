package tasks

import (
	"strings"
	"testing"
)

func TestBuildDiskpartMBRPrimaryCount(t *testing.T) {
	// 3 个分区 / 2 个主分区 → 2 主 + 扩展 + 1 逻辑
	sp := &PartitionSpec{
		Disk: 0, Table: "mbr", Count: 3, PrimaryCount: 2,
		SizesGB:     []int{100, 100, 0},
		Filesystems: []string{"NTFS", "NTFS", "NTFS"},
		Labels:      []string{"系统", "软件", "文档"},
	}
	script := BuildDiskpart(sp)
	if n := strings.Count(script, "Create partition primary"); n != 2 {
		t.Errorf("应 2 个主分区，实际 %d:\n%s", n, script)
	}
	if !strings.Contains(script, "Create partition extended") {
		t.Errorf("应创建扩展分区:\n%s", script)
	}
	if n := strings.Count(script, "Create partition logical"); n != 1 {
		t.Errorf("应 1 个逻辑分区，实际 %d:\n%s", n, script)
	}
	if !strings.Contains(script, "select partition 5") {
		t.Errorf("MBR 逻辑分区应从 5 开始编号:\n%s", script)
	}

	// 4 个分区 / 4 个主分区 → 全主分区，无扩展
	sp2 := &PartitionSpec{
		Disk: 0, Table: "mbr", Count: 4, PrimaryCount: 4,
		SizesGB:     []int{100, 100, 100, 0},
		Filesystems: []string{"NTFS", "NTFS", "NTFS", "NTFS"},
	}
	s2 := BuildDiskpart(sp2)
	if n := strings.Count(s2, "Create partition primary"); n != 4 {
		t.Errorf("应 4 个主分区，实际 %d:\n%s", n, s2)
	}
	if strings.Contains(s2, "Create partition extended") {
		t.Errorf("全主分区时不应有扩展分区:\n%s", s2)
	}

	// 5 个分区 / 主分区填 4 → 要留槽给扩展，实际最多 3 主
	sp3 := &PartitionSpec{
		Disk: 0, Table: "mbr", Count: 5, PrimaryCount: 4,
		SizesGB:     []int{100, 100, 100, 100, 0},
		Filesystems: []string{"NTFS", "NTFS", "NTFS", "NTFS", "NTFS"},
	}
	s3 := BuildDiskpart(sp3)
	if n := strings.Count(s3, "Create partition primary"); n != 3 {
		t.Errorf("有逻辑分区时主分区最多 3 个，实际 %d:\n%s", n, s3)
	}
	if !strings.Contains(s3, "Create partition extended") {
		t.Errorf("应创建扩展分区:\n%s", s3)
	}

	// GPT 不受 primary_count 影响：全 primary
	sp4 := &PartitionSpec{
		Disk: 0, Table: "gpt", Count: 3, PrimaryCount: 1,
		SizesGB:     []int{100, 100, 0},
		Filesystems: []string{"NTFS", "NTFS", "NTFS"},
	}
	s4 := BuildDiskpart(sp4)
	if n := strings.Count(s4, "Create partition primary"); n != 3 {
		t.Errorf("GPT 应 3 个 primary，实际 %d:\n%s", n, s4)
	}

	// 逐分区标记 [主, 逻辑, 逻辑] → 1 主 + 2 逻辑
	sp5 := &PartitionSpec{
		Disk: 0, Table: "mbr", Count: 3, Primaries: []bool{true, false, false},
		SizesGB:     []int{100, 100, 0},
		Filesystems: []string{"NTFS", "NTFS", "NTFS"},
	}
	s5 := BuildDiskpart(sp5)
	if n := strings.Count(s5, "Create partition primary"); n != 1 {
		t.Errorf("逐分区标记应 1 主，实际 %d:\n%s", n, s5)
	}
	if n := strings.Count(s5, "Create partition logical"); n != 2 {
		t.Errorf("逐分区标记应 2 逻辑，实际 %d:\n%s", n, s5)
	}

	// 非前缀标记 [主, 逻辑, 主] 会被收敛成前缀 → 1 主 + 2 逻辑
	sp6 := &PartitionSpec{
		Disk: 0, Table: "mbr", Count: 3, Primaries: []bool{true, false, true},
		SizesGB:     []int{100, 100, 0},
		Filesystems: []string{"NTFS", "NTFS", "NTFS"},
	}
	s6 := BuildDiskpart(sp6)
	if n := strings.Count(s6, "Create partition primary"); n != 1 {
		t.Errorf("非前缀标记应收敛为 1 主，实际 %d:\n%s", n, s6)
	}
}
