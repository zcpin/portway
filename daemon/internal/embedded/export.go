//go:build cgo

// cgo 导出层：把 Engine 包装成 C ABI，供 Flutter 通过 dart:ffi 调用。
//
// 约定（Dart 侧必须遵守）：
//   - 除 sshtunnel_free_string / sshtunnel_version 外，所有函数都返回一段
//     堆上的 JSON 字符串，形如 {"ok":true,"data":...} 或 {"ok":false,"error":".."}。
//     调用方必须用 sshtunnel_free_string 释放，不能直接用 libc free——
//     字符串由 Go 侧的同一条 C 运行时分发，跨运行时释放会导致堆损坏。
//   - 句柄来自 sshtunnel_new，同一个句柄可被多个线程使用；不再使用时调用
//     sshtunnel_close 释放。
//   - 事件回调通过 sshtunnel_set_event_handler 注册，注册后引擎会立即补发
//     一次完整快照，因此客户端不必额外拉取列表。
package embedded

/*
#include <stdlib.h>
#include "event_callback.h"
*/
import "C"

import (
	"errors"
	"sync"
	"unsafe"
)

// 引擎注册表。用整数句柄而不是全局单例：客户端切换工作区时会新建引擎，
// 旧引擎需要能继续被安全释放。
var (
	registryMu   sync.RWMutex
	registry     = map[int64]*Engine{}
	registryNext int64
	callbacks    = map[int64]C.sshtunnel_event_cb{}
)

func registerEngine(engine *Engine) int64 {
	registryMu.Lock()
	defer registryMu.Unlock()
	registryNext++
	id := registryNext
	registry[id] = engine
	return id
}

func lookupEngine(id int64) *Engine {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return registry[id]
}

func unregisterEngine(id int64) *Engine {
	registryMu.Lock()
	defer registryMu.Unlock()
	engine := registry[id]
	delete(registry, id)
	delete(callbacks, id)
	return engine
}

func setCallback(id int64, cb C.sshtunnel_event_cb) {
	registryMu.Lock()
	defer registryMu.Unlock()
	callbacks[id] = cb
}

func lookupCallback(id int64) C.sshtunnel_event_cb {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return callbacks[id]
}

// guard 把 Go 字符串转成 C 字符串返回给调用方。
func guard(action func() string) *C.char {
	return C.CString(Call(action))
}

// with 取出句柄对应的引擎再执行操作。
func with(handle C.longlong, action func(*Engine) string) *C.char {
	return guard(func() string {
		engine := lookupEngine(int64(handle))
		if engine == nil {
			return fail(errors.New("引擎句柄无效或已释放"))
		}
		return action(engine)
	})
}

func text(value string) *C.char { return C.CString(value) }

// ---------- 生命周期 ----------

// sshtunnel_new 创建引擎，返回 {"ok":true,"data":{"handle":N}}。
//
// config_path 为空字符串时按默认规则查找配置。
//
//export sshtunnel_new
func sshtunnel_new(configPath *C.char) *C.char {
	return guard(func() string {
		engine, err := New(C.GoString(configPath))
		if err != nil {
			return fail(err)
		}
		return ok(map[string]interface{}{"handle": registerEngine(engine)})
	})
}

// sshtunnel_start 启动引擎（连接 auto_start 的隧道并开启状态监测）。
//
//export sshtunnel_start
func sshtunnel_start(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string {
		if err := engine.Start(); err != nil {
			return fail(err)
		}
		return okPayload(nil)
	})
}

// sshtunnel_stop 停止所有隧道，但保留引擎对象（可再次 Start）。
//
//export sshtunnel_stop
func sshtunnel_stop(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string {
		engine.Stop()
		return okPayload(nil)
	})
}

// sshtunnel_close 停止并释放引擎；调用后句柄立即失效。
//
//export sshtunnel_close
func sshtunnel_close(handle C.longlong) *C.char {
	return guard(func() string {
		engine := unregisterEngine(int64(handle))
		if engine == nil {
			return fail(errors.New("引擎句柄无效或已释放"))
		}
		engine.SetEventHandler(nil)
		engine.Stop()
		return okPayload(nil)
	})
}

