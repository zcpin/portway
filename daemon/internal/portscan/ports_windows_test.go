//go:build windows

package portscan

import (
	"encoding/binary"
	"testing"
	"unsafe"
)

// TestTCPTableClassConstants 锁定 TCP_TABLE_CLASS 的取值。
//
// 这几个常量一旦写错，parseTCPTable 会按错误的行宽解析整个缓冲区，
// **不会返回任何错误**，只会得到一批看起来像进程号的小整数（2、5、8、11）。
// 这个 bug 曾经真实存在：OWNER_PID_LISTENER 被写成 2（其实是 BASIC_ALL），
// 导致所有 TCP 记录的 PID 都是垃圾数据。用常量断言把它钉死。
func TestTCPTableClassConstants(t *testing.T) {
	cases := []struct {
		name string
		got  uint32
		want uint32
	}{
		// 取自 Windows SDK iprtrmib.h 的 TCP_TABLE_CLASS 枚举
		{"tcpTableOwnerPidListener", tcpTableOwnerPidListener, 3},
		{"tcpTableOwnerPidConnections", tcpTableOwnerPidConnections, 4},
		{"tcpTableOwnerPidAll", tcpTableOwnerPidAll, 5},
		// UDP_TABLE_CLASS 枚举
		{"udpTableOwnerPid", udpTableOwnerPid, 1},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d", c.name, c.got, c.want)
		}
	}
}

// TestMIBRowLayout 锁定 MIB 行结构的大小与字段偏移。
//
// 行宽是解析的隐含前提：结构体字段顺序写错、或平台对齐变化，都会让
// 解析结果整体错位。
func TestMIBRowLayout(t *testing.T) {
	// MIB_TCPROW_OWNER_PID 是 6 个 DWORD，共 24 字节
	if got := unsafe.Sizeof(mibTCPRowOwnerPID{}); got != 24 {
		t.Errorf("sizeof(mibTCPRowOwnerPID) = %d, want 24", got)
	}
	// MIB_TCP6ROW_OWNER_PID：16 + 4 + 4 + 16 + 4 + 4 + 4 + 4 = 56
	if got := unsafe.Sizeof(mibTCP6RowOwnerPID{}); got != 56 {
		t.Errorf("sizeof(mibTCP6RowOwnerPID) = %d, want 56", got)
	}
	// MIB_UDPROW_OWNER_PID 是 3 个 DWORD，共 12 字节
	if got := unsafe.Sizeof(mibUDPRowOwnerPID{}); got != 12 {
		t.Errorf("sizeof(mibUDPRowOwnerPID) = %d, want 12", got)
	}
	// MIB_UDP6ROW_OWNER_PID：16 + 4 + 4 + 4 = 28
	if got := unsafe.Sizeof(mibUDP6RowOwnerPID{}); got != 28 {
		t.Errorf("sizeof(mibUDP6RowOwnerPID) = %d, want 28", got)
	}

	// OwningPID 必须是最后一个字段，且偏移为 20
	row := mibTCPRowOwnerPID{}
	base := uintptr(unsafe.Pointer(&row))
	pidOff := uintptr(unsafe.Pointer(&row.OwningPID)) - base
	if pidOff != 20 {
		t.Errorf("offsetof(OwningPID) = %d, want 20", pidOff)
	}
}

// TestParseTCPTableRejectsWrongStride 确认行宽校验能拦住用错 class 的情况。
//
// 构造一个「行宽与结构体不符」的缓冲区（模拟 BASIC_ALL 的 20 字节行），
// 解析器必须返回空而不是吐出一堆垃圾 PID。
func TestParseTCPTableRejectsWrongStride(t *testing.T) {
	// 2 行 × 20 字节 + 4 字节表头 = 44 字节
	buf := make([]byte, 44)
	binary.LittleEndian.PutUint32(buf[0:], 2) // dwNumEntries
	// 第 0 行：LISTEN, 0.0.0.0, 端口 135（网络字节序）, remote 0, remotePort 0
	binary.LittleEndian.PutUint32(buf[4:], mibTCPStateListen)
	binary.LittleEndian.PutUint32(buf[8:], 0)
	binary.LittleEndian.PutUint32(buf[12:], 0x8700) // 135 的网络字节序
	binary.LittleEndian.PutUint32(buf[16:], 0)
	binary.LittleEndian.PutUint32(buf[20:], 0)

	got := parseTCPTable(buf, afInet, tcpTableOwnerPidListener)
	if len(got) != 0 {
		t.Errorf("解析行宽不符的表应返回空，实际得到 %d 条: %+v", len(got), got)
	}
}

