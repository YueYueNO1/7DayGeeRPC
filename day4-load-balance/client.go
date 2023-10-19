package geerpc

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"geerpc/codec"
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
	/*//先调用函数来解析传入的可变参数opts
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
	return NewClient(conn, opt)*/
	return dialTimeout(NewClient, network, address, opts...)
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
func (client *Client) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	/*call := <-client.Go(serviceMethod, args, reply, make(chan *Call, 1)).Done
	return call.Error*/
	//day3-timeout：新增call超时处理机制
	call := client.Go(serviceMethod, args, reply, make(chan *Call, 1))
	select {
	case <-ctx.Done():
		client.removeCall(call.Seq)
		return errors.New("rpc client: call failed: " + ctx.Err().Error())
	case call := <-call.Done:
		return call.Error
	}
}

type clientResult struct {
	client *Client
	err    error
}

type newClientFunc func(conn net.Conn, opt *Option) (client *Client, err error)

// 该函数的作用是：实现了一个带有连接超时功能的网络客户端建立过程
func dialTimeout(f newClientFunc, network, address string, opts ...*Option) (client *Client, err error) {
	opt, err := parseOptions(opts...)
	//解析opt可能出现的错误
	if err != nil {
		return nil, err
	}
	//利用DialTimeout函数建立一个与指定网络和地址连接的连接对象conn，并设置连接的超时时间为opt.ConnectTimeout
	//如果连接过程中出现错误，则直接返回错误
	conn, err := net.DialTimeout(network, address, opt.ConnectTimeout)
	if err != nil {
		return nil, err
	}
	//保证连接是关闭的
	defer func() {
		if err != nil {
			_ = conn.Close()
		}
	}()
	//创建了一个用于接受结果的通道ch，并在单独的一个goroutine中执行具体的客户端逻辑。
	ch := make(chan clientResult)
	go func() {
		//该goroutine调用f函数创建了一个客户端，并将结果发送到通道ch中
		client, err := f(conn, opt)
		ch <- clientResult{
			client: client,
			err:    err,
		}
	}()
	//==0 即说明没有设置超时时间，将直接从通道ch中读取结果并将其返回
	if opt.ConnectTimeout == 0 {
		result := <-ch
		return result.client, result.err
	}
	//否则，将根据设置的连接超时时间opt.ConnectTimeout进行不同的处理结果
	//time.After 函数用于创建一个通道，该通道在指定的时间间隔后接受一个时间值
	//与select一起用，以实现计时器的功能
	select {
	//从计时器返回的超时时间或从通道ch中读取结果
	//如果超时时间到达，则会返回一个连接超时的错误
	case <-time.After(opt.ConnectTimeout):
		return nil, fmt.Errorf("rpc client: connect timeout: expect within %s", opt.ConnectTimeout)
		//否则，会返回从通道ch中接收到的结果
	case result := <-ch:
		return result.client, result.err
	}
}

func NewHTTPClient(conn net.Conn, opt *Option) (*Client, error) {
	_, _ = io.WriteString(conn, fmt.Sprintf("CONNECT %s HTTP/1.0\n\n", defaultRPCPath))

	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: "CONNECT"})
	if err == nil && resp.Status == connected {
		return NewClient(conn, opt)
	}
	if err == nil {
		err = errors.New("unexpected HTTP response: " + resp.Status)
	}
	return nil, err
}

func DialHTTP(network, address string, opts ...*Option) (*Client, error) {
	return dialTimeout(NewHTTPClient, network, address, opts...)
}

func XDial(rpcAddress string, opts ...*Option) (*Client, error) {
	parts := strings.Split(rpcAddress, "@")
	if len(parts) != 2 {
		return nil, fmt.Errorf("rpc client err : wrong format'%s',expect proptocol@addr", rpcAddress)
	}
	protocol, addr := parts[0], parts[1]
	switch protocol {
	case "http":
		return DialHTTP("tcp", rpcAddress, opts...)
	default:
		return Dial(protocol, addr, opts...)
	}
}
