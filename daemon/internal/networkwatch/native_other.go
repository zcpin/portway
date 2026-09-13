//go:build !windows

package networkwatch

func watchNative(signal func(string)) func() { return func() {} }
