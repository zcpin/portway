// Package networkwatch detects network changes and gaps caused by system sleep.
package networkwatch

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/logger"
)

type detector struct {
	lastWall    int64
	fingerprint string
}

func (d *detector) observe(now time.Time, fingerprint string) string {
	reason := ""
	if d.lastWall != 0 {
		switch {
		case now.UnixNano()-d.lastWall > int64(10*time.Second):
			reason = "system resumed"
		case d.fingerprint != fingerprint:
			reason = "network changed"
		}
	}
	d.lastWall, d.fingerprint = now.UnixNano(), fingerprint
	return reason
}

func networkFingerprint() (string, error) {
	interfaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	var addresses []string
	for _, iface := range interfaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			return "", err
		}
		for _, address := range addrs {
			addresses = append(addresses, fmt.Sprintf("%d/%s/%s", iface.Index, iface.Name, address.String()))
		}
	}
	sort.Strings(addresses)
	return strings.Join(addresses, "\n"), nil
}

// Watch coalesces bursts and keeps polling while its consumer is recovering
// tunnels. Native Windows events supplement the cross-platform fallback.
func Watch(ctx context.Context) <-chan string {
	changes := make(chan string, 1)
	go func() {
		defer close(changes)
		native := make(chan string, 1)
		stop := watchNative(func(reason string) {
			select {
			case native <- reason:
			default:
			}
		})
		defer stop()
		var state detector
		if initial, err := networkFingerprint(); err == nil {
			state.observe(time.Now(), initial)
		}
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		var pending string
		var timer *time.Timer
		var ready <-chan time.Time
		defer func() {
			if timer != nil {
				timer.Stop()
			}
		}()
		queue := func(reason string) {
			if reason == "" {
				return
			}
			pending = reason
			if timer == nil {
				timer = time.NewTimer(750 * time.Millisecond)
				ready = timer.C
			}
		}
		failed := false
		for {
			select {
			case <-ctx.Done():
				return
			case reason := <-native:
				queue(reason)
			case <-ticker.C:
				fingerprint, err := networkFingerprint()
				if err != nil {
					if !failed {
						logger.Debug("Network observation failed: %v", err)
					}
					failed = true
					continue
				}
				failed = false
				queue(state.observe(time.Now(), fingerprint))
			case <-ready:
				select {
				case changes <- pending:
				default:
				}
				timer, ready = nil, nil
			}
		}
	}()
	return changes
}
