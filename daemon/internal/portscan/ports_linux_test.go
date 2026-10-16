//go:build linux

package portscan

import (
	"testing"
)

// 真实的 /proc/net/tcp 片段（取自 Linux 6.x，字段以空格分隔）。
//
// 关键校验点：
//   - 0100007F:1F90 → 127.0.0.1:8080（地址小端、端口大端）
//   - st=0A 才是 LISTEN，其它状态要过滤掉
const sampleProcNetTCP = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 12345 1 0000000000000000 100 0 0 10 0
   1: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0        0 23456 1 0000000000000000 100 0 0 10 0
   2: 0100007F:1F91 0100007F:1F90 01 00000000:00000000 00:00000000 00000000  1000        0 34567 1 0000000000000000 100 0 0 10 0
`

func TestParseProcNetTCPFiltersNonListen(t *testing.T) {
	got := parseProcNet(sampleProcNetTCP, ProtocolTCP, "/proc/net/tcp")
	if len(got) != 2 {
		t.Fatalf("期望 2 条 LISTEN，实际 %d 条: %+v", len(got), got)
	}
	if got[0].LocalAddr != "127.0.0.1" || got[0].LocalPort != 8080 {
		t.Errorf("第 0 条 = %s:%d, want 127.0.0.1:8080",
			got[0].LocalAddr, got[0].LocalPort)
	}
	if got[1].LocalAddr != "0.0.0.0" || got[1].LocalPort != 22 {
		t.Errorf("第 1 条 = %s:%d, want 0.0.0.0:22",
			got[1].LocalAddr, got[1].LocalPort)
	}
	if got[0].inode != "12345" {
		t.Errorf("inode = %q, want 12345", got[0].inode)
	}
}

func TestParseProcNetUDPKeepsAllStates(t *testing.T) {
	// UDP 的 st 字段是 07（UDP_CLOSE），但 UDP 无状态机，全部保留
	const sample = `  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000:0035 00000000:0000 07 00000000:00000000 00:00000000 00000000   102        0 11111 1 0000000000000000 0
   1: 0100007F:007B 00000000:0000 07 00000000:00000000 00:00000000 00000000  1000        0 22222 1 0000000000000000 0
`
	got := parseProcNet(sample, ProtocolUDP, "/proc/net/udp")
	if len(got) != 2 {
		t.Fatalf("UDP 应保留全部行，实际 %d 条", len(got))
	}
	if got[0].LocalPort != 53 {
		t.Errorf("端口 = %d, want 53", got[0].LocalPort)
	}
	if got[1].LocalPort != 123 {
		t.Errorf("端口 = %d, want 123", got[1].LocalPort)
	}
}

func TestParseProcNetIPv6(t *testing.T) {
	// ::1 在 /proc/net/tcp6 里是 00000000000000000000000001000000
	// （每 4 字节一组各自小端：最后一组 0000:0001 → 01 00 00 00）
	const sample = `  sl  local_address                         remote_address                        st tx_queue rx_queue tr tm->when retrnsmt   uid  timeout inode
   0: 00000000000000000000000001000000:1F90 00000000000000000000000000000000:0000 0A 00000000:00000000 00:00000000 00000000  1000        0 99999 1 0000000000000000 100 0 0 10 0
`
	got := parseProcNet(sample, ProtocolTCP, "/proc/net/tcp6")
	if len(got) != 1 {
		t.Fatalf("期望 1 条，实际 %d 条", len(got))
	}
	if got[0].LocalAddr != "::1" {
		t.Errorf("地址 = %q, want ::1", got[0].LocalAddr)
	}
	if got[0].LocalPort != 8080 {
		t.Errorf("端口 = %d, want 8080", got[0].LocalPort)
	}
}

func TestParseProcAddrIPv4(t *testing.T) {
	cases := []struct {
		hex  string
		want string
	}{
		{"0100007F", "127.0.0.1"},
		{"00000000", "0.0.0.0"},
		{"0101A8C0", "192.168.1.1"}, // C0 A8 01 01 小端 → 192.168.1.1
		{"08080808", "8.8.8.8"},
	}
	for _, c := range cases {
		if got := parseProcAddr(c.hex, false); got != c.want {
			t.Errorf("parseProcAddr(%q, v4) = %q, want %q", c.hex, got, c.want)
		}
	}
}

func TestParseProcAddrIPv6(t *testing.T) {
	cases := []struct {
		hex  string
		want string
	}{
		// ::1 — 组 3 的 0000:0001 内存字节 00 00 00 01 → 反转 01000000
		{"00000000000000000000000001000000", "::1"},
		// ::（全零）
		{"00000000000000000000000000000000", "::"},
		// fe80::1 — 组 0 的 fe80:0000 内存字节 fe 80 00 00 → 反转 000080fe
		{"000080fe000000000000000001000000", "fe80::1"},
	}
	for _, c := range cases {
		if got := parseProcAddr(c.hex, true); got != c.want {
			t.Errorf("parseProcAddr(%q, v6) = %q, want %q", c.hex, got, c.want)
		}
	}
}

func TestParseProcAddrRejectsBadInput(t *testing.T) {
	// 长度不符或非十六进制都应返回空串而不是 panic
	for _, bad := range []string{"", "ZZZZ", "0100", "0100007F00"} {
		if got := parseProcAddr(bad, false); got != "" {
			t.Errorf("parseProcAddr(%q, v4) = %q, want 空串", bad, got)
		}
	}
	for _, bad := range []string{"", "0100", "0100007F"} {
		if got := parseProcAddr(bad, true); got != "" {
			t.Errorf("parseProcAddr(%q, v6) = %q, want 空串", bad, got)
		}
	}
}

func TestParseSSOutput(t *testing.T) {
	const sample = `tcp   LISTEN 0      128          127.0.0.1:8080       0.0.0.0:*    users:(("nginx",pid=1234,fd=6))
