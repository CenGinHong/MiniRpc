package main

import (
	"MiniRpc"
	"MiniRpc/xclient"
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

func (f Foo) Sleep(args Args, reply *int) error {
	// 增加延时，某些调用会失败
	time.Sleep(time.Second * time.Duration(args.Num1))
	*reply = args.Num1 + args.Num2
	return nil
}

func startServer(addrCh chan string) {
	var foo Foo
	// 监听这个tcp端口
	l, _ := net.Listen("tcp", ":0")
	s := MiniRpc.NewServer()
	_ = s.Register(&foo)
	addrCh <- l.Addr().String()
	s.Accept(l)
}

// foo 封装一层便于观察结果
func foo(ctx context.Context, x *xclient.XClient, typ string, serviceMethod string, args *Args) {
	var reply int
	var err error
	switch typ {
	case "call":
		{
			err = x.Call(ctx, serviceMethod, args, &reply)
		}
	case "broadcast":
		{
			err = x.Broadcast(ctx, serviceMethod, args, &reply)
		}
	}
	if err != nil {
		log.Printf("%s %s error: %v", typ, serviceMethod, err)
	} else {
		log.Printf("%s %s success: %d + %d = %d", typ, serviceMethod, args.Num1, args.Num2, reply)
	}
}

func call(addr1, addr2 string) {
	// 将服务器端的地址写入服务发现接口
	d := xclient.NewMultiServerDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	// 新建客户端，该客户端使用随机的方式进行服务端选择
	x := xclient.NewXClient(d, xclient.RanDomSelect, nil)
	defer func(x *xclient.XClient) {
		_ = x.Close()
	}(x)
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(context.Background(), x, "call", "Foo.Sum", &Args{Num1: 1, Num2: i * i})
		}(i)
	}
	wg.Wait()
}

func broadcast(addr1, addr2 string) {
	d := xclient.NewMultiServerDiscovery([]string{"tcp@" + addr1, "tcp@" + addr2})
	x := xclient.NewXClient(d, xclient.RanDomSelect, nil)
	defer func() { _ = x.Close() }()
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			foo(context.Background(), x, "broadcast", "Foo.Sum", &Args{Num1: i, Num2: i * i})
			ctx, _ := context.WithTimeout(context.Background(), time.Second*2)
			foo(ctx, x, "broadcast", "Foo.Sleep", &Args{Num1: i, Num2: i * i})
		}(i)
	}
	wg.Wait()
}

func main() {
	log.SetFlags(0)
	// 启动两个服务端
	ch1 := make(chan string)
	ch2 := make(chan string)
	go startServer(ch1)
	go startServer(ch2)
	add1 := <-ch1
	add2 := <-ch2
	time.Sleep(time.Second)
	call(add1, add2)
	broadcast(add1, add2)
}
