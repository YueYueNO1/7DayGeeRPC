package xclient

import (
	"context"
	. "geerpc"
	"io"
	"reflect"
	"sync"
)

type XClient struct {
	d       Discovery  //服务发现实例
	mode    SelectMode //负载均衡模式
	opt     *Option    //协议选项
	mu      sync.Mutex
	clients map[string]*Client //client实例列表
}

var _ io.Closer = (*Client)(nil)

func NewXClient(d Discovery, mode SelectMode, opt *Option) *XClient {
	return &XClient{d: d, mode: mode, opt: opt, clients: make(map[string]*Client)}

}

func (xc *XClient) Close() error {
	xc.mu.Lock()
	defer xc.mu.Unlock()
	for key, client := range xc.clients {
		_ = client.Close()
		delete(xc.clients, key)
	}
	return nil
}

// 用于调用远程RPC服务==>建立TCP连接
func (xc *XClient) dial(rpcAddr string) (*Client, error) {
	//使用锁，以确保clients字段的并发访问安全
	xc.mu.Lock()
	defer xc.mu.Unlock()
	client, ok := xc.clients[rpcAddr] //建立服务端与客户端之间的TCP连接
	if ok && !client.IsAvaliable() {
		//如果已经有与该地址对应的client连接存在，但它已不可用，则会关闭连接
		_ = client.Close()
		delete(xc.clients, rpcAddr)
		client = nil
	}
	//然后重新创建一个可用的client连接，并将其保存到xc.clients字典中，以便复用。
	if client == nil {
		var err error
		client, err = XDial(rpcAddr, xc.opt)
		if err != nil {
			return nil, err
		}
		xc.clients[rpcAddr] = client
	}
	return client, nil
}

// 用于发送RPC请求并接收响应。
func (xc *XClient) call(rpcAddr string, ctx context.Context, serviceMethod string, args, reply interface{}) error {
	//先调用dial方法获取rpcAddr对应的client连接
	client, err := xc.dial(rpcAddr)
	if err != nil {
		return err
	}
	//然后通过client.call方法发送请求并接收响应数据，如果发送失败则会返回错误
	return client.Call(ctx, serviceMethod, args, reply)

}

// 对外提供的调用入口，该方法以传入的serviceMethod参数为名调用远程RPC服务，
func (xc *XClient) Call(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	rpcAddr, err := xc.d.Get(xc.mode)
	if err != nil {
		return err
	}
	//将结果存储在传入的reply参数中，并返回错误状态码
	return xc.call(rpcAddr, ctx, serviceMethod, args, reply)

}

//整个流程中，d.Get(xc.mode)用于根据当前选择模式，从discovery服务中动态获取一个可用的RPC服务地址；
//call方法用于调用RPC服务；dial方法用于建立TCP连接。
//因此XClient结构体实现了动态发现RPC服务地址，负载均衡和故障转移等功能，可以大大的提高RPC服务的可用性和性能

// 一个常用功能：广播
// 即 通过将请求同时发送给多个RPC服务器来实现数据的广播传输
// 适用于在多个服务器上执行相同操作的场景，如群发消息、批量处理等
func (xc *XClient) Broadcast(ctx context.Context, serviceMethod string, args, reply interface{}) error {
	//先通过xc.d.GetAll获取所有可用的RPC服务地址
	servers, err := xc.d.GetAll()
	if err != nil {
		return err
	}

	var wg sync.WaitGroup //等待组
	var mu sync.Mutex     //互斥锁
	var e error           //用于记录出现的错误
	//判断reply是否为nil，如果是，则设置replyDone为true，表示不需要接收响应
	replyDone := reply == nil
	//创建一个新的上下文ctx，并通过cancle函数在发生错误时取消请求
	ctx, cancel := context.WithCancel(ctx)
	//遍历所有的RPC服务地址，对于每个地址启动一个goroutine进行调用
	for _, rpcAddr := range servers {
		wg.Add(1)
		go func(rpcAddr string) {
			//先创建一个克隆的reply对象用于接收响应。
			var clonedReply interface{}
			if reply != nil {
				//如果reply不为nil，则使用反射机制克隆一个与reply类型相同的对象
				clonedReply = reflect.New(reflect.ValueOf(reply).Elem().Type()).Interface()
			}
			//调用xc.call方法发生RPC请求，并将响应保存至克隆的reply对象中
			err := xc.call(rpcAddr, ctx, serviceMethod, args, clonedReply)
			//使用互斥锁mu保护错误判断和响应赋值的过程，避免并发访问冲突，
			mu.Lock()
			//如果发生错误且e为空，则将当前错误赋值给e，并调用cannel函数取消所有请求。
			if err != nil && e == nil {
				e = err
				cancel()
			}
			//如果没有发生错误且replyDone为false，则将克隆的reply对象的值设置给原始的reply
			if err == nil && !replyDone {
				reflect.ValueOf(reply).Elem().Set(reflect.ValueOf(clonedReply).Elem())
				replyDone = true
			}
			mu.Unlock()
		}(rpcAddr)
	}
	//等待所有goroutine完成，即等待所有的RPC调用结束
	wg.Wait()
	//返回错误e
	return e
}
