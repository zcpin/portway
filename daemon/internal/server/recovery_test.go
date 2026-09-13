package server

import (
	"testing"
	"time"
)

func TestBatchStopClearsRecoveryIntentAfterRetryExhaustion(t *testing.T) {
	a, _ := snapshotTestServer(t)
	cfg := snapshotTestTunnel("offline", 15491)
	cfg.MaxReconnectAttempts = 1
	manual := false
	cfg.AutoStart = &manual
	if err := a.AddTunnel(cfg); err != nil {
		t.Fatal(err)
	}
	if err := a.StartTunnel(cfg.Name); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(2 * time.Second)
	for {
		state := a.GetTunnels()[0]
		if state.State == "failed" && !state.IsRunning {
			if !state.DesiredRunning {
				t.Fatal("failure lost recovery intent")
			}
			break
		}
		select {
		case <-deadline:
			t.Fatal("tunnel did not exhaust retries")
		case <-time.After(time.Millisecond):
		}
	}
	results, err := a.BatchTunnels("stop", []string{cfg.Name})
	if err != nil || len(results) != 1 || !results[0].OK {
		t.Fatalf("stop: %+v %v", results, err)
	}
	state := a.GetTunnels()[0]
	if state.DesiredRunning || state.IsRunning || state.State != "stopped" {
		t.Fatalf("stop did not cancel recovery: %+v", state)
	}
}
