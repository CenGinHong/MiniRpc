package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func _assert(condition bool, msg string, v ...interface{}) {
	if !condition {
		panic(fmt.Sprintf("assertion failed: "+msg, v...))
	}
}

func TestClient_dialTimeOut(t *testing.T) {
	// 并行化
	t.Parallel()
	l, _ := net.Listen("tcp", ":0")
	// 创建客户端阻塞两秒,模拟连接失败
	f := func(conn net.Conn, opt *Option) (client *Client, err error) {
		_ = conn.Close()
		time.Sleep(time.Second * 2)
		return nil, nil
	}
	//
	t.Run("timeout", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &Option{ConnectTimeout: time.Second})
		// 正常应该报错
		_assert(err != nil && strings.Contains(err.Error(), "connect timeout"), "expect a timeout error")
	})
	t.Run("0", func(t *testing.T) {
		_, err := dialTimeout(f, "tcp", l.Addr().String(), &Option{ConnectTimeout: 0})
		_assert(err == nil, "0 means no limit")
	})
}

type Bar int

func (b Bar) Timeout(_ int, _ *int) error {
	// 该方法两秒会过时
	time.Sleep(time.Second * 2)
	return nil
}

func startServer1(addr chan string) {
	var b Bar
	_ = Register(&b)
	// 选择一个空闲的端口
	l, _ := net.Listen("tcp", ":0")
	addr <- l.Addr().String()
	Accept(l)
}

func TestClient_Call(t *testing.T) {
	t.Parallel()
	addrCh := make(chan string)
	go startServer1(addrCh)
	addr := <-addrCh
	time.Sleep(time.Second)
	for i := 0; i < 5; i++ {
		t.Run("client timeout", func(t *testing.T) {
			client, _ := Dial("tcp", addr)
			// 客户端要求全过程必须控制在1秒，包括从发送到接收到结果
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var reply int
			err := client.Call(ctx, "Bar.Timeout", 1, &reply)
			_assert(err != nil && strings.Contains(err.Error(), ctx.Err().Error()), "expect a timeout error")
		})

		t.Run("server handle timeout", func(t *testing.T) {
			// 客户端要求服务端处理时间必须在1秒
			client, _ := Dial("tcp", addr, &Option{
				HandleTimeout: time.Second,
			})
			var reply int
			err := client.Call(context.Background(), "Bar.Timeout", 1, &reply)
			_assert(err != nil && strings.Contains(err.Error(), "handle timeout"), "expect time error")
		})
	}
}

func TestXDial(t *testing.T) {
	if runtime.GOOS == "linux" {
		ch := make(chan struct{})
		addr := "/tmp/minirpc.sock"
		go func() {
			_ = os.Remove(addr)
			l, err := net.Listen("unix", addr)
			if err != nil {
				t.Fatal("failed to listen unix socket")
			}
			ch <- struct{}{}
			Accept(l)
		}()
		_, err := XDial("unix@" + addr)
		_assert(err == nil, "failed to connect unix socket")
	}
}
