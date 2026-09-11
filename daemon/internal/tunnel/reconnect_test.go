package tunnel

import (
	"testing"
	"time"
)

func TestParseStrategyRejectsNonPositiveInterval(t *testing.T) {
	for _, strategy := range []string{"fixed", "exponential"} {
		for _, interval := range []time.Duration{0, -time.Second} {
			if _, err := ParseStrategy(strategy, interval); err == nil {
				t.Errorf("ParseStrategy(%q, %v) 应当报错，0 间隔会退化成空转热循环", strategy, interval)
			}
		}
	}
}

func TestParseStrategyAcceptsPositiveInterval(t *testing.T) {
	strategy, err := ParseStrategy("exponential", 3*time.Second)
	if err != nil {
		t.Fatalf("ParseStrategy() error = %v", err)
	}
	if got := strategy.GetInterval(1); got <= 0 {
		t.Errorf("GetInterval(1) = %v, want > 0", got)
	}
}
