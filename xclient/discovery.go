package xclient

import (
	"errors"
	"math"
	"math/rand"
	"sync"
	"time"
)

type SelectMode int

// 服务发现策略，仅实现轮询和随机
const (
	RanDomSelect SelectMode = iota
	RoundRobinSelect
)

// Discovery 服务发现接口
type Discovery interface {
	Refresh() error
	Update(servers []string) error
	Get(Mode SelectMode) (string, error)
	GetAll() ([]string, error)
}

type MultiServersDiscovery struct {
	r       *rand.Rand
	mu      sync.Mutex
	servers []string
	index   int
}

func (d *MultiServersDiscovery) Refresh() error {
	return nil
}

func (d *MultiServersDiscovery) Get(Mode SelectMode) (string, error) {
	// 上锁
	d.mu.Lock()
	defer d.mu.Unlock()

	// 服务器数量
	n := len(d.servers)
	if n == 0 {
		return "", errors.New("rpc discovery: no available servers")
	}
	// 刷新模式
	switch Mode {
	case RanDomSelect:
		{
			// 随机选择一个
			return d.servers[d.r.Intn(n)], nil
		}
	case RoundRobinSelect:
		{
			// 使用计数器去轮询
			s := d.servers[d.index%n]
			// TODO 其实不取余也没问题？只要在安全范围就行？
			d.index = (d.index + 1) % n
			return s, nil
		}
	default:
		{
			return "", errors.New("rpc discovery: not supported select mode")
		}
	}
}

func (d *MultiServersDiscovery) GetAll() (strings []string, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	// 创建切片并且将源数据拷贝一份出去
	servers := make([]string, len(d.servers), len(d.servers))
	copy(servers, d.servers)
	return servers, nil
}

func NewMultiServerDiscovery(servers []string) *MultiServersDiscovery {
	d := &MultiServersDiscovery{
		servers: servers,
		// 设置随机数种子
		r: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
	// 生成序列号
	d.index = d.r.Intn(math.MaxInt32 - 1)
	return d
}

// 判断MultiServersDiscovery是否实现了Discovery接口
var _ Discovery = (*MultiServersDiscovery)(nil)

func (d *MultiServersDiscovery) Update(servers []string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.servers = servers
	return nil
}
