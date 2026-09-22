#ifndef PORTWAY_EVENT_CALLBACK_H
#define PORTWAY_EVENT_CALLBACK_H

// 事件回调：引擎把一条事件 JSON 交给客户端。
//
// 回调由调用方（Dart 侧）注册，可能在引擎的任意 goroutine 上被调用，
// 因此实现必须自行保证线程安全。约定：回调只做入队，立刻返回。
//
// 内存所有权：`event` 的所有权随这次回调转移给接收方。调用方在
// sshtunnel_invoke_event 里已经不再持有它，接收方读取完需要用
// sshtunnel_free_string 释放（Dart 的 listener 回调是异步执行的，
// 所以调用方不能替接收方回收）。
typedef void (*sshtunnel_event_cb)(const char* event);

// 调用回调；cb 为空时不做任何事。
//
// 之所以要有这个壳：cgo 不能直接调用「值形式的 C 函数指针」，
// 必须经过一个真正的 C 函数。又因为本文件同时承担 //export 的声明，
// 函数体放在 event_callback.c 里。
void sshtunnel_invoke_event(sshtunnel_event_cb cb, const char* event);

#endif
