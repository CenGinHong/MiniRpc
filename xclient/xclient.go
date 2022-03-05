package xclient

import (
	"MiniRpc"
	"context"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery
	mode    SelectMode
	opt     *MiniRpc.Option
	mu      sync.Mutex
	clients map[string]*MiniRpc.Client
}

func NewXClient(d Discovery, mode SelectMode, opt *MiniRpc.Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*MiniRpc.Client)}
}

func (x *XClient) Close() error {
	x.mu.Lock()
	defer x.mu.Unlock()
	for key, client := range x.clients {
		_ = client.Close()
		delete(x.clients, key)
	}
	return nil
}

func (x *XClient) dial(rpcAddr string) (*MiniRpc.Client, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	client, ok := x.clients[rpcAddr]
	// 检查可用状态
	if ok && !client.IsAvailable() {
		_ = client.Close()
		delete(x.clients, rpcAddr)
		client = nil
	}
	// 为空，创建一个新的客户端连接
	if client == nil {
		var err error
		client, err = MiniRpc.XDial(rpcAddr, x.opt)
		if err != nil {
			return nil, err
		}
		x.clients[rpcAddr] = client
	}
	return client, nil
}

func (x *XClient) call(ctx context.Context, rpcAddr string, serviceMethod string, args interface{}, reply interface{}) error {
	client, err := x.dial(rpcAddr)
	if err != nil {
		return err
	}
	return client.Call(ctx, serviceMethod, args, reply)
}

func (x *XClient) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 根据不同策略选择不同的服务端地址
	rpcAddr, err := x.d.Get(x.mode)
	if err != nil {
		return err
	}
	// 发起调用
	return x.call(ctx, rpcAddr, serviceMethod, args, reply)
}

// Broadcast 向所有客户端请求，使用fast-fail快速返回结果
func (x *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	// 获取所有服务端
	servers, err := x.d.GetAll()
	if err != nil {
		return err
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	var e error
	// 如果reply是空的，就是未完成
	replyDone := reply == nil
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	for _, rpcAddr := range servers {
		wg.Add(1)
		// 开启goroutine
		go func(rpcAddr string) {
			defer wg.Done()
			// 这里是个指针
			var clonedReply interface{}
			// 如果reply被赋值，反射构建一个副本
			if reply != nil {
				// reflect.New返回指针
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			err := x.call(ctx, rpcAddr, serviceMethod, args, clonedReply)
			mu.Lock()
			defer mu.Unlock()
			if err != nil && e == nil {
				e = err
				// 如果任意一个实例发生错误，则返回其中一个错误；如果调用成功，则返回其中一个的结果
				// 快速失败
				cancel()
				return
			}
			// 能返回结果而且reply仍然是空的话
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
		}(rpcAddr)
	}
	wg.Wait()
	return e
}