// sshtunnel_set_event_handler 注册事件回调，并立即补发一次完整快照。
//
//export sshtunnel_set_event_handler
func sshtunnel_set_event_handler(handle C.longlong, cb C.sshtunnel_event_cb) *C.char {
	return with(handle, func(engine *Engine) string {
		id := int64(handle)
		if cb == nil {
			setCallback(id, nil)
			engine.SetEventHandler(nil)
			return okPayload(nil)
		}

		setCallback(id, cb)
		engine.SetEventHandler(func(event string) {
			current := lookupCallback(id)
			if current == nil {
				return
			}
			// 字符串所有权在这里交给客户端，**不能**在本函数里释放。
			//
			// 原因：Dart 的 NativeCallable.listener 是异步回调——native 调用
			// 返回后才在目标 isolate 上执行回调。若此处 defer free，回调真正读取
			// 时内存已被释放（快照就是这种情形：它在 SetEventHandler 里同步派发，
			// 必然早于回调执行），解析会得到游离数据。
			//
			// 因此约定：收到的事件字符串由客户端读取后调用
			// sshtunnel_free_string 释放。极端情况下（isolate 已销毁、消息被丢弃）
			// 会漏掉一次释放，量级很小且进程随即退出，可以接受。
			C.sshtunnel_invoke_event(current, C.CString(event))
		})
		return okPayload(nil)
	})
}

// sshtunnel_free_string 释放本库返回的字符串。
//
//export sshtunnel_free_string
func sshtunnel_free_string(value *C.char) {
	if value != nil {
		C.free(unsafe.Pointer(value))
	}
}

// sshtunnel_version 返回库版本（静态字符串，不需要释放）。
//
//export sshtunnel_version
func sshtunnel_version() *C.char { return text(Version) }

// ---------- 查询 ----------

//export sshtunnel_tunnels
func sshtunnel_tunnels(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.Tunnels() })
}

//export sshtunnel_status
func sshtunnel_status(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.Status() })
}

//export sshtunnel_logs
func sshtunnel_logs(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.Logs() })
}

//export sshtunnel_keys
func sshtunnel_keys(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.Keys() })
}

//export sshtunnel_ssh_connections
func sshtunnel_ssh_connections(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.SSHConnections() })
}

//export sshtunnel_global_settings
func sshtunnel_global_settings(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.GlobalSettings() })
}

//export sshtunnel_info
func sshtunnel_info(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.Info() })
}

//export sshtunnel_stat_key
func sshtunnel_stat_key(handle C.longlong, path *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.StatKey(C.GoString(path)) })
}

//export sshtunnel_export_config
func sshtunnel_export_config(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.ExportConfig() })
}

//export sshtunnel_list_backups
func sshtunnel_list_backups(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.ListBackups() })
}

//export sshtunnel_read_backup
func sshtunnel_read_backup(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.ReadBackup(C.GoString(name)) })
}

// ---------- 隧道操作 ----------

//export sshtunnel_start_tunnel
func sshtunnel_start_tunnel(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.StartTunnel(C.GoString(name)) })
}

//export sshtunnel_stop_tunnel
func sshtunnel_stop_tunnel(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.StopTunnel(C.GoString(name)) })
}

//export sshtunnel_restart_tunnel
func sshtunnel_restart_tunnel(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.RestartTunnel(C.GoString(name)) })
}

//export sshtunnel_delete_tunnel
func sshtunnel_delete_tunnel(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.DeleteTunnel(C.GoString(name)) })
}

//export sshtunnel_add_tunnel
func sshtunnel_add_tunnel(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.AddTunnel(C.GoString(payload)) })
}

//export sshtunnel_update_tunnel
func sshtunnel_update_tunnel(handle C.longlong, name *C.char, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.UpdateTunnel(C.GoString(name), C.GoString(payload))
	})
}

//export sshtunnel_batch_tunnels
func sshtunnel_batch_tunnels(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.BatchTunnels(C.GoString(payload)) })
}

//export sshtunnel_diagnose_tunnel
func sshtunnel_diagnose_tunnel(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.DiagnoseTunnel(C.GoString(name)) })
}

