package logger

import "testing"

func TestConcurrentGlobalLoggerInitialization(t *testing.T) {
	const workers = 32
	start := make(chan struct{})
	results := make(chan *Logger, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			l := GetGlobalLogger()
			l.SetLevel(INFO)
			results <- l
		}()
	}
	close(start)
	first := <-results
	if first == nil {
		t.Fatal("日志实例未初始化")
	}
	for i := 1; i < workers; i++ {
		if got := <-results; got != first {
			t.Fatal("并发调用得到了不同的日志实例")
		}
	}
}