tcp   LISTEN 0      511            0.0.0.0:80         0.0.0.0:*    users:(("nginx",pid=1234,fd=7))
udp   UNCONN 0      0              0.0.0.0:53         0.0.0.0:*    users:(("dnsmasq",pid=5678,fd=4))
tcp   LISTEN 0      128               [::]:22            [::]:*    users:(("sshd",pid=9012,fd=3))
`
	got := parseSSOutput(sample)
	if len(got) != 4 {
		t.Fatalf("期望 4 条，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Protocol != ProtocolTCP || got[0].LocalPort != 8080 || got[0].PID != 1234 {
		t.Errorf("第 0 条 = %+v, want tcp 8080 pid=1234", got[0])
	}
	if got[2].Protocol != ProtocolUDP || got[2].LocalPort != 53 || got[2].PID != 5678 {
		t.Errorf("第 2 条 = %+v, want udp 53 pid=5678", got[2])
	}
	if got[3].LocalAddr != "::" || got[3].LocalPort != 22 {
		t.Errorf("第 3 条 = %s:%d, want [::]:22", got[3].LocalAddr, got[3].LocalPort)
	}
}

func TestSplitHostPortLoose(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
	}{
		{"127.0.0.1:8080", "127.0.0.1", 8080},
		{"0.0.0.0:80", "0.0.0.0", 80},
		{"*:8080", "0.0.0.0", 8080},
		{"[::]:22", "::", 22},
		{"[::1]:8080", "::1", 8080},
	}
	for _, c := range cases {
		host, port, err := splitHostPortLoose(c.in)
		if err != nil {
			t.Errorf("splitHostPortLoose(%q) 报错: %v", c.in, err)
			continue
		}
		if host != c.wantHost || port != c.wantPort {
			t.Errorf("splitHostPortLoose(%q) = %s:%d, want %s:%d",
				c.in, host, port, c.wantHost, c.wantPort)
		}
	}
}

func TestExtractSSPID(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{`users:(("nginx",pid=1234,fd=6))`, 1234},
		{`users:(("node",pid=99,fd=20))`, 99},
		{`users:(("a",pid=1,fd=1),("b",pid=2,fd=2))`, 1},
		{`no pid here`, 0},
		{``, 0},
	}
	for _, c := range cases {
		if got := extractSSPID(c.in); got != c.want {
			t.Errorf("extractSSPID(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestUserFromStatus(t *testing.T) {
	const status = `Name:	nginx
Umask:	0022
State:	S (sleeping)
Tgid:	1234
Pid:	1234
Uid:	0	0	0	0
Gid:	0	0	0	0
`
	// root 的 UID 是 0，/etc/passwd 里应能查到 root
	got := userFromStatus(status)
	if got != "root" && got != "0" {
		t.Errorf("userFromStatus = %q, want root 或 0", got)
	}
}

func TestUserFromStatusMissingUid(t *testing.T) {
	if got := userFromStatus("Name:\tfoo\n"); got != "" {
		t.Errorf("缺少 Uid 行应返回空串，实际 %q", got)
	}
}

func TestStartTimeFromStatHandlesParensInComm(t *testing.T) {
	// comm 里含空格和括号时，必须从最后一个 ')' 之后开始切分
	const stat = `1234 (my proc (v2)) S 1 1234 1234 0 -1 4194560 100 0 0 0 1 2 0 0 20 0 1 0 12345678 0 0 0 0 0 0 0 0 0 0 0 0 0 0`
	got := startTimeFromStat(stat)
	if got.IsZero() {
		t.Fatal("含括号的 comm 应能正确解析，实际返回零值")
	}
}

func TestStartTimeFromStatRejectsShortInput(t *testing.T) {
	if got := startTimeFromStat("1234 (x) S"); !got.IsZero() {
		t.Errorf("字段不足应返回零值，实际 %v", got)
	}
	if got := startTimeFromStat("no parens here"); !got.IsZero() {
		t.Errorf("缺少 ')' 应返回零值，实际 %v", got)
	}
}

func TestDedupeLinux(t *testing.T) {
	entries := []rawEntry{
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 100},
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 100},
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 200},
		{Protocol: ProtocolUDP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 100},
	}
	got := dedupe(entries)
	if len(got) != 3 {
		t.Errorf("去重后应有 3 条，实际 %d 条: %+v", len(got), got)
	}
}
