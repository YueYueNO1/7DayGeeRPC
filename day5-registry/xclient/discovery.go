package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

const (
	RandomSelect     SelectMode = iota //随机选择
	RoundRobinSelect                   //轮询选择
)

type Discovery interface {
	Refresh() error                      //从远程注册中心刷新服务列表
	Update(servers []string) error       //更新服务列表
	Get(mode SelectMode) (string, error) //根据指定的选择模式从服务列表中选择一个服务地址
	GetAll() ([]string, error)           //获取当前可用的服务地址列表和可能的错误
}

type MultiServerDiscovery struct {
	r       *rand.Rand   //随机数生成器
	mu      sync.RWMutex //防止数据冲突和错误发生
	servers []string     //保存所有可用地址
	index   int          //表示选择的服务器地址在servers切片中的下标位置
}

func NewMultiServerDiscovery(servers []string) *MultiServerDiscovery {
	d := &MultiServerDiscovery{
		servers: servers,
		//r是一个产生随机数的实例，初始化时使用时间戳设定随机数种子，避免每次产生相同的随机数序列
		r: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	//用于记录Round Robin算法已经轮询到的位置，为了避免每次从0开始，初始化时随机设定一个值
	d.index = d.r.Intn(math.MaxInt32)
	return d
}

// 显式声明
var _Discovery = (*MultiServerDiscovery)(nil)

// 用于刷新服务地址列表
func (d *MultiServerDiscovery) Refresh() error {
	return nil
}

// 更新服务地址的列表
func (d *MultiServerDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}

// 用于根据指定的选择模式从服务列表中选择一个服务地址
func (d *MultiServerDiscovery) Get(mode SelectMode) (string, error) {

	d.mu.Lock()
	defer d.mu.Unlock()
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}

	//有两种选择模式
	switch mode {
	//随机选择一个服务地址，通过调用随机数生成器从切片中选取一个随机下标，并返回对应的服务地址
	case RandomSelect:
		return d.servers[d.r.Intn(n)], nil
		//轮询选择一个服务地址，根据d.index %n 的计算结果，从切片中选择一个下标对应的服务地址，
		//并将d.index + 1 ，以实现下次选择时的位置轮转
	case RoundRobinSelect:
		s := d.servers[d.index%n]
		d.index = (d.index + 1) % n
		return s, nil
	default:
		return "", errors.New("rpc discovery: not supported select mode")
	}
}

// 获取当前所有可用的服务地址列表
func (d *MultiServerDiscovery) GetAll() ([]string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}
