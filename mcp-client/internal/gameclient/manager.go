package gameclient

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dobyte/due/network/ws/v2"
	"github.com/dobyte/due/v2/cluster"
	"github.com/dobyte/due/v2/cluster/client"
	"github.com/dobyte/due/v2/encoding"
)

var (
	ErrClientNotFound = errors.New("client not found")
	ErrTimeout        = errors.New("wait message timeout")
)

// Message 一条从服务器收到的消息（响应或推送）
type Message struct {
	Route int32
	Seq   int32
	Data  json.RawMessage
	Time  time.Time
}

// SimClient 一个模拟的游戏客户端连接
type SimClient struct {
	id     int64 // 本地会话 ID（MCP 工具用该 ID 标识客户端）
	label  string
	cid    int64  // 底层连接 ID
	uid    int64  // 登录后的 uid，未登录为 0
	status string // connecting / connected / logged_in / disconnected
	conn   *client.Conn

	mu    sync.Mutex
	inbox []*Message
	wake  chan struct{}
}

// Manager 统一管理多个模拟客户端
type Manager struct {
	component *client.Client
	proxy     *client.Proxy
	seq       atomic.Int64

	mu    sync.RWMutex
	byID  map[int64]*SimClient
	byCID map[int64]*SimClient
}

// NewManager 初始化 due client 组件并启动（stdout 由 MCP 协议占用，打印临时转向 stderr）
func NewManager() (*Manager, error) {
	m := &Manager{
		byID:  make(map[int64]*SimClient),
		byCID: make(map[int64]*SimClient),
	}

	comp := client.NewClient(
		client.WithClient(ws.NewClient()),
		client.WithCodec(encoding.Invoke("json")),
	)
	m.component = comp
	m.proxy = comp.Proxy()

	// 默认路由处理器：接收所有消息（请求响应 + 服务端推送）
	m.proxy.SetDefaultRouteHandler(m.onMessage)
	m.proxy.AddEventListener(cluster.Connect, m.onConnect)
	m.proxy.AddEventListener(cluster.Disconnect, m.onDisconnect)

	// 组件启动时会向 stdout 打印信息框，会污染 MCP stdio 协议，临时将 stdout 指向 stderr
	withStdoutToStderr(func() {
		comp.Init()
		comp.Start()
	})

	return m, nil
}

// Get 根据本地会话 ID 获取客户端
func (m *Manager) Get(id int64) (*SimClient, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sc, ok := m.byID[id]
	if !ok {
		return nil, ErrClientNotFound
	}
	return sc, nil
}

// List 返回全部客户端
func (m *Manager) List() []*SimClient {
	m.mu.RLock()
	defer m.mu.RUnlock()
	list := make([]*SimClient, 0, len(m.byID))
	for _, sc := range m.byID {
		list = append(list, sc)
	}
	return list
}

// Connect 建立一个新的游戏客户端连接，addr 为空时使用 etc 配置的默认地址
func (m *Manager) Connect(label, addr string) (*SimClient, error) {
	conn, err := m.proxy.Dial(client.WithDialAddr(addr))
	if err != nil {
		return nil, err
	}

	m.mu.Lock()
	sc := &SimClient{
		id:     m.seq.Add(1),
		label:  label,
		cid:    conn.ID(),
		status: "connected",
		conn:   conn,
		wake:   make(chan struct{}, 1),
	}
	m.byID[sc.id] = sc
	m.byCID[conn.ID()] = sc
	m.mu.Unlock()

	return sc, nil
}

// Disconnect 关闭指定客户端连接
func (m *Manager) Disconnect(id int64) error {
	sc, err := m.Get(id)
	if err != nil {
		return err
	}
	if err := sc.conn.Close(); err != nil {
		return err
	}
	sc.setStatus("disconnected")
	return nil
}

// 路由处理器：消息分发到对应连接的消息队列
func (m *Manager) onMessage(ctx *client.Context) {
	m.mu.RLock()
	sc, ok := m.byCID[ctx.CID()]
	m.mu.RUnlock()
	if !ok {
		return
	}

	buf, _ := ctx.Data().([]byte)
	sc.enqueue(&Message{
		Route: ctx.Route(),
		Seq:   ctx.Seq(),
		Data:  append(json.RawMessage(nil), buf...),
		Time:  time.Now(),
	})
}

