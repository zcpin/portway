package server

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/byteporter/ssh-tunnel/internal/logger"
	"github.com/coder/websocket"
)

const (
	writeTimeout = 5 * time.Second
	pingInterval = 30 * time.Second
	// clientSendBuffer 是单个连接的发送队列长度；队列写满说明该客户端消费过慢
	clientSendBuffer = 256
)

// client 是一个 WebSocket 连接及其发送队列。
type client struct {
	conn *websocket.Conn
	send chan []byte
	done chan struct{}
	once sync.Once
}

// closeDone 幂等地通知写循环退出。
func (c *client) closeDone() {
	c.once.Do(func() { close(c.done) })
}

// Hub 维护所有 WebSocket 连接，并把业务事件广播出去。
// 它实现了 app.EventEmitter 接口。
//
// 每个连接有独立的发送队列与写 goroutine：Emit 只做非阻塞入队，
// 慢客户端不会拖住调用方（日志与状态广播都在 Emit 这条路径上）；
// 队列写满时直接断开该连接，客户端重连后可重新拿到全量状态。
type Hub struct {
	mu    sync.RWMutex
	conns map[*client]struct{}
}

func NewHub() *Hub {
	return &Hub{conns: make(map[*client]struct{})}
}

// Emit 推送一条事件，格式为 {"type":"<event>","<event>":<data>}。
func (h *Hub) Emit(event string, data interface{}) {
	payload, err := json.Marshal(map[string]interface{}{
		"type": event,
		event:  data,
	})
	if err != nil {
		logger.Error("failed to marshal event %s: %v", event, err)
		return
	}

	h.mu.RLock()
	clients := make([]*client, 0, len(h.conns))
	for c := range h.conns {
		clients = append(clients, c)
	}
	h.mu.RUnlock()

	for _, c := range clients {
		select {
		case c.send <- payload:
		default:
			// 慢客户端：断开而不是阻塞广播。
			// 用 CloseNow 立即断开：Close 会等待关闭握手（最长 5 秒），
			// 那会把 Emit 的调用方重新拖住
			h.remove(c)
			_ = c.conn.CloseNow()
			// 日志钩子会再次调用 Emit，必须先移除满队列的连接。
			logger.Debug("websocket client too slow, dropping connection")
		}
	}
}

// ClientCount 返回当前连接数。
func (h *Hub) ClientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.conns)
}

// ServeHTTP 接受 WebSocket 升级并维持连接，直到客户端断开。
func (h *Hub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	wsConn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// 本地客户端 origin 不固定（Flutter 桌面端可能不带 origin），跳过校验
		InsecureSkipVerify: true,
	})
	if err != nil {
		logger.Error("websocket accept failed: %v", err)
		return
	}

	c := &client{
		conn: wsConn,
		send: make(chan []byte, clientSendBuffer),
		done: make(chan struct{}),
	}
	h.add(c)
	defer func() {
		h.remove(c)
		_ = wsConn.CloseNow()
	}()

	ctx := r.Context()
	logger.Debug("websocket client connected (%d total)", h.ClientCount())

	// 写循环：唯一对该连接执行 Write 的 goroutine
	go h.writeLoop(c)

	// ping 循环：探测僵死连接
	go func() {
		ticker := time.NewTicker(pingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
				pingCtx, cancel := context.WithTimeout(context.Background(), writeTimeout)
				err := wsConn.Ping(pingCtx)
				cancel()
				if err != nil {
					h.remove(c)
					_ = wsConn.CloseNow()
					return
				}
			}
		}
	}()

	// 读循环：客户端关闭或出错时返回，触发 defer 清理
	for {
		if _, _, err := wsConn.Read(ctx); err != nil {
			return
		}
	}
}

func (h *Hub) writeLoop(c *client) {
	for {
		select {
		case <-c.done:
			return
		case payload := <-c.send:
			ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
			err := c.conn.Write(ctx, websocket.MessageText, payload)
			cancel()
			if err != nil {
				h.remove(c)
				_ = c.conn.CloseNow()
				logger.Debug("websocket write failed, connection removed: %v", err)
				return
			}
		}
	}
}

func (h *Hub) add(c *client) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[c] = struct{}{}
}

func (h *Hub) remove(c *client) {
	h.mu.Lock()
	_, exists := h.conns[c]
	delete(h.conns, c)
	h.mu.Unlock()
	if exists {
		c.closeDone()
	}
}
