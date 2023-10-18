package geerpc

import (
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"sync"
)

type Call struct {
	Seq           uint64
	ServiceMethod string
	Args          interface{}
	Reply         interface{}
	Error         error
	//支持异步调用，当调用结束时，会调用call.done()通知调用方
	Done chan *Call
}

func (call *Call) done() {
	call.Done <- call
}

type Client struct {
	cc      codec.Codec //消息编解码器
	opt     *Option
	sending sync.Mutex   //互斥锁。防止出现多个请求报文混淆。
	header  codec.Header //每个请求的消息头
	mu      sync.Mutex
	seq     uint64           //给发送的请求编号，每个请求拥有唯一编号
	pending map[uint64]*Call //存储未处理完的请求。键是请求，值是Call实例
	//closing 和 shutdown 任意一个值置为 true，则表示 Client 处于不可用的状态，但有些许的差别，
	//closing 是用户主动关闭的，即调用 Close 方法，
	//而 shutdown 置为 true 一般是有错误发生。
	closing  bool
	shutdown bool
}

var _ io.Closer = (*Client)(nil)

var ErrShutdown = errors.New("connection is shut down")

func (client *Client) Close() error {
	client.mu.Lock()
	defer client.mu.Unlock()
	if client.closing {
		return ErrShutdown
	}

	client.closing = true
	return client.cc.Close()
}

func (client *Client) IsAvaliable() bool {
	client.mu.Lock()
	defer client.mu.Unlock()
	return !client.shutdown && !client.closing
}

// 将参数call添加到client.pending中，并更新client.seq
func (client *Client) registerCall(call *Call) (uint64, error) {
	client.mu.Lock()
	defer client.mu.Unlock()

	if client.closing || client.shutdown {
		return 0, ErrShutdown
	}
	call.Seq = client.seq
	client.pending[call.Seq] = call
	client.seq++
	return call.Seq, nil
}

// 根据seq，从client.pending中移除对应的call，并返回
func (client *Client) removeCall(seq uint64) *Call {
	client.mu.Lock()
	defer client.mu.Unlock()
	call := client.pending[seq]
	delete(client.pending, seq)
	return call
}

// 服务端或客户端发生错误时调用，将shutdown设置为true，且将错误信息通知所有pending状态的call
func (client *Client) terminateCall(err error) {
	client.sending.Lock()
	defer client.sending.Unlock()
	client.mu.Lock()
	defer client.mu.Unlock()
	client.shutdown = true
	for _, call := range client.pending {
		call.Error = err
		call.done()
	}
}

func (client *Client) receive() {
	var err error
	for err == nil {
		var h codec.Header
		if err = client.cc.ReadHeader(&h); err != nil {
			break
		}
		call := client.removeCall(h.Seq)
		switch {
		case call == nil:
			err = client.cc.ReadBody(nil)
		case h.Error != "":
			call.Error = fmt.Errorf(h.Error)
			err = client.cc.ReadBody(nil)
			call.done()
		default:
			err = client.cc.ReadBody(call.Reply)
			if err != nil {
				call.Error = errors.New("reading body " + err.Error())
			}
			call.done()
		}
	}

	client.terminateCall(err)
}

func NewClient(conn net.Conn, opt *Option) (*Client, error) {

	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		err := fmt.Errorf("invalid codec type %s", opt.CodecType)
		log.Println("rpc client : codec error: ", err)
		return nil, err
	}
	if err := json.NewEncoder(conn).Encode(opt); err != nil {
		log.Println("rpc client : options err : ", err)
		_ = conn.Close()
		return nil, err
	}
	return NewClientCodec(f(conn), opt), nil

}

func NewClientCodec(cc codec.Codec, opt *Option) *Client {
	client := &Client{
		seq:     1,
		cc:      cc,
		opt:     opt,
		pending: make(map[uint64]*Call),
	}

	go client.receive()
	return client

}

//用于解析函数参数中的选项。可以接受以恶搞可变数量的*Option类型参数，即表示可以传入任意个数的option结构体指针类型参数。

func parseOptions(opts ...*Option) (*Option, error) {
	//判断传入的参数是否为空或第一个参数是否为nil
	if len(opts) == 0 || opts[0] == nil {
		return DefaultOption, nil
	}
	//判断传入的参数是否等于1
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

// Dial函数通过网络连接到指定地址的RPC服务器，并返回一个client结构体指针和一个错误值。
func Dial(network, address string, opts ...*Option) (client *Client, err error) {
	//先调用函数来解析传入的可变参数opts
	opt, err := parseOptions(opts...)
	if err != nil {
		return nil, err
	}
	conn, err := net.Dial(network, address)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err == nil {
			_ = conn.Close()
		}
	}()
	//调用NewClient函数创建一个Client结构体实例，并将连接对象和解析后的选项作为参数传入该函数进行初始化。
	return NewClient(conn, opt)
}

// 至此，GeeRPC客户端已经具备了完整的创建连接和接收响应的能力了，最后还需要实现发送请求的能力
func (client *Client) send(call *Call) {
	client.sending.Lock()
	defer client.sending.Unlock()

	seq, err := client.registerCall(call)
	if err != nil {
		call.Error = err
		call.done()
		return

	}

	client.header.ServiceMethod = call.ServiceMethod
	client.header.Seq = seq
	client.header.Error = ""

	if err := client.cc.Write(&client.header, call.Args); err != nil {
		call := client.removeCall(seq)
		if call != nil {
			call.Error = err
			call.done()
		}
	}
}

// 异步接口，返回call实例
func (client *Client) Go(serviceMethod string, args, reply interface{}, done chan *Call) *Call {
	if done == nil {
		done = make(chan *Call, 10)
	} else if cap(done) == 0 {
		log.Panic("rpc client : done channel is unbuffered")
	}
	call := &Call{
		ServiceMethod: serviceMethod,
		Args:          args,
		Reply:         reply,
		Done:          done,
	}
	client.send(call)
	return call
}

// Call是对go的封装，阻塞call.Done，等待响应返回，是一个同步接口
func (client *Client) Call(serviceMethod string, args, reply interface{}) error {
	call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error
}