func (m *Manager) onConnect(conn *client.Conn) {
	m.mu.RLock()
	sc, ok := m.byCID[conn.ID()]
	m.mu.RUnlock()
	if ok {
		sc.setStatus("connected")
	}
}

func (m *Manager) onDisconnect(conn *client.Conn) {
	m.mu.RLock()
	sc, ok := m.byCID[conn.ID()]
	m.mu.RUnlock()
	if ok {
		sc.setStatus("disconnected")
	}
}

// 临时将 stdout 替换为 stderr 执行 fn
func withStdoutToStderr(fn func()) {
	old := os.Stdout
	os.Stdout = os.Stderr
	defer func() { os.Stdout = old }()
	fn()
}

// Send 发送一条协议（fire-and-forget），data 为空时发送空对象
func (sc *SimClient) Send(route int32, data []byte) error {
	if len(data) == 0 {
		data = []byte("{}")
	}
	return sc.conn.Push(&cluster.Message{Route: route, Data: data})
}

// Request 发送协议并等待 expectRoute 路由的响应
func (sc *SimClient) Request(route int32, data []byte, expectRoute int32, timeout time.Duration) (*Message, error) {
	if err := sc.Send(route, data); err != nil {
		return nil, err
	}
	return sc.WaitMessage(&expectRoute, timeout)
}

// WaitMessage 等待下一条消息；route 为 nil 时返回任意路由的下一条消息
func (sc *SimClient) WaitMessage(route *int32, timeout time.Duration) (*Message, error) {
	deadline := time.Now().Add(timeout)
	for {
		sc.mu.Lock()
		for i, msg := range sc.inbox {
			if route == nil || msg.Route == *route {
				sc.inbox = append(sc.inbox[:i], sc.inbox[i+1:]...)
				sc.mu.Unlock()
				return msg, nil
			}
		}
		sc.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, ErrTimeout
		}
		select {
		case <-sc.wake:
		case <-time.After(remaining):
		}
	}
}

// PollMessages 取出当前收到的全部消息（不等待）
func (sc *SimClient) PollMessages() []*Message {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	msgs := sc.inbox
	sc.inbox = nil
	return msgs
}

// Login 发送登录协议并等待响应，成功后将 uid 记到客户端上
func (sc *SimClient) Login(token string, timeout time.Duration) (*LoginRes, error) {
	data, _ := json.Marshal(&LoginReq{Token: token})
	if err := sc.Send(RouteLogin, data); err != nil {
		return nil, err
	}

	route := int32(RouteLogin)
	msg, err := sc.WaitMessage(&route, timeout)
	if err != nil {
		return nil, err
	}

	res := &LoginRes{}
	if err := json.Unmarshal(msg.Data, res); err != nil {
		return nil, err
	}

	sc.mu.Lock()
	sc.uid = res.UID
	if res.Code == 0 {
		sc.status = "logged_in"
	}
	sc.mu.Unlock()

	return res, nil
}

// Info 客户端信息
func (sc *SimClient) Info() map[string]any {
	sc.mu.Lock()
	uid, status := sc.uid, sc.status
	sc.mu.Unlock()
	return map[string]any{
		"client_id": sc.id,
		"label":     sc.label,
		"cid":       sc.cid,
		"uid":       uid,
		"status":    status,
	}
}

// ID 返回本地会话 ID
func (sc *SimClient) ID() int64 { return sc.id }

// Label 返回客户端标签
func (sc *SimClient) Label() string { return sc.label }

// SetLabel 设置客户端标签
func (sc *SimClient) SetLabel(label string) {
	sc.mu.Lock()
	sc.label = label
	sc.mu.Unlock()
}

func (sc *SimClient) setStatus(s string) {
	sc.mu.Lock()
	sc.status = s
	sc.mu.Unlock()
}

func (sc *SimClient) enqueue(msg *Message) {
	sc.mu.Lock()
	sc.inbox = append(sc.inbox, msg)
	sc.mu.Unlock()
	select {
	case sc.wake <- struct{}{}:
	default:
	}
}