// TestParseTCPTableAcceptsCorrectStride 确认正确行宽的表能被正常解析。
func TestParseTCPTableAcceptsCorrectStride(t *testing.T) {
	rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	buf := make([]byte, 4+rowSize)
	binary.LittleEndian.PutUint32(buf[0:], 1)
	binary.LittleEndian.PutUint32(buf[4:], mibTCPStateListen) // State
	binary.LittleEndian.PutUint32(buf[8:], 0)                 // LocalAddr 0.0.0.0
	binary.LittleEndian.PutUint32(buf[12:], 0x8700)           // LocalPort 135
	binary.LittleEndian.PutUint32(buf[16:], 0)                // RemoteAddr
	binary.LittleEndian.PutUint32(buf[20:], 0)                // RemotePort
	binary.LittleEndian.PutUint32(buf[24:], 1652)             // OwningPID

	got := parseTCPTable(buf, afInet, tcpTableOwnerPidListener)
	if len(got) != 1 {
		t.Fatalf("期望 1 条记录，实际 %d 条", len(got))
	}
	if got[0].LocalPort != 135 {
		t.Errorf("LocalPort = %d, want 135", got[0].LocalPort)
	}
	if got[0].PID != 1652 {
		t.Errorf("PID = %d, want 1652", got[0].PID)
	}
	if got[0].Protocol != ProtocolTCP {
		t.Errorf("Protocol = %q, want %q", got[0].Protocol, ProtocolTCP)
	}
}

// TestParseTCPTableFiltersNonListen 确认非 LISTEN 状态的行被过滤掉。
func TestParseTCPTableFiltersNonListen(t *testing.T) {
	rowSize := int(unsafe.Sizeof(mibTCPRowOwnerPID{}))
	buf := make([]byte, 4+rowSize*2)
	binary.LittleEndian.PutUint32(buf[0:], 2)
	// 第 0 行：ESTABLISHED（状态 5），应被过滤
	binary.LittleEndian.PutUint32(buf[4:], 5)
	binary.LittleEndian.PutUint32(buf[12:], 0x1f90) // 8080
	binary.LittleEndian.PutUint32(buf[24:], 111)
	// 第 1 行：LISTEN，应保留
	binary.LittleEndian.PutUint32(buf[4+rowSize:], mibTCPStateListen)
	binary.LittleEndian.PutUint32(buf[12+rowSize:], 0x8700) // 135
	binary.LittleEndian.PutUint32(buf[24+rowSize:], 222)

	got := parseTCPTable(buf, afInet, tcpTableOwnerPidListener)
	if len(got) != 1 {
		t.Fatalf("期望只保留 LISTEN 行，实际 %d 条", len(got))
	}
	if got[0].PID != 222 {
		t.Errorf("PID = %d, want 222", got[0].PID)
	}
}

// TestParseUDPTableKeepsAllStates 确认 UDP 不过滤状态——UDP 没有状态机。
func TestParseUDPTableKeepsAllStates(t *testing.T) {
	rowSize := int(unsafe.Sizeof(mibUDPRowOwnerPID{}))
	buf := make([]byte, 4+rowSize*2)
	binary.LittleEndian.PutUint32(buf[0:], 2)
	binary.LittleEndian.PutUint32(buf[8:], 0x3500) // 端口 53
	binary.LittleEndian.PutUint32(buf[12:], 3900)
	binary.LittleEndian.PutUint32(buf[8+rowSize:], 0x7b00) // 端口 123
	binary.LittleEndian.PutUint32(buf[12+rowSize:], 4548)

	got := parseUDPTable(buf, afInet)
	if len(got) != 2 {
		t.Fatalf("UDP 应保留全部行，实际 %d 条", len(got))
	}
	if got[0].PID != 3900 || got[1].PID != 4548 {
		t.Errorf("PID = %d, %d, want 3900, 4548", got[0].PID, got[1].PID)
	}
}

// TestIPv4String 确认地址字节序转换正确。
func TestIPv4String(t *testing.T) {
	cases := []struct {
		raw  uint32
		want string
	}{
		{0x0100007F, "127.0.0.1"},   // 小端存放的 127.0.0.1
		{0x00000000, "0.0.0.0"},     // 通配
		{0x0101A8C0, "192.168.1.1"}, // 192.168.1.1
	}
	for _, c := range cases {
		if got := ipv4String(c.raw); got != c.want {
			t.Errorf("ipv4String(0x%08X) = %q, want %q", c.raw, got, c.want)
		}
	}
}

// TestPortFromNetworkOrder 确认端口字节序转换正确。
//
// MIB 里的端口是「网络字节序的 16 位整数被当作小端 DWORD 读入」的结果，
// 所以 135（0x0087）在缓冲区里是 0x8700，8080（0x1F90）是 0x901F。
// 转换方式是把低 16 位的两个字节交换回来。
func TestPortFromNetworkOrder(t *testing.T) {
	cases := []struct {
		raw  uint32
		want int
	}{
		{0x8700, 135},  // 135  = 0x0087
		{0x5000, 80},   // 80   = 0x0050
		{0x901F, 8080}, // 8080 = 0x1F90
		{0x0000, 0},
	}
	for _, c := range cases {
		if got := portFromNetworkOrder(c.raw); got != c.want {
			t.Errorf("portFromNetworkOrder(0x%04X) = %d, want %d", c.raw, got, c.want)
		}
	}
}
