package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"sync"
	"time"
)

type Foo int

type Args struct {
	Num1 int
	Num2 int
}

func (f Foo) Sum(args Args, reply *int) error {
	*reply = args.Num1 + args.Num2
	return nil
}

func startServer(addr chan string) {
	var foo Foo
	// 监听这个tcp端口
	l, _ := net.Listen("tcp", ":9999")
	// 注册结构体成为被调用服务
	_ = Register(&foo)
	// 监听不同地址提供服务服务
	HandleHTTP()
	// 将监听的地址写入chan
	addr <- l.Addr().String()
	_ = http.Serve(l, nil)
}

func call(addr chan string) {

	c, _ := DialHTTP("tcp", <-addr)
	defer func(c *Client) {
		_ = c.Close()
	}(c)
	time.Sleep(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			if err := c.Call(context.Background(), "Foo.Sum", args, &reply); err != nil {
				return
			}
			log.Printf("%d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}

func main() {
	log.SetFlags(0)
	// 地址
	ch := make(chan string)
	// 客户端逻辑
	go call(ch)
	// 服务端逻辑
	startServer(ch)
}
