package networkwatch

import (
	"context"
	"testing"
	"time"
)

func TestDetectsNetworkChangesAndSleepWithoutInitialRecovery(t *testing.T) {
	var detector detector
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	if reason := detector.observe(now, "wifi-a"); reason != "" {
		t.Fatal("initial snapshot triggered recovery")
	}
	if reason := detector.observe(now.Add(2*time.Second), "wifi-a"); reason != "" {
		t.Fatal("unchanged network triggered recovery")
	}
	if reason := detector.observe(now.Add(4*time.Second), "wifi-b"); reason != "network changed" {
		t.Fatalf("network change = %q", reason)
	}
	if reason := detector.observe(now.Add(5*time.Minute), "wifi-b"); reason != "system resumed" {
		t.Fatalf("resume with unchanged IP = %q", reason)
	}
	if reason := detector.observe(now.Add(5*time.Minute+2*time.Second), "wifi-b"); reason != "" {
		t.Fatal("resume notification repeated")
	}
}

func TestNetworkWatcherStopsAndReleasesNativeSubscriptions(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	changes := Watch(ctx)
	cancel()
	done := make(chan struct{})
	go func() {
		for range changes {
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("network watcher did not stop")
	}
}
