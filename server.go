package main

import (
	"MiniRpc/codec"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"
)

func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

const MagicNumber = 0x3bef5c

type Option struct {
	MagicNumber    int           // 标记这是一个MyRpc的请求
	CodecType      codec.Type    // 选择什么方式去解码
	ConnectTimeout time.Duration // 连接限制最长时间，0代表没有限制
	HandleTimeout  time.Duration // 服务端处理限制最长时间
}

// DefaultOption 默认请求option
var DefaultOption = &Option{
	MagicNumber:    MagicNumber,
	CodecType:      codec.GobType,
	ConnectTimeout: time.Second * 10,
}

// 在一次请求中报文用这样的方式发送
//| Option{MagicNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |
//| <------      固定 JSON 编码      ------>  | <-------   编码方式由 CodeType 决定   ------->|

type Server struct {
	serviceMap sync.Map // 安全map
}

func (s *Server) Register(rcvr interface{}) error {
	service := newService(rcvr)
	if _, dup := s.serviceMap.LoadOrStore(service.name, service); dup {
		return errors.New("rpc Server: service already defined: " + service.name)
	}
	return nil
}

func (s *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	// 服务名称需要满足<service>.<method>
	dot := strings.LastIndex(serviceMethod, ".")
	// 格式错误
	if dot < 0 {
		err = errors.New("rpc Server: service/method request ill-formed: " + serviceMethod)
		return
	}
	serviceName := serviceMethod[:dot]
	methodName := serviceMethod[dot+1:]
	// 找对应服务方法
	svci, ok := s.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc Server: can not find service " + serviceName)
		return
	}
	// 转服务实例
	svc = svci.(*service)
	// 找该服务的方法
	mtype = svc.method[methodName]
	// 方法不存在，报错
	if mtype == nil {
		err = errors.New("rpc server: can not find method " + methodName)
	}
	return
}

func Register(rcvr interface{}) error { return DefaultServer.Register(rcvr) }

func NewServer() *Server {
	return &Server{}
}

var DefaultServer = NewServer()

// Accept 接受一个连接并处理
func (s *Server) Accept(lis net.Listener) {
	for {
		// 循环等待接受socket建立连接
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc Server: accept error:", err)
			return
		}
		// 启动协程处理一个客户端的请求
		go s.ServeConn(conn)
	}
}

func (s *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() {
		// 处理完成，关闭conn
		if err := conn.Close(); err != nil {
			log.Println("rpc Server: conn close error:", err)
			return
		}
	}()
	var opt Option
	// 解码Option
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc Server: options error: ", err)
		return
	}
	// 无效magicNumber
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc Server: invalid magic number %x", opt.MagicNumber)
		return
	}
	// 获取对应构造函数
	f, ok := codec.NewCodecFuncMap[opt.CodecType]
	if !ok {
		log.Printf("rpc Server: invalid codec type %s", opt.CodecType)
		return
	}
	// 套上不同信息格式的编码器，然后处理一个客户端的请求
	s.serveCodec(f(conn), opt.HandleTimeout)
}

var invalidRequest = struct{}{}

// | Option{MagicNumber: xxx, CodecType: xxx} | Header{ServiceMethod ...} | Body interface{} |
// | <------      固定 JSON 编码      ------>  | <-------   编码方式由 CodeType 决定   ------->|
// | Option | Header1 | Body1 | Header2 | Body2 | ... Header 和 Body 可以有多个
func (s *Server) serveCodec(cc codec.Codec, handleTimeout time.Duration) {
	// 同一请求返回过程中需要保证线程安全
	sending := new(sync.Mutex)
	wg := new(sync.WaitGroup)
	for {
		// header和body可能有很多个，所以要全部处理完
		req, err := s.readRequest(cc)
		if err != nil {
			// 表示读取完
			if req == nil {
				break
			}
			req.header.Error = err.Error()
			// 将空结构返回，表示结束，对应上面的
			s.sendResponse(cc, req.header, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		// 开一个协程处理请求
		go s.handleRequest(cc, req, sending, wg, handleTimeout)
	}
	wg.Wait()
}

type request struct {
	header *codec.Header // 请求头
	argv   reflect.Value // 请求参数
	replyv reflect.Value // 请求回参
	mtype  *methodType   // 方法
	svc    *service      // 服务
}

func (s *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	// 请求头
	var h codec.Header
	// 读请求头
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

func (s *Server) readRequest(cc codec.Codec) (*request, error) {
	// 读出来请求头
	h, err := s.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{header: h}
	// 找到服务
	req.svc, req.mtype, err = s.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	// 根据这个方法的参数类型，新建一个实参
	req.argv = req.mtype.newArgv()
	req.replyv = req.mtype.newReplyv()
	// 得到指针,因为ReadBody需要传入指针
	argvi := req.argv.Interface()
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}
	// 将参数读取并写入arg
	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rcp server: read body err: ", err)
		return req, err
	}
	return req, nil
}

func (s *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	// 上锁,保护该信道只被当前协程所写
	sending.Lock()
	defer sending.Unlock()
	// 把头部和内容写入信道
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

// handleRequest 服务端处理request
func (s *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	// 这里的header是不共享的，及对应每一个req都有一个header,不用担心会出现client的线程安全问题
	defer wg.Done()
	called := make(chan struct{})
	sent := make(chan struct{})
	// 开启协程执行调用
	go func() {
		// 调用
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		// 处理完成
		called <- struct{}{}
		if err != nil {
			req.header.Error = err.Error()
			s.sendResponse(cc, req.header, invalidRequest, sending)
			// 发送完成
			sent <- struct{}{}
			return
		}
		s.sendResponse(cc, req.header, req.replyv.Interface(), sending)
		// 发送完成
		sent <- struct{}{}
		return
	}()
	// 没有超时时间，直接返回
	if timeout == 0 {
		<-called
		<-sent
		return
	}
	// 有超时时间
	select {
	case <-time.After(timeout):
		{
			// 直接返回错误
			req.header.Error = fmt.Sprintf("rpc Server: request handle timeout: expect within %s", timeout)
			s.sendResponse(cc, req.header, invalidRequest, sending)
		}
	case <-called:
		{
			// 等待发送完成
			<-sent
		}

	}
}
