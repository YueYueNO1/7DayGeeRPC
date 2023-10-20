package xclient

import (
	"log"
	"net/http"
	"strings"
	"time"
)

type GeeRegistryDiscovery struct {
	*MultiServerDiscovery               //嵌套了MultiServersDiscovery，很多能力可用复用
	registry              string        //注册中心的地址
	timeout               time.Duration //服务列表的过期时间
	lastUpdate            time.Time     //代表最后从注册中心更新服务列表的时间，默认10s过期，10s之后需要从注册中心更新新的列表
}

const defaultUpdateTimeout = time.Second * 10

func NewGeeRegistryDiscovery(registerAddr string, timeout time.Duration) *GeeRegistryDiscovery {

	if timeout == 0 {
		timeout = defaultUpdateTimeout
	}
	d := &GeeRegistryDiscovery{
		MultiServerDiscovery: NewMultiServerDiscovery(make([]string, 0)),
		registry:             registerAddr,
		timeout:              timeout,
	}
	return d
}

// 实现超时获取：
// 更新服务地址列表
func (d *GeeRegistryDiscovery) Update(servers []string) error {

	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	//更新时间戳
	d.lastUpdate = time.Now()
	return nil
}

// 作用是从注册中心刷新服务地址列表
func (d *GeeRegistryDiscovery) Refresh() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	//判断当前时间距离最近一次更新的时间是否超过了设定的超时时间,如果超过则需要更新
	if d.lastUpdate.Add(d.timeout).After(time.Now()) {
		return nil
	}
	log.Println("rpc registry: refresh servers from registry", d.registry)
	//获得注册中心的响应resp
	resp, err := http.Get(d.registry)
	if err != nil {
		log.Println("rpc registry refresh err: ", err)
		return err
	}
	//通过解析响应头的“X-Geerpc-Servers”字段，获得注册中心返回的服务地址列表。
	servers := strings.Split(resp.Header.Get("X-Geerpc-Servers"), ",")
	d.servers = make([]string, 0, len(servers))
	for _, server := range servers {
		if strings.TrimSpace(server) != "" {
			//将解析得到的服务地址赋值给d.servers
			d.servers = append(d.servers, strings.TrimSpace(server))
		}
	}
	//更新lastUpdate为当前时间
	d.lastUpdate = time.Now()
	//返回nil表示刷新成功
	return nil
}

// 需要调用Refresh函数以确保服务列表没有过期
func (d *GeeRegistryDiscovery) Get(mode SelectMode) (string, error) {
	if err := d.Refresh(); err != nil {
		return "", err
	}
	return d.MultiServerDiscovery.Get(mode)
}

// 需要调用Refresh函数以确保服务列表没有过期
func (d *GeeRegistryDiscovery) GetAll() ([]string, error) {
	if err := d.Refresh(); err != nil {
		return nil, err
	}
	return d.MultiServerDiscovery.GetAll()

}
