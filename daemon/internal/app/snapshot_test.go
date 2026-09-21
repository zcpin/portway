package app

import (
	"testing"
	"time"

	"github.com/byteporter/portway/internal/config"
)

type eventEmitterFunc func(string, interface{})

func (f eventEmitterFunc) Emit(event string, data interface{}) { f(event, data) }

func TestInitialSnapshotCannotOverwriteNewerStatus(t *testing.T) {
	a, _ := settingsApp(t)
	if err := a.AddTunnel(config.Tunnel{
		Name: "probe", LocalPort: 15432, RemoteHost: "127.0.0.1", RemotePort: 5432,
		SSHHost: "127.0.0.1:1", SSHUser: "tester", KeyFile: "keys/missing",
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.StartTunnel("probe"); err != nil {
		t.Fatal(err)
	}

	type stateEvent struct {
		kind    string
		running bool
	}
	events := make(chan stateEvent, 4)
	snapshotRead := make(chan struct{})
	releaseSnapshot := make(chan struct{})
	emitter := eventEmitterFunc(func(event string, data interface{}) {
		switch event {
		case "snapshot":
			close(snapshotRead)
			<-releaseSnapshot
			events <- stateEvent{event, data.([]TunnelInfo)[0].IsRunning}
		case "status":
			events <- stateEvent{event, data.(map[string]bool)["probe"]}
		}
	})
	a.SetEmitter(emitter)
	snapshotDone := make(chan struct{})
	go func() {
		a.SendSnapshot(emitter)
		close(snapshotDone)
	}()
	select {
	case <-snapshotRead:
	case <-time.After(2 * time.Second):
		close(releaseSnapshot)
		t.Fatal("snapshot did not start")
	}

	var stopErr error
	stopDone := make(chan struct{})
	go func() {
		stopErr = a.StopTunnel("probe")
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Error("new status was emitted before the pending snapshot")
	case <-time.After(100 * time.Millisecond):
	}
	if a.GetStatus()["probe"] {
		t.Error("concurrent stop did not update the runtime state")
	}
	close(releaseSnapshot)
	for _, done := range []<-chan struct{}{snapshotDone, stopDone} {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("snapshot or status emission is blocked")
		}
	}
	if stopErr != nil {
		t.Fatal(stopErr)
	}
	for _, want := range []stateEvent{{"snapshot", true}, {"status", true}, {"status", false}} {
		select {
		case got := <-events:
			if got != want {
				t.Fatalf("event = %+v, want %+v", got, want)
			}
		default:
			t.Fatalf("missing event: %+v", want)
		}
	}
}
