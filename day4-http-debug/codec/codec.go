package codec

import (
	"io"
)

type Header struct {
	ServiceMethod string // 服务名和服务名
	Seq           uint64 // 请求的序号，可以认为是某种请求的id，用于区分不同请求
	Error         string //错误信息
}

// 抽象出对消息体进行编解码的接口，抽象出接口是为了实现不同的codec实例
type Codec interface {
	io.Closer
	ReadHeader(*Header) error
	ReadBody(interface{}) error
	Write(*Header, interface{}) error
}

// 抽象codec的构造函数。通过codec的tpye得到构造函数从而创建codec实例。
// 与工厂模式相似，但是不同的是返回的是构造函数，并非实例
type NewCodecFunc func(io.ReadWriteCloser) Codec

type Type string

// 我们定义了两种codec，gob和json，但实际上只实现了gob一种。实际上两者的实现非常接近。
const (
	GobType  Type = "application/gob"
	JsonType Type = "application/json" // not implemented
)

var NewCodecFuncMap map[Type]NewCodecFunc

func init() {
	NewCodecFuncMap = make(map[Type]NewCodecFunc)
	NewCodecFuncMap[GobType] = NewGobCodec
}
