package geerpc

import (
	"errors"
	"fmt"
	"geerpc/codec"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"time"
)

import (
	"encoding/json"
)

const MagicNumber = 0x3bef5c

// 目前需要协商的唯一一项内容是消息的编解码方式，我们将这部分放到结构体Option中来承载
type Option struct {
	MagicNumber int        // MagicNumber marks this's a geerpc request
	CodecType   codec.Type // client may choose different Codec to encode body
	//day3：新增连接超时，将超时设定放在Option
	ConnectTimeout time.Duration
	HandleTimeout  time.Duration
}

// 一般来说，涉及协议协商的这部分信息需要设计固定的字节来传输。但是为了实现更加见到，GeeRPC客户端固定采用JSON编码Option，后续的header和body的编码方式由Option中的CodeType指定。
// 服务器首先使用JSON解码Option，然后通过Option的CodeType解码剩余的内容。
var DefaultOption = &Option{
	MagicNumber: MagicNumber,
	CodecType:   codec.GobType,
	//day3-timeout新增：ConnectTimeout默认值为10s
	ConnectTimeout: time.Second * 10,
}

// Server represents an RPC Server.
// 定义了结构体Server
type Server struct {
	//新增serviceMap
	serviceMap sync.Map
}

// NewServer returns a new Server.
func NewServer() *Server {
	return &Server{}
}

// DefaultServer is the default instance of *Server.
// 是一个默认的实例，主要是为了用户方便
var DefaultServer = NewServer()

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
// 实现了Accept的方式，net.Listen作为参数，for循环等待socket建立连接，并开启子协程处理，处理过程交给了serverConn
func (server *Server) Accept(lis net.Listener) {
	for {
		conn, err := lis.Accept()
		if err != nil {
			log.Println("rpc server: accept error:", err)
			return
		}
		go server.ServeConn(conn)
	}
}

// Accept accepts connections on the listener and serves requests
// for each incoming connection.
func Accept(lis net.Listener) { DefaultServer.Accept(lis) }

// ServeConn runs the server on a single connection.
// ServeConn blocks, serving the connection until the client hangs up.
func (server *Server) ServeConn(conn io.ReadWriteCloser) {
	defer func() { _ = conn.Close() }()
	var opt Option
	//通过json.newdecoder反序列化得到Opton实例，检查MagicNumber和CodeType的值是否正确，然后根据CodeType得到对应的消息编解码器，接下来的处理交给serverCodec
	if err := json.NewDecoder(conn).Decode(&opt); err != nil {
		log.Println("rpc server: options error: ", err)
		return
	}
	if opt.MagicNumber != MagicNumber {
		log.Printf("rpc server: invalid magic number %x", opt.MagicNumber)
		return
	}
	f := codec.NewCodecFuncMap[opt.CodecType]
	if f == nil {
		log.Printf("rpc server: invalid codec type %s", opt.CodecType)
		return
	}
	server.serveCodec(f(conn), &opt)
}

// invalidRequest is a placeholder for response argv when error occurs
var invalidRequest = struct{}{}

func (server *Server) serveCodec(cc codec.Codec, opt *Option) {
	sending := new(sync.Mutex) // make sure to send a complete response
	wg := new(sync.WaitGroup)  // wait until all request are handled
	for {
		req, err := server.readRequest(cc)
		if err != nil {
			if req == nil {
				break // it's not possible to recover, so close the connection
			}
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			continue
		}
		wg.Add(1)
		go server.handleRequest(cc, req, sending, wg, opt.HandleTimeout)
	}
	wg.Wait()
	_ = cc.Close()
}

// request stores all information of a call
type request struct {
	h            *codec.Header // header of request
	argv, replyv reflect.Value // argv and replyv of request
	mtype        *methodType
	svc          *service
}

func (server *Server) readRequestHeader(cc codec.Codec) (*codec.Header, error) {
	var h codec.Header
	if err := cc.ReadHeader(&h); err != nil {
		if err != io.EOF && err != io.ErrUnexpectedEOF {
			log.Println("rpc server: read header error:", err)
		}
		return nil, err
	}
	return &h, nil
}

// 读取请求
func (server *Server) readRequest(cc codec.Codec) (*request, error) {
	h, err := server.readRequestHeader(cc)
	if err != nil {
		return nil, err
	}
	req := &request{h: h}
	// TODO: now we don't know the type of request argv
	//day2 ,通过newArgv和newReplyv两个方法创建出两个入参实例，如何通过cc.ReadBody()将请求保温反序列化为第一个入参argv
	//同时我们需要注意传入的argv可能是值类型，也有可能是指针类型，所以处理方法有点差异
	req.svc, req.mtype, err = server.findService(h.ServiceMethod)
	if err != nil {
		return req, err
	}
	// day 1, just suppose its string
	req.argv = reflect.New(reflect.TypeOf(""))
	//新增replyv
	req.replyv = req.mtype.newReplyv()
	argvi := req.argv.Interface()
	//确保argvi是指针类型，readBody函数需要一个指针类型传参
	if req.argv.Type().Kind() != reflect.Ptr {
		argvi = req.argv.Addr().Interface()
	}

	if err = cc.ReadBody(argvi); err != nil {
		log.Println("rpc server: read argv err:", err)
		return req, err
	}
	return req, nil
}

