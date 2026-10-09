package frp

import (
	"context"
	"sync"
	"testing"
	"time"
)

func awaitInstanceExit(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("FRP 实例未及时退出")
	}
}

func TestInstanceStopBeforeServiceRun(t *testing.T) {
	mgr := newTestManager(t)
	service, _, _, err := buildService(mgr.store.Path("prod"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	instance := &Instance{name: "prod", svc: service, cancel: cancel, done: done}

	// Reproduce Start publishing the instance before its Run goroutine gets
	// scheduled. Stop must work even though FRP's own cancel function is still nil.
	run := make(chan struct{})
	resume := sync.OnceFunc(func() { close(run) })
	go func() {
		defer close(done)
		<-run
		_ = service.Run(ctx)
		instance.markExited(done, cancel)
	}()
	t.Cleanup(func() { cancel(); resume(); awaitInstanceExit(t, done) })

	stopped := make(chan any, 1)
	go func() {
		defer func() { stopped <- recover() }()
		instance.Stop()
	}()
	select {
	case <-ctx.Done():
	case panicValue := <-stopped:
		t.Fatalf("Stop 在 Run 开始前结束，未取消实例上下文（panic=%v）", panicValue)
	case <-time.After(time.Second):
		t.Fatal("Stop 没有取消尚未开始运行的服务")
	}
	select {
	case panicValue := <-stopped:
		t.Fatalf("Stop 未等待 Run 完成清理（panic=%v）", panicValue)
	default:
	}
	resume()
	select {
	case panicValue := <-stopped:
		if panicValue != nil {
			t.Fatalf("Stop panic: %v", panicValue)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消后 Run/Stop 未及时退出")
	}
	if instance.Running() {
		t.Fatal("停止后仍报告运行中")
	}
}

func TestInstanceConcurrentStopAndRestart(t *testing.T) {
	mgr := newTestManager(t)
	instance := newInstance("prod", mgr.store.Path("prod"))
	t.Cleanup(instance.Stop)
	for attempt := 0; attempt < 5; attempt++ {
		if err := instance.Start(); err != nil {
			t.Fatalf("第 %d 次启动失败: %v", attempt+1, err)
		}
		start, done := make(chan struct{}), make(chan struct{})
		var wg sync.WaitGroup
		for n := 0; n < 8; n++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				instance.Stop()
			}()
		}
		close(start)
		go func() { wg.Wait(); close(done) }()
		awaitInstanceExit(t, done)
		if instance.Running() || instance.LastError() != "" {
			t.Fatalf("停止后状态不正确: running=%v error=%q", instance.Running(), instance.LastError())
		}
	}
}
