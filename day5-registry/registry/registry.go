package registry

import (
	"log"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type GeeRegistry struct {
	//默认超时时间设置为5min，也就是说任何注册的服务超过5min，即视为不可用状态
	timeout time.Duration
	mu      sync.Mutex
	servers map[string]*ServerItem
}

type ServerItem struct {
	Addr  string
	start time.Time
}

const (
	defaultPath    = "/_geerpc_/registry"
	defaultTimeoyt = time.Minute * 5 //设置默认时间为5min
)

func New(timeout time.Duration) *GeeRegistry {
	return &GeeRegistry{
		servers: make(map[string]*ServerItem),
		timeout: timeout,
	}

}

var DefaultGeeRegister = New(defaultTimeoyt)

// 为GeeRegistry实现添加服务实例和返回服务列表的方法：
// putServer : 添加服务实例，如果服务已经存在，则更新start
func (r *GeeRegistry) putServer(addr string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := r.servers[addr]
	if s == nil {
		r.servers[addr] = &ServerItem{
			Addr:  addr,
			start: time.Now(),
		}
	} else {
		s.start = time.Now()
	}
}

// aliveServer :返回可用的服务列表，如果存在超时的服务，则删除
func (r *GeeRegistry) aliveServers() []string {

	r.mu.Lock()
	defer r.mu.Unlock()
	var alive []string //存储可用的服务地址
	//遍历r.servers 字典，key为服务地址，value为服务信息结构体
	for addr, s := range r.servers {
		//== 0 表示没有设置超时时间，或者对应服务的启动时间加上超时时间r.timeout仍然晚于当前时间
		if r.timeout == 0 || s.start.Add(r.timeout).After(time.Now()) {
			//则将该服务地址添加到alive中
			alive = append(alive, addr)
		} else {
			//否则说明服务已经超时，需要在r.servers字典中删除
			delete(r.servers, addr)
		}
	}
	//排列，保证返货的可用服务列表有序
	sort.Strings(alive)
	//返回可用的服务列表alive
	return alive
}

// 为了实现上的简单，GeeRegistry采用了HTTP协议提供服务，且所有有用的信息都承载在HTTPHeadder中
// Get：返回所有可用的服务列表，通过自定义字段X-Geerpc-Servers承载
// Post ：添加服务实例或发送心跳，通过自定义字段X-Geerpc_Server承载
// Runs at /_geerpc_/registry
func (r *GeeRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case "GET":
		w.Header().Set("X-Geerpc-Servers", strings.Join(r.aliveServers(), ","))
	case "POST":
		addr := req.Header.Get("X-Geerpc-Server")
		if addr == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		r.putServer(addr)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}

}

// 添加路由
func (r *GeeRegistry) HandleHTTP(registryPath string) {
	http.Handle(registryPath, r)
	log.Println("rpc registry path: ", registryPath)
}

func HandleHTTP() {
	DefaultGeeRegister.HandleHTTP(defaultPath)
}

// 便于服务启动时定时向注册中心发送心跳，默认周期比注册中心设置的过期时间要少1min
func Heartbeat(registry, addr string, duration time.Duration) {

	//未设置过期时间的话
	if duration == 0 {
		duration = defaultTimeoyt - time.Duration(1)*time.Minute
	}

	var err error
	err = sendHeartbeat(registry, addr)
	//以下函数段会在每个心跳周期内发送一次心跳请求，并根据返回的错误判断是否继续发送心跳请求。
	//如果出现错误，说明与注册中心的连接可能断开，循环终止，停止发送心跳请求
	go func() {
		t := time.NewTicker(duration)
		for err == nil {
			<-t.C
			err = sendHeartbeat(registry, addr)
		}
	}()
}

// 该函数的作用是： 向注册中心发送心跳请求，以告知注册中心该服务仍处于存活状态。
// 使用HTTP POST方法发送请求，并在请求头中添加服务地址信息
func sendHeartbeat(registry, addr string) error {
	log.Println(addr, "send heart beat to registry", registry)
	httpClient := &http.Client{}
	//通过创建HTTP POST请求req，请求地址为注册中心地址registry，请求体为空
	req, _ := http.NewRequest("POST", registry, nil)
	//在请求头中加入”X-Geerpc-Server”字段，值为该服务的地址addr
	req.Header.Set("X-Geerpc-Server", addr)
	//发送请求，并获取返回结果
	if _, err := httpClient.Do(req); err != nil {
		log.Println("rpc server : heart beat err: ", err)
		return err
	}
	return nil
}