// 发送请求
func (server *Server) sendResponse(cc codec.Codec, h *codec.Header, body interface{}, sending *sync.Mutex) {
	sending.Lock()
	defer sending.Unlock()
	//通过cc.Write来将指定的数据进行序列化并发送到对端。
	if err := cc.Write(h, body); err != nil {
		log.Println("rpc server: write response error:", err)
	}
}

// 回复请求
func (server *Server) handleRequest(cc codec.Codec, req *request, sending *sync.Mutex, wg *sync.WaitGroup, timeout time.Duration) {
	// TODO, should call registered rpc methods to get the right replyv
	// day 1, just print argv and send a hello message
	defer wg.Done()
	//day3-timeout：新增服务端处理超时
	called := make(chan struct{})
	sent := make(chan struct{})
	go func() {
		err := req.svc.call(req.mtype, req.argv, req.replyv)
		called <- struct{}{}
		if err != nil {
			req.h.Error = err.Error()
			server.sendResponse(cc, req.h, invalidRequest, sending)
			sent <- struct{}{}
			return
		}
		server.sendResponse(cc, req.h, req.replyv.Interface(), sending)
		sent <- struct{}{}
	}()

	if timeout == 0 {
		<-called
		<-sent
		return
	}
	select {
	case <-time.After(timeout):
		req.h.Error = fmt.Sprintf("rpc server: request handle timeout: expect within %s", timeout)
		server.sendResponse(cc, req.h, invalidRequest, sending)
	case <-called:
		<-sent
	}

	/*//通过req.svc.call完成方法调用，将replyv传递给sendResponse完成序列化
	err := req.svc.call(req.mtype, req.argv, req.replyv)
	if err != nil {
		req.h.Error = err.Error()
		server.sendResponse(cc, req.h, invalidRequest, sending)
		return
	}
	server.sendResponse(cc, req.h, req.replyv.Interface(), sending)*/
}

func (server *Server) Register(rcvr interface{}) error {
	s := newService(rcvr)
	if _, dup := server.serviceMap.LoadOrStore(s.name, s); dup {
		return errors.New("rpc : service already defined : " + s.name)
	}
	return nil
}

func Register(rcvr interface{}) error {

	return DefaultServer.Register(rcvr)
}

func (server *Server) findService(serviceMethod string) (svc *service, mtype *methodType, err error) {
	dot := strings.LastIndex(serviceMethod, ".")
	if dot < 0 {
		err = errors.New("rpc server : service/method request ill-formed: " + serviceMethod)
		return
	}
	//因为ServiceMethod的构成是 Service.Method ，因此先将其分割为两部分，第一部分是Service的名称，第二部分即方法名。
	//从serviceMap中找到对应的service实例，再从service实例的method中，找到对应的methodType
	serviceName, methodName := serviceMethod[:dot], serviceMethod[dot+1:]
	svci, ok := server.serviceMap.Load(serviceName)
	if !ok {
		err = errors.New("rpc server : can't find service " + serviceName)
		return
	}
	svc = svci.(*service)
	mtype = svc.method[methodName]
	if mtype == nil {
		err = errors.New("rpc server : can't find method " + methodName)
	}
	return
}

// 实现HTTP服务，用于处理RPC请求
const (
	connected        = "200 Connect to Gee RPC"
	defaultRPCPath   = "/_geerpc_"
	defaultDebugPath = "/debug/geerpc"
)

// 该函数实现了回答RPC的响应的http处理器
func (server *Server) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method != "CONNECT" {
		w.Header().Set("Content-Type", "text/plain;charset=utf-8")
		w.WriteHeader(http.StatusMethodNotAllowed)
		_, _ = io.WriteString(w, "405 must CONNECT\n")
		return
	}

	//Hijack函数的作用是从HTTP服务中获取底层网络连接并接管它
	//Hijack方法返回的conn对象代表了客户端和服务器之间的底层连接，
	//通过将连接传递给server.ServerConn(conn)方法，我们可以使用自定义的RPC协议或其他协议在服务器和客户端之间进行通信
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		log.Print("rpc hijacking", req.RemoteAddr, ":", err.Error())
		return
	}
	_, _ = io.WriteString(conn, "HTTP/1.0"+connected+"\n\n")
	//将Conn连接传递给ServerConn方法进行进一步处理
	server.ServeConn(conn)
}

// HandleHTTP函数 用于注册HTTP请求的处理程序。
func (server *Server) HandleHTTP() {
	//使用了http.Handle函数将默认的RPC路径defaultRPCRath（默认值为“/geerpc”）和server实例绑定在一起，从而提供了一个方便的方法来处理RPC请求
	http.Handle(defaultRPCPath, server)
	//新增http-client
	http.Handle(defaultDebugPath, debugHTTP{server})
	log.Println("rpc server debug path:", defaultDebugPath)
}

// 该函数是简单地调用HandleHTTP()方法的快捷方法，将默认服务器与默认RPC路径绑定在一起，
// 它提供了一个快速设置HTTP处理程序的方式，以便可以使用默认服务器进行RPC调用
func HandleHTTP() {
	DefaultServer.HandleHTTP()
}
