package MiniRpc

import (
	"go/ast"
	"log"
	"reflect"
	"sync/atomic"
)

type methodType struct {
	method    reflect.Method // 方法本身
	ArgType   reflect.Type   // 第一个参数的类型
	ReplyType reflect.Type   // 第二个参数的类型
	numCalls  uint64         // 统计方法调用次数
}

func (t *methodType) NumCalls() uint64 {
	return atomic.LoadUint64(&t.numCalls)
}

// Go 程序中的类型（Type）指的是系统原生数据类型，如 int、string、bool、float32 等类型，以及使用 type 关键字定义的类型，
// 这些类型的名称就是其类型本身的名称。例如使用 type A struct{} 定义结构体时，A 就是 struct{} 的类型。

// newArgv 创建对应类型的实例
func (t *methodType) newArgv() reflect.Value {
	var argv reflect.Value
	// 区分指针类型和值类型的创建
	// Kind()输出具体类型，例如struct,pointer,Type输出实际类型，例如methodType
	// 如果是一个指针
	if t.ArgType.Kind() == reflect.Ptr {
		// Elem() 是取指针类型的元素类型，因为这里是指针，即通过指针指向的实际类型
		// reflect.New 和new一样，返回给定类型的指针，因为arg是指针，要获取其具体类型
		argv = reflect.New(t.ArgType.Elem())
	} else {
		// New得到指针，再通过elem获得实例
		argv = reflect.New(t.ArgType).Elem()
	}
	return argv
}

// newReplyv 创建对应类型的返回值
func (t methodType) newReplyv() reflect.Value {
	// replyv本身一定是一个指针类型
	replyv := reflect.New(t.ReplyType.Elem())
	switch t.ReplyType.Elem().Kind() {
	case reflect.Map:
		{
			replyv.Elem().Set(reflect.MakeMap(t.ReplyType.Elem()))
		}
	case reflect.Slice:
		{
			replyv.Elem().Set(reflect.MakeSlice(t.ReplyType.Elem(), 0, 0))
		}
	}
	return replyv
}

type service struct {
	name   string                 // 映射的结构体名称，例如WaitGroup
	typ    reflect.Type           // 结构体的类型
	rcvr   reflect.Value          // 结构体的实例本身，调用时需要rcvr作为第0个参数
	method map[string]*methodType // 存放结构体所有符合条件的方法
}

// newService 将结构体下的函数构建服务
func newService(rcvr interface{}) *service {
	s := new(service)
	s.rcvr = reflect.ValueOf(rcvr)
	// rcvr可能是指针，通过Indirect可以返回它指向的对象的类型。不然的话，它的type就是reflect.Ptr
	s.name = reflect.Indirect(s.rcvr).Type().Name()
	// 获取Type
	s.typ = reflect.TypeOf(rcvr)
	// 该结构体是否导出（首字母是不是大写）
	if !ast.IsExported(s.name) {
		log.Fatalf("rpc Server: %s is not a valid service name\n", s.name)
	}
	s.registerMethods()
	return s
}

// registerMethods 过滤符合条件的方法，需要满足（args,*reply) error才能被注册
func (s *service) registerMethods() {
	s.method = make(map[string]*methodType)
	for i := 0; i < s.typ.NumMethod(); i++ {
		method := s.typ.Method(i)
		mType := method.Type
		// 必须要有三个入参(反射中，第一个参数都是self)，一个返回值
		if mType.NumIn() != 3 || mType.NumOut() != 1 {
			continue
		}
		// 最后一个参数需要为error
		if mType.Out(0) != reflect.TypeOf((*error)(nil)).Elem() {
			continue
		}
		// 得到类型
		argType, replyType := mType.In(1), mType.In(2)
		// 入参和结果是不是可导出的
		if !isExportedOrBuiltinType(argType) || !isExportedOrBuiltinType(replyType) {
			continue
		}
		// 构建服务函数
		s.method[method.Name] = &methodType{
			method:    method,
			ArgType:   argType,
			ReplyType: replyType,
		}
		log.Printf("rpc Server: register %s.%s\n", s.name, method.Name)
	}
}

// isExportedOrBuiltinType 是不是导出类型或者内置类型
func isExportedOrBuiltinType(t reflect.Type) bool {
	return ast.IsExported(t.Name()) || t.PkgPath() == ""
}

// call 通过反射值调用方法
func (s service) call(m *methodType, argv, replyv reflect.Value) error {
	// 记录被被调用次数，原子增加
	atomic.AddUint64(&m.numCalls, 1)
	// 获得函数
	f := m.method.Func
	// 调用该函数,第一个参数需要是该结构体的实例本身
	returnValues := f.Call([]reflect.Value{s.rcvr, argv, replyv})
	// 必返回一个error
	if errInter := returnValues[0].Interface(); errInter != nil {
		return errInter.(error)
	}
	return nil
}
