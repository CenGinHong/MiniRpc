package main

import (
	"context"
	"log"
	"net"
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
	// 注册结构体
	if err := Register(&foo); err != nil {
		log.Fatal("register error:", err)
	}
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		log.Fatalln("network error:", err)
	}
	log.Println("start rpc server on:", l.Addr())
	// 将开启的服务端地址返回
	addr <- l.Addr().String()
	Accept(l)
}

func main() {
	log.SetFlags(0)
	addr := make(chan string)
	// 起一个协程负责接受请求
	go startServer(addr)
	// 模拟客户端
	client, err := Dial("tcp", <-addr)
	if err != nil {
		log.Fatalln("rpc Client: client dial error: ", client)
		return
	}
	defer func() {
		_ = client.Close()
	}()
	time.Sleep(time.Second)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// 新建参数
			args := &Args{Num1: i, Num2: i * i}
			var reply int
			// 发送请求
			ctx, cancel := context.WithTimeout(context.Background(), time.Second*2)
			defer cancel()
			if err = client.Call(ctx, "Foo.Sum", args, &reply); err != nil {
				log.Fatal("call Foo.Sum error:", err)
			}
			log.Printf("reply: %d + %d = %d", args.Num1, args.Num2, reply)
		}(i)
	}
	wg.Wait()
}
