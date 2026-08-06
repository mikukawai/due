package dsclient

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client DS 控制 API 客户端（TCP 短连接 JSON Line 协议）
type Client struct {
	addr string
}

// New 创建 DS 客户端
func New(addr string) *Client {
	return &Client{addr: addr}
}

// ============================ 响应结构体 ============================

// PingResp ping 响应
type PingResp struct {
	OK          bool   `json:"ok"`
	BattlePort  int    `json:"battle_port"`
	ControlPort int    `json:"control_port"`
	RoomCount   int    `json:"room_count"`
	Service     string `json:"service"`
}

// CreateRoomResp 创建房间响应
type CreateRoomResp struct {
	OK           bool     `json:"ok"`
	RoomKey      string   `json:"room_key"`
	BattlePort   int      `json:"battle_port"`
	PlayerTokens []string `json:"player_tokens"`
	PlayerDecks  []string `json:"player_decks"`
	Status       string   `json:"status"`
}

// RoomInfo DS 返回的房间信息
type RoomInfo struct {
	RoomKey      string   `json:"room_key"`
	PlayerTokens []string `json:"player_tokens"`
	PlayerDecks  []string `json:"player_decks"`
	Status       string   `json:"status"`
}

// ListRoomsResp 房间列表响应
type ListRoomsResp struct {
	OK    bool        `json:"ok"`
	Rooms []*RoomInfo `json:"rooms"`
}

// CloseRoomResp 关闭房间响应
type CloseRoomResp struct {
	OK      bool   `json:"ok"`
	RoomKey string `json:"room_key"`
	Message string `json:"message"`
}

// Result 战斗结果
type Result struct {
	RoomKey    string `json:"room_key"`
	WinnerID   int    `json:"winner_id"`   // -1 表示无效
	Reason     string `json:"reason"`
	Status     string `json:"status"`
	PlayerDecks []string `json:"player_decks"`
}

// PullResultsResp 拉取结果响应
type PullResultsResp struct {
	OK      bool      `json:"ok"`
	Results []*Result `json:"results"`
}

// ============================ 命令方法 ============================

// Ping 测试连通性
func (c *Client) Ping() (*PingResp, error) {
	var resp PingResp
	if err := c.call(&resp, "ping"); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CreateRoom 创建对战房间
func (c *Client) CreateRoom(playerDecks [2]string, metadata map[string]any) (*CreateRoomResp, error) {
	var resp CreateRoomResp
	if err := c.call(&resp, "create_room",
		"player_decks", playerDecks,
		"metadata", metadata,
	); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ListRooms 列出所有房间
func (c *Client) ListRooms() (*ListRoomsResp, error) {
	var resp ListRoomsResp
	if err := c.call(&resp, "list_rooms"); err != nil {
		return nil, err
	}
	return &resp, nil
}

// CloseRoom 关闭房间
func (c *Client) CloseRoom(roomKey, reason string) error {
	var resp CloseRoomResp
	if err := c.call(&resp, "close_room",
		"room_key", roomKey,
		"reason", reason,
	); err != nil {
		return err
	}
	if !resp.OK {
		return fmt.Errorf("close_room failed: %s", resp.Message)
	}
	return nil
}

// PullResults 拉取已完成战斗结果
func (c *Client) PullResults(clear bool) (*PullResultsResp, error) {
	var resp PullResultsResp
	if err := c.call(&resp, "pull_results", "clear", clear); err != nil {
		return nil, err
	}
	return &resp, nil
}

// ============================ 底层通信 ============================

// call 通用调用：Dial → Write JSON+\n → ReadLine → Unmarshal → Close
func (c *Client) call(resp any, action string, kv ...any) error {
	// 构建请求 JSON
	req := map[string]any{"action": action}
	for i := 0; i+1 < len(kv); i += 2 {
		req[kv[i].(string)] = kv[i+1]
	}

	reqBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	// TCP 短连接
	conn, err := net.DialTimeout("tcp", c.addr, 3*time.Second)
	if err != nil {
		return fmt.Errorf("dial ds %s: %w", c.addr, err)
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(3 * time.Second))

	// 发送：JSON + \n
	if _, err := conn.Write(append(reqBytes, '\n')); err != nil {
		return fmt.Errorf("write ds: %w", err)
	}

	// 读取响应行
	reader := bufio.NewReader(conn)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return fmt.Errorf("read ds response: %w", err)
	}

	// 解析响应
	if err := json.Unmarshal(line, resp); err != nil {
		return fmt.Errorf("unmarshal ds response: %w (raw: %s)", err, string(line))
	}

	return nil
}
