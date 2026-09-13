package tunnel

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNetworkRecoveryPreservesHealthyConnectionAndManualStop(t *testing.T) {
	dialed := make(chan *fakeSSHConn, 4)
	tun := newTestTunnel(t, dialed)
	t.Cleanup(tun.Stop)
	if tun.RecoverNetwork(context.Background(), "network changed") {
		t.Fatal("never-started tunnel recovered")
	}
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	first := waitConn(t, dialed)
	before := awaitState(t, tun, StateConnected)
	if tun.RecoverNetwork(context.Background(), "system resumed") || tun.Status() != before {
		t.Fatal("healthy connection was replaced")
	}
	first.failReq.Store(true)
	if !tun.RecoverNetwork(context.Background(), "network changed") {
		t.Fatal("unhealthy connection was not recovered")
	}
	second := waitConn(t, dialed)
	awaitState(t, tun, StateConnected)
	if first == second {
		t.Fatal("recovery reused the dead connection")
	}
	awaitSignal(t, first.closed, "old transport was not released")
	tun.Stop()
	if tun.Status().DesiredRunning || tun.RecoverNetwork(context.Background(), "system resumed") {
		t.Fatal("manual stop did not cancel recovery intent")
	}
}

func TestNetworkRecoveryInterruptsLongBackoff(t *testing.T) {
	dialed := make(chan *fakeSSHConn, 4)
	tun := newTestTunnel(t, dialed)
	t.Cleanup(tun.Stop)
	tun.strategy, _ = ParseStrategy("fixed", time.Hour)
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	first := waitConn(t, dialed)
	awaitState(t, tun, StateConnected)
	first.Close()
	awaitState(t, tun, StateReconnecting)
	if !tun.RecoverNetwork(context.Background(), "network changed") {
		t.Fatal("recovery did not wake backoff")
	}
	_ = waitConn(t, dialed)
	awaitState(t, tun, StateConnected)
}

func TestRetryExhaustionRecoversUnlessManuallyStopped(t *testing.T) {
	for _, manualStop := range []bool{false, true} {
		t.Run(map[bool]string{false: "recover", true: "stopped"}[manualStop], func(t *testing.T) {
			tun := newTestTunnel(t, nil)
			t.Cleanup(tun.Stop)
			tun.config.MaxReconnectAttempts = 1
			var calls atomic.Int32
			tun.dial = func(context.Context) (sshConn, error) {
				if calls.Add(1) == 1 {
					return nil, errors.New("network offline")
				}
				return newFakeSSHConn(), nil
			}
			if err := tun.Start(); err != nil {
				t.Fatal(err)
			}
			awaitState(t, tun, StateFailed)
			tun.mu.Lock()
			wg := tun.wg
			tun.mu.Unlock()
			wg.Wait()
			if !tun.Status().DesiredRunning {
				t.Fatal("retry exhaustion lost start intent")
			}
			if manualStop {
				tun.Stop()
			}
			recovered := tun.RecoverNetwork(context.Background(), "network changed")
			if recovered == manualStop {
				t.Fatalf("recovered=%v manualStop=%v", recovered, manualStop)
			}
			if recovered {
				awaitState(t, tun, StateConnected)
			}
		})
	}
}

func TestManualStopCancelsRecoveryWaitingForOldDial(t *testing.T) {
	tun := newTestTunnel(t, nil)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	var calls atomic.Int32
	t.Cleanup(func() { once.Do(func() { close(release) }); tun.Stop() })
	tun.dial = func(ctx context.Context) (sshConn, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-ctx.Done()
			<-release
			return nil, ctx.Err()
		}
		return newFakeSSHConn(), nil
	}
	if err := tun.Start(); err != nil {
		t.Fatal(err)
	}
	awaitSignal(t, started, "initial dial did not begin")
	if !tun.RecoverNetwork(context.Background(), "system resumed") {
		t.Fatal("recovery was not scheduled")
	}
	stopped := make(chan struct{})
	go func() { tun.Stop(); close(stopped) }()
	awaitState(t, tun, StateStopped)
	once.Do(func() { close(release) })
	awaitSignal(t, stopped, "stop was blocked by recovery")
	if calls.Load() != 1 || tun.Status().DesiredRunning || tun.IsRunning() {
		t.Fatal("recovery restarted a manually stopped tunnel")
	}
}