// ---------- SSH 连接 ----------

//export sshtunnel_add_ssh_connection
func sshtunnel_add_ssh_connection(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.AddSSHConnection(C.GoString(payload)) })
}

//export sshtunnel_update_ssh_connection
func sshtunnel_update_ssh_connection(handle C.longlong, name *C.char, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.UpdateSSHConnection(C.GoString(name), C.GoString(payload))
	})
}

//export sshtunnel_delete_ssh_connection
func sshtunnel_delete_ssh_connection(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.DeleteSSHConnection(C.GoString(name)) })
}

//export sshtunnel_test_ssh_connection
func sshtunnel_test_ssh_connection(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.TestSSHConnection(C.GoString(payload)) })
}

//export sshtunnel_inspect_host_key
func sshtunnel_inspect_host_key(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.InspectHostKey(C.GoString(payload)) })
}

//export sshtunnel_trust_host_key
func sshtunnel_trust_host_key(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.TrustHostKey(C.GoString(payload)) })
}

// ---------- 私钥与配置 ----------

//export sshtunnel_unlock_key
func sshtunnel_unlock_key(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.UnlockKey(C.GoString(payload)) })
}

//export sshtunnel_lock_key
func sshtunnel_lock_key(handle C.longlong, path *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.LockKey(C.GoString(path)) })
}

// ---------- FRP ----------

//export sshtunnel_frp_clients
func sshtunnel_frp_clients(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.FrpClients() })
}

//export sshtunnel_frp_add_client
func sshtunnel_frp_add_client(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.AddFrpClient(C.GoString(payload)) })
}

//export sshtunnel_frp_update_client
func sshtunnel_frp_update_client(handle C.longlong, name *C.char, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.UpdateFrpClient(C.GoString(name), C.GoString(payload))
	})
}

//export sshtunnel_frp_delete_client
func sshtunnel_frp_delete_client(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.DeleteFrpClient(C.GoString(name)) })
}

//export sshtunnel_frp_start_client
func sshtunnel_frp_start_client(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.StartFrpClient(C.GoString(name)) })
}

//export sshtunnel_frp_stop_client
func sshtunnel_frp_stop_client(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.StopFrpClient(C.GoString(name)) })
}

//export sshtunnel_frp_restart_client
func sshtunnel_frp_restart_client(handle C.longlong, name *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.RestartFrpClient(C.GoString(name)) })
}

//export sshtunnel_frp_add_proxy
func sshtunnel_frp_add_proxy(handle C.longlong, client *C.char, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.AddFrpProxy(C.GoString(client), C.GoString(payload))
	})
}

//export sshtunnel_frp_update_proxy
func sshtunnel_frp_update_proxy(handle C.longlong, client *C.char, proxy *C.char, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.UpdateFrpProxy(C.GoString(client), C.GoString(proxy), C.GoString(payload))
	})
}

//export sshtunnel_frp_delete_proxy
func sshtunnel_frp_delete_proxy(handle C.longlong, client *C.char, proxy *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.DeleteFrpProxy(C.GoString(client), C.GoString(proxy))
	})
}

//export sshtunnel_frp_toggle_proxy
func sshtunnel_frp_toggle_proxy(handle C.longlong, client *C.char, proxy *C.char, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string {
		return engine.ToggleFrpProxy(C.GoString(client), C.GoString(proxy), C.GoString(payload))
	})
}

//export sshtunnel_reload
func sshtunnel_reload(handle C.longlong) *C.char {
	return with(handle, func(engine *Engine) string { return engine.Reload() })
}

//export sshtunnel_set_global_settings
func sshtunnel_set_global_settings(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.SetGlobalSettings(C.GoString(payload)) })
}

//export sshtunnel_preview_import
func sshtunnel_preview_import(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.PreviewImport(C.GoString(payload)) })
}

//export sshtunnel_import_config
func sshtunnel_import_config(handle C.longlong, payload *C.char) *C.char {
	return with(handle, func(engine *Engine) string { return engine.ImportConfig(C.GoString(payload)) })
}
