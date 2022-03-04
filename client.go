package main

import (
	"MiniRpc/codec"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type Call struct {
	Seq           uint64
	ServiceMethod string      // 形如格式 <service>.<method>
	Args          interface{} // 传入被调用函数的参数
	Reply         interface{} // 函数的返回值
	Error         error       // 错误
	Done          chan *Call  // 当调用结束后的通知
}

func (c *Call) done() {
	// 调用结束，把自己塞进管道子通知调用方
	c.Done <- c
}

// Client 一个客户端可能发起多个call, 并且通过多个协程发起
type Client struct {
	cc       codec.Codec      // 编解码器，将消息进行编解码
	opt      *Option          // 报文头
	sending  sync.Mutex       // 互斥锁，保护请求有序发送
	header   codec.Header     // 请求的消息头
	mu       sync.Mutex       // 互斥锁，保护pending 和 shutdown的线程安全
	seq      uint64           // 发送的请求编号
	pending  map[uint64]*Call // 存储未处理完的请求，该map的值当请求失败或者收到恢复才可以移除
	closing  bool             // 标识client是否可用，这个用于用户主动关闭
	shutdown bool             // 标识client是否可用，这个用于有错误时关闭
}

func XDial(rpcAddr string, opts ...*Option) (*Client, error) {
	// 切分协议
	parts := strings.Split(rpcAddr, "@")
	// 错误格式
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err: wrong format '%s', expect protocol@addr", rpcAddr)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		{
			// 基于http请求
			return DialHTTP("tcp", addr, opts...)
		}
	default:
		{
			// 其他网络协议
			return Dial(protocol, addr, opts...)
		}
	}
}

func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closing {
		return ErrShutdown
	}
	c.closing = true
	return c.cc.Close()
}

// IsAvailable 这个客户端是否还在工作
func (c *Client) IsAvailable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.shutdown && !c.closing
}

// 检查Client是否实现了Closer接口
var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shutdown")

// registerCall 注册一个请求，返回编号seq
func (c *Client) registerCall(call *Call) (uint64, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	// 信道已经被关闭,不能发送请求
	if c.closing || c.shutdown {
		return 0, ErrShutdown
	}
	// 赋值编号
	call.Seq = c.seq
	// 置入map
	c.pending[c.seq] = call
	c.seq++
	return call.Seq, nil
}

// removeCall 移除一个请求
func (c *Client) removeCall(seq uint64) *Call {
	c.mu.Lock()
	defer c.mu.Unlock()
	call := c.pending[seq]
	delete(c.pending, seq)
	return call
}

// terminateCalls  发生错误，打断所有待发送的请求，将所有待发送的清空
func (c *Client) terminateCalls(err error) {
	// TODO 这里应该可以不用加sending锁？
	//c.sending.Lock()
	//defer c.sending.Unlock()
	c.mu.Lock()
	defer c.mu.Lock()
	c.shutdown = true
	for _, call := range c.pending {
		call.Error = err
		call.done()
	}
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, option *Option) (client *Client, err error)

// dialTimeout 带超时连接并创建客户端，客户端是使用创建的conn来创建的
func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	// 解析opt
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	// 连接，带超时时间，如果超时将返回错误
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	ch := make(chan clientResult, 1)
	// 使用协程获取执行newClient
	go func() {
		// TODO 在f里加超时逻辑是为什么
		client, err := f(conn, opt)
		// 使用chan返回结果
		ch <- clientResult{client: client, err: err}
	}()
	// 如果没有超时时间，就一直在这里阻塞等待
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	// 如果有超时时间，就看那个chan消息比较快到
	select {
	case <-time.After(opt.ConnectTimeout):
		{
			return nil, fmt.Errorf("rpc Client: connect timeout: expect within %s", opt.ConnectTimeout)
		}
	case result := <-ch:
		{
			return result.client, result.err
		}
	}
}

func (c *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		// 读取头部解析到h
		if err = c.cc.ReadHeader(&h); err != nil {
			// 如果conn被关闭就从这里break
			break
		}
		// 该call已经收到结果，从pending中移除这个call
		call := c.removeCall(h.Seq)
		switch {
		// 如果被移除的call不存在，可能在send方法write发送请求出错就被移除了，
		case call == nil:
			{
				// 该结果无意义，不用理会消息体
				err = c.cc.ReadBody(nil)
			}
		case h.Error != "":
			{
				// 服务端处理过程中发生了错误
				call.Error = fmt.Errorf(h.Error)
				// 不理会消息体
				err = c.cc.ReadBody(nil)
				// 完成请求
				call.done()
			}
		default:
			{
				// 将请求结果读出
				err = c.cc.ReadBody(call.Reply)
				if err != nil {
					call.Error = errors.New("reading body " + err.Error())
				}
				// 完成请求
				call.done()
			}
		}
	}
	// 发生错误，终止pending中的请求
	c.terminateCalls(err)
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {
	// 获取编解码器
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc Client: codec error: ", err)
		return nil, err
	}
	// 将opt先写入
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client: options error: ", err)
		_ = conn.Close()
		return nil, err
	}
	return newClientCodec(f(conn), opt), nil
}

func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	// 写入信道
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	// 使用CONNECT握手后连接成功创建客户端
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	// 连接失败
	if err == nil {
		err = errors.New("unexpected HTTP response " + resp.Status)
	}
	return nil, err
}

func newClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}
	// 起协程接受服务
	go client.receive()
	return client
}

// 初始化options
func parseOptions(opts ...*Option) (*Option, error) {
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	// opts在一次请求中只能有一个
	if len(opts) != 1 {
		return nil, errors.New("number of options is more than 1")
	}
	opt := opts[0]
	opt.MagicNumber = DefaultOption.MagicNumber
	if opt.CodecType == "" {
		opt.CodecType = DefaultOption.CodecType
	}
	return opt, nil
}

func Dial(network string, address string, opts ...*Option) (client *Client, err error) {
	return dialTimeout(NewClient, network, address, opts...)
}

// DialHTTP 连接到一个有具体地址的HTTP RPC 服务器
func DialHTTP(network string, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

func (c *Client) send(call *Call) {
	// 将该请求登记，写入pending
	seq, err := c.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return
	}
	c.sending.Lock()
	defer c.sending.Unlock()
	c.header.ServiceMethod = call.ServiceMethod
	c.header.Seq = seq
	c.header.Error = ""
	// 不能把sending锁改到这里，因为锁是header是公用的，这样可能会导致不同请求使用同一个header
	// 然后接受返回值的时候在map找不到正确的内容回填
	// 发送请求时发生错误
	if err = c.cc.Write(&c.header, call.Args); err != nil {
		// 发生错误，将call移除
		removeCall := c.removeCall(seq)
		// call可能为空，这代表write部分失败
		// client已经接受结果并处理
		if removeCall != nil {
			removeCall.Error = err
			removeCall.done()
		}
	}
}

func (c *Client) Go(serviceMethod string, args interface{}, reply interface{}, done chan *Call) *Call {
	if cap(done) == 0 {
		log.Panic("rpc Client: done channel is unbuffered")
	}
	// 构建请求
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	c.send(call)
	return call
}

// Call 对Go 封装，阻塞响应返回，同步接口
func (c *Client) Call(ctx context.Context, serviceMethod string, args interface{}, reply interface{}) error {
	// 使用context引入超时控制,这里的时间包括从发送请求到接受回来收到结果
	call := c.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		{
			// 超时，移除请求
			c.removeCall(call.Seq)
			return errors.New("rpc client: call failed: " + ctx.Err().Error())
		}
	case call := <-call.Done:
		{
			return call.Error
		}
	}
}
