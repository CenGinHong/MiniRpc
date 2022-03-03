package codec

import "io"

type Header struct {
	ServiceMethod string // 服务端方法，格式"service.method"
	Seq           uint64 // 请求序号
	Error         string // 错误信息
}

type Codec interface {
	io.Closer
	ReadHeader(header *Header) error  // header是用来放读取出来后返回的对象
	ReadBody(body interface{}) error  // 使用指针返回的方式，body是用来放去读出来后返回的对象
	Write(*Header, interface{}) error // 写入信道
}

type NewCodecFunc func(closer io.ReadWriteCloser) Codec

type Type string

const (
	GobType Type = "application/gob"
	Json    Type = "application/json"
)

// NewCodecFuncMap 存储不同格式对应的构造函数
var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
