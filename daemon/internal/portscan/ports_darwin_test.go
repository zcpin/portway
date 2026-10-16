//go:build darwin

package portscan

import (
	"testing"
)

// 真实的 lsof 输出片段。
//
// 注意 COMMAND 列可能含空格（如 "Google Chrome"），但 lsof 默认会截断到
// 9 字符且不含空格，因此按空白切分是安全的。
const sampleLsofTCP = `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
nginx    1234   root    6u  IPv4 0x1234567890abcdef      0t0  TCP 127.0.0.1:8080 (LISTEN)
node     5678   user   20u  IPv6 0xabcdef1234567890      0t0  TCP *:3000 (LISTEN)
sshd     9012   root    3u  IPv4 0x1111111111111111      0t0  TCP *:22 (LISTEN)
python   3456   user    5u  IPv6 0x2222222222222222      0t0  TCP [::1]:8000 (LISTEN)
`

func TestParseLsofTCP(t *testing.T) {
	got := parseLsof(sampleLsofTCP, ProtocolTCP)
	if len(got) != 4 {
		t.Fatalf("期望 4 条，实际 %d 条: %+v", len(got), got)
	}

	if got[0].LocalAddr != "127.0.0.1" || got[0].LocalPort != 8080 || got[0].PID != 1234 {
		t.Errorf("第 0 条 = %s:%d pid=%d, want 127.0.0.1:8080 pid=1234",
			got[0].LocalAddr, got[0].LocalPort, got[0].PID)
	}
	// `*:3000` 的通配地址要归一化成 0.0.0.0
	if got[1].LocalAddr != "0.0.0.0" || got[1].LocalPort != 3000 {
		t.Errorf("第 1 条 = %s:%d, want 0.0.0.0:3000",
			got[1].LocalAddr, got[1].LocalPort)
	}
	// IPv6 带方括号
	if got[3].LocalAddr != "::1" || got[3].LocalPort != 8000 {
		t.Errorf("第 3 条 = %s:%d, want ::1:8000",
			got[3].LocalAddr, got[3].LocalPort)
	}
	if got[0].Protocol != ProtocolTCP {
		t.Errorf("Protocol = %q, want %q", got[0].Protocol, ProtocolTCP)
	}
}

func TestParseLsofUDP(t *testing.T) {
	const sample = `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
mDNSRes  9012  _mdns    4u  IPv4 0x1234567890abcdef      0t0  UDP *:5353
dnsmasq  3456   root    5u  IPv4 0xabcdef1234567890      0t0  UDP 127.0.0.1:53
`
	got := parseLsof(sample, ProtocolUDP)
	if len(got) != 2 {
		t.Fatalf("期望 2 条，实际 %d 条", len(got))
	}
	if got[0].LocalPort != 5353 || got[0].PID != 9012 {
		t.Errorf("第 0 条 = %+v, want 端口 5353 pid 9012", got[0])
	}
	if got[0].Protocol != ProtocolUDP {
		t.Errorf("Protocol = %q, want %q", got[0].Protocol, ProtocolUDP)
	}
}

func TestParseLsofFiltersWrongProtocol(t *testing.T) {
	// 同一个输出里既有 TCP 又有 UDP 时，只返回请求的那一种
	const sample = `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
nginx    1234   root    6u  IPv4 0x1234567890abcdef      0t0  TCP 127.0.0.1:8080 (LISTEN)
mDNSRes  9012  _mdns    4u  IPv4 0xabcdef1234567890      0t0  UDP *:5353
`
	if got := parseLsof(sample, ProtocolTCP); len(got) != 1 {
		t.Errorf("只要 TCP 时应返回 1 条，实际 %d 条", len(got))
	}
	if got := parseLsof(sample, ProtocolUDP); len(got) != 1 {
		t.Errorf("只要 UDP 时应返回 1 条，实际 %d 条", len(got))
	}
}

func TestParseLsofSkipsHeaderAndBadLines(t *testing.T) {
	const sample = `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
short line
nginx    notanumber    6u  IPv4 0x1234      0t0  TCP 127.0.0.1:8080 (LISTEN)
nginx    1234   root    6u  IPv4 0x1234      0t0  TCP 127.0.0.1:8080 (LISTEN)
`
	got := parseLsof(sample, ProtocolTCP)
	if len(got) != 1 {
		t.Fatalf("只应保留 1 条合法记录，实际 %d 条: %+v", len(got), got)
	}
	if got[0].PID != 1234 {
		t.Errorf("PID = %d, want 1234", got[0].PID)
	}
}

func TestParseLsofEmptyInput(t *testing.T) {
	if got := parseLsof("", ProtocolTCP); len(got) != 0 {
		t.Errorf("空输入应返回空，实际 %d 条", len(got))
	}
	// lsof 无匹配时只输出表头
	const headerOnly = `COMMAND   PID   USER   FD   TYPE             DEVICE SIZE/OFF NODE NAME
`
	if got := parseLsof(headerOnly, ProtocolTCP); len(got) != 0 {
		t.Errorf("只有表头应返回空，实际 %d 条", len(got))
	}
}

func TestSplitLsofAddr(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{"127.0.0.1:8080", "127.0.0.1", 8080, false},
		{"*:3000", "0.0.0.0", 3000, false},
		{"[::1]:8000", "::1", 8000, false},
		{"[::]:22", "::", 22, false},
		{"0.0.0.0:80", "0.0.0.0", 80, false},
		{"noport", "", 0, true},
		{"[::1]8080", "", 0, true},
	}
	for _, c := range cases {
		host, port, err := splitLsofAddr(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("splitLsofAddr(%q) 应报错，实际返回 %s:%d", c.in, host, port)
			}
			continue
		}
		if err != nil {
			t.Errorf("splitLsofAddr(%q) 报错: %v", c.in, err)
			continue
		}
		if host != c.wantHost || port != c.wantPort {
			t.Errorf("splitLsofAddr(%q) = %s:%d, want %s:%d",
				c.in, host, port, c.wantHost, c.wantPort)
		}
	}
}

func TestNetSplitHostPort(t *testing.T) {
	cases := []struct {
		in       string
		wantHost string
		wantPort string
		wantErr  bool
	}{
		{"127.0.0.1:8080", "127.0.0.1", "8080", false},
		{"[::1]:8000", "::1", "8000", false},
		{"[fe80::1]:80", "fe80::1", "80", false},
		{"noport", "", "", true},
		{"[::1]8080", "", "", true},
	}
	for _, c := range cases {
		host, port, err := netSplitHostPort(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("netSplitHostPort(%q) 应报错", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("netSplitHostPort(%q) 报错: %v", c.in, err)
			continue
		}
		if host != c.wantHost || port != c.wantPort {
			t.Errorf("netSplitHostPort(%q) = %s:%s, want %s:%s",
				c.in, host, port, c.wantHost, c.wantPort)
		}
	}
}

func TestBaseName(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/usr/sbin/nginx", "nginx"},
		{"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome", "Google Chrome"},
		{"node", "node"},
		{"", ""},
	}
	for _, c := range cases {
		if got := baseName(c.in); got != c.want {
			t.Errorf("baseName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestDedupeDarwin(t *testing.T) {
	entries := []rawEntry{
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 100},
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 100},
		{Protocol: ProtocolTCP, LocalAddr: "127.0.0.1", LocalPort: 8080, PID: 200},
	}
	got := dedupe(entries)
	if len(got) != 2 {
		t.Errorf("去重后应有 2 条，实际 %d 条", len(got))
	}
}
