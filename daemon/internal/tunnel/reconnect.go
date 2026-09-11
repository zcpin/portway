package tunnel

import (
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// ReconnectStrategy defines the reconnection behavior
type ReconnectStrategy interface {
	// GetInterval returns the time to wait before the next reconnection attempt
	GetInterval(attempt int) time.Duration
	// ShouldContinue returns whether to continue reconnecting
	ShouldContinue(attempt int, maxAttempts int) bool
	// Reset resets the strategy state
	Reset()
}

// FixedIntervalStrategy uses a fixed interval between reconnection attempts
type FixedIntervalStrategy struct {
	interval time.Duration
}

// NewFixedIntervalStrategy creates a new fixed interval strategy
func NewFixedIntervalStrategy(interval time.Duration) *FixedIntervalStrategy {
	return &FixedIntervalStrategy{
		interval: interval,
	}
}

// GetInterval returns the fixed interval
func (s *FixedIntervalStrategy) GetInterval(attempt int) time.Duration {
	return s.interval
}

// ShouldContinue returns whether to continue reconnecting
func (s *FixedIntervalStrategy) ShouldContinue(attempt int, maxAttempts int) bool {
	return maxAttempts == 0 || attempt < maxAttempts
}

// Reset resets the strategy state
func (s *FixedIntervalStrategy) Reset() {
	// No state to reset for fixed interval
}

// ExponentialBackoffStrategy uses exponential backoff with optional jitter
type ExponentialBackoffStrategy struct {
	baseInterval    time.Duration
	maxInterval     time.Duration
	multiplier      float64
	jitter          bool
	randomGenerator func() float64
}

// NewExponentialBackoffStrategy creates a new exponential backoff strategy
func NewExponentialBackoffStrategy(baseInterval, maxInterval time.Duration, multiplier float64, jitter bool) *ExponentialBackoffStrategy {
	return &ExponentialBackoffStrategy{
		baseInterval: baseInterval,
		maxInterval:  maxInterval,
		multiplier:   multiplier,
		jitter:       jitter,
		// 在构造时确定随机源：GetInterval 可能被新旧两次 run 并发调用，
		// 惰性初始化会产生数据竞争
		randomGenerator: rand.Float64,
	}
}

// GetInterval returns the exponentially increasing interval
func (s *ExponentialBackoffStrategy) GetInterval(attempt int) time.Duration {
	// Calculate exponential backoff
	interval := float64(s.baseInterval) * math.Pow(s.multiplier, float64(attempt))

	// Cap at max interval
	if interval > float64(s.maxInterval) {
		interval = float64(s.maxInterval)
	}

	// Add jitter if enabled (±25%)
	if s.jitter {
		jitterRange := interval * 0.25
		jitterOffset := (s.randomGenerator() - 0.5) * 2 * jitterRange
		interval += jitterOffset
	}

	return time.Duration(interval)
}

// ShouldContinue returns whether to continue reconnecting
func (s *ExponentialBackoffStrategy) ShouldContinue(attempt int, maxAttempts int) bool {
	return maxAttempts == 0 || attempt < maxAttempts
}

// Reset resets the strategy state
func (s *ExponentialBackoffStrategy) Reset() {
	// No state to reset for exponential backoff
}

// ParseStrategy parses a strategy string and returns a ReconnectStrategy
func ParseStrategy(strategyType string, interval time.Duration) (ReconnectStrategy, error) {
	// 间隔为 0 会让 time.After(0) 立刻返回，重连退化成空转热循环
	if interval <= 0 {
		return nil, fmt.Errorf("reconnect interval must be greater than 0, got %v", interval)
	}

	switch strategyType {
	case "fixed":
		return NewFixedIntervalStrategy(interval), nil
	case "exponential":
		// Use default values for exponential backoff
		// Base interval from config, max interval of 5 minutes, multiplier of 2
		maxInterval := 5 * time.Minute
		if interval > maxInterval {
			maxInterval = interval * 10
		}
		return NewExponentialBackoffStrategy(interval, maxInterval, 2.0, true), nil
	default:
		return nil, fmt.Errorf("unknown strategy type: %s", strategyType)
	}
}
