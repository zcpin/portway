package networkwatch

import (
	"runtime"
	"unsafe"

	"github.com/byteporter/ssh-tunnel/internal/logger"
	"golang.org/x/sys/windows"
)

func watchNative(signal func(string)) func() {
	ipHelper := windows.NewLazySystemDLL("iphlpapi.dll")
	cancelIP := ipHelper.NewProc("CancelMibChangeNotify2")
	addressCallback := windows.NewCallback(func(_, _, _ uintptr) uintptr {
		signal("network changed")
		return 0
	})
	var handles []uintptr
	for _, name := range []string{"NotifyUnicastIpAddressChange", "NotifyRouteChange2"} {
		proc := ipHelper.NewProc(name)
		if err := proc.Find(); err != nil {
			logger.Debug("%s unavailable: %v", name, err)
			continue
		}
		var handle uintptr
		code, _, _ := proc.Call(0, addressCallback, 0, 0, uintptr(unsafe.Pointer(&handle)))
		if code == 0 {
			handles = append(handles, handle)
		} else {
			logger.Debug("%s failed: %d", name, code)
		}
	}
	power := windows.NewLazySystemDLL("powrprof.dll")
	register := power.NewProc("PowerRegisterSuspendResumeNotification")
	unregister := power.NewProc("PowerUnregisterSuspendResumeNotification")
	callback := windows.NewCallback(func(_, event, _ uintptr) uintptr {
		if event == 0x12 || event == 0x07 || event == 0x06 {
			signal("system resumed")
		}
		return 0
	})
	parameters := &struct{ Callback, Context uintptr }{Callback: callback}
	var powerHandle uintptr
	if err := register.Find(); err == nil {
		code, _, _ := register.Call(2, uintptr(unsafe.Pointer(parameters)), uintptr(unsafe.Pointer(&powerHandle)))
		if code != 0 {
			logger.Debug("Power notifications unavailable: %d", code)
			powerHandle = 0
		}
	}
	return func() {
		for _, handle := range handles {
			_, _, _ = cancelIP.Call(handle)
		}
		if powerHandle != 0 {
			_, _, _ = unregister.Call(powerHandle)
		}
		runtime.KeepAlive(parameters)
	}
}
