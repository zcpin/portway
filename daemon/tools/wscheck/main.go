// Command wscheck 是一个调试工具：连接 daemon 的 WebSocket，
// 打印收到的事件，用于验证事件推送链路是否正常。
//
// 用法：wscheck <ws-url> [token]
// 例：wscheck ws://127.0.0.1:51581/ws abc123...
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: wscheck <ws-url> [token]")
		os.Exit(1)
	}

	url := os.Args[1]
	token := ""
	if len(os.Args) > 2 {
		token = os.Args[2]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	opts := &websocket.DialOptions{}
	if token != "" {
		opts.HTTPHeader = http.Header{"X-Auth-Token": []string{token}}
	}

	c, resp, err := websocket.Dial(ctx, url, opts)
	if err != nil {
		if resp != nil {
			fmt.Fprintf(os.Stderr, "dial failed: %v (http %d)\n", err, resp.StatusCode)
		} else {
			fmt.Fprintf(os.Stderr, "dial failed: %v\n", err)
		}
		os.Exit(1)
	}
	defer c.Close(websocket.StatusNormalClosure, "")

	fmt.Println("connected, waiting for events (20s)...")
	for {
		_, data, err := c.Read(ctx)
		if err != nil {
			fmt.Println("connection closed:", err)
			return
		}
		fmt.Println("recv:", string(data))
	}
}
