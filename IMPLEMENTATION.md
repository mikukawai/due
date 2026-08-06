# DUE 服务器实现任务列表

## 协议层
- [ ] 二进制帧编解码 — 格式 `[4B size BE][1B header][4B route BE][NB JSON body]`，header=0x00 数据/0x80 心跳

## 认证
- [ ] JWT HS256 生成/验证 — Claims: uid/type/iat/exp，密钥 `due-game-server-secret-change-me`，access 7200s / refresh 604800s
- [ ] 登录/重连/登出路由 — 路由号 1001-1004，登录成功下发 token 对 + player 信息

## 空闲心跳机制

**不是固定间隔心跳，而是"空闲 10s 才发"。**

- 每 session 记录 `last_send_time`（任何 `WriteMessage` 都更新）
- 每 session 记录 `last_recv_time`（任何 `ReadMessage` 都更新）
- Tick 检查（每 1s）：若 `now - last_send_time > 10s` → 发心跳包
- 若 `now - last_recv_time > 20s` → 断开连接（连续 2 次心跳周期无数据）

**Go WritePump 伪代码：**
```go
func (s *Session) WritePump() {
    ticker := time.NewTicker(1 * time.Second)
    for {
        select {
        case data := <-s.sendQueue:
            s.Conn.WriteMessage(websocket.BinaryMessage, data)
            s.lastSendTime = time.Now()
        case <-ticker.C:
            if time.Since(s.lastSendTime) > 10*time.Second {
                s.sendQueue <- protocol.PackHeartbeat()
            }
        }
    }
}
```

**Go ReadPump 伪代码：**
```go
func (s *Session) ReadPump() {
    for {
        _, msg, err := s.Conn.ReadMessage()
        if err != nil { return }
        s.lastRecvTime = time.Now()
        route, body, isHeartbeat, _ := protocol.UnpackMessage(msg)
        if isHeartbeat {
            continue  // 收到的回复心跳，只更新 lastRecvTime，不发回
        }
        dispatch(s, route, body)
    }
}
```

**超时检查 goroutine：**
```go
go func() {
    ticker := time.NewTicker(5 * time.Second)
    for range ticker.C {
        if time.Since(s.lastRecvTime) > 20*time.Second {
            s.Conn.Close()
            return
        }
    }
}()
```

## WebSocket 会话管理
- [ ] Session 结构体 — Conn/UID/AccessToken/MatchRoomKey/sendQueue + lastSendTime/lastRecvTime
- [ ] 读写 Pump — ReadPump 解帧分发 / WritePump 消费 sendQueue 写入
- [ ] **空闲心跳** — WritePump 每秒检查，距上次发送超 10s 发心跳；ReadPump 记录 lastRecvTime
- [ ] **心跳超时断开** — 独立 goroutine 每 5s 检查 `lastRecvTime`，超 20s 断开
- [ ] 全局 Session 注册表 — uid → Session，支持按 uid 推送消息

## DS 控制 API 客户端
- [ ] TCP 短连接 JSON 行协议 — 目标 `127.0.0.1:7001`，每命令 Dial→Write(json+"\n")→ReadLine→Close
- [ ] create_room — 参数 player_decks[2] + metadata，返回 room_key/battle_port/player_tokens
- [ ] get_room — 查询单房间状态
- [ ] list_rooms — 列出所有房间
- [ ] close_room — 关闭房间
- [ ] pull_results(clear) — 拉取已完成战斗结果

## 匹配房间状态
- [ ] ActiveRoom — room_key/UIDS[2]/tokens[2]，GetPlayerID / OpponentUID 方法
- [ ] InviteRoom — 6位随机码/creator_uid/deck_id

## 匹配管理器
- [ ] 快速匹配队列 — QueueQuickMatch 入队，满 2 人 → create_room → 向双方 push MatchReady(2003)
- [ ] 取消匹配 — CancelMatch 从队列移除
- [ ] 创建邀请房 — CreateInviteRoom 生成6位码，返回 InviteCreated(2010)
- [ ] 加入邀请房 — JoinInviteRoom 查码 → create_room → 双方 push MatchReady
- [ ] 观战列表 — HandleSpectateList → list_rooms → push SpectateRooms(2007)
- [ ] 观战加入 — HandleSpectateJoin → 查 activeRoom → push SpectateJoin(2005) 返回 ds_host/port/room_key
- [ ] 战绩查询 — GetStats 返回 wins/losses

## 结果轮询
- [ ] 后台 goroutine — 每 2s pull_results(true)，遍历结果 → RecordWin/RecordLoss → 向双方 push MatchResult(2006) → 清理 ActiveRoom

## 胜负记录
- [ ] PlayerStore — 内存 map[uid]{Wins, Losses}，RecordWin/RecordLoss/GetRecord

## 路由分发
- [ ] 路由常量定义 — 2001-2012 + 错误码
- [ ] Dispatch 函数 — switch route → 调对应 manager 方法 → 回复

## 入口
- [ ] main.go — 加载配置 → 启动 WebSocket HTTP 服务 → 启动结果轮询 goroutine → 信号优雅关闭

---

## 路由号对照

| 号 | 方向 | 含义 | Body JSON |
|----|------|------|-----------|
| 1001 | C→S | 登录 | `{"token":"<jwt>"}` |
| 1002 | C→S | 重连 | `{"token":"<jwt>"}` |
| 1003 | C→S | 登出 | `{}` |
| 1004 | C→S | 刷新令牌 | `{"token":"<refresh>"}` |
| 2001 | C→S | 快速匹配 | `{"deck_id":"xxx"}` |
| 2002 | C→S | 取消匹配 | `{}` |
| 2003 | S→C | 匹配成功 | `{"ds_host":"...","battle_port":7000,"room_key":"...","player_token":"...","player_id":0,"opponent_uid":1001}` |
| 2004 | C→S | 请求观战列表 | `{}` |
| 2005 | C→S | 加入观战 | `{"room_key":"..."}` |
| 2006 | S→C | 结算通知 | `{"room_key":"...","winner_uid":1001,"loser_uid":1002,"winner_player_id":0,"reason":"normal"}` |
| 2007 | S→C | 观战房间列表 | `{"rooms":[...]}` |
| 2008 | C→S | 创建邀请房 | `{"deck_id":"xxx"}` |
| 2009 | C→S | 加入邀请房 | `{"invite_code":"ABC123","deck_id":"xxx"}` |
| 2010 | S→C | 邀请房已创建 | `{"invite_code":"ABC123"}` |
| 2011 | C→S | 查询战绩 | `{}` |
| 2012 | S→C | 战绩返回 | `{"wins":5,"losses":2}` |

## DS TCP 控制 API

**⚠️ 关键：JSON 字段名是 `"command"` 不是 `"cmd"`！**
**格式：每行一个 JSON，发送 `json + "\n"`，DS 按 `\n` 分割行。**

| command | 输入 | 输出 |
|---------|------|------|
| `ping` | `{"command":"ping"}` | `{"ok":true,"service":"battle_server","battle_port":7000,"control_port":7001,"room_count":0}` |
| `create_room` | `{"command":"create_room","player_decks":["deck_a","deck_b"],"metadata":{"uids":[1001,1002]}}` | `{"ok":true,"room_key":"...","battle_port":7000,"player_tokens":["tok1","tok2"],"status":"waiting","player_decks":[...]}` |
| `get_room` | `{"command":"get_room","room_key":"..."}` | `{"ok":true,"room":{"room_key":"...","status":"...","player_tokens":[...],"player_peers":[...],"player_decks":[...],"metadata":{...}}}` |
| `list_rooms` | `{"command":"list_rooms"}` | `{"ok":true,"rooms":[{...完整房间快照...}]}` |
| `close_room` | `{"command":"close_room","room_key":"...","reason":"finished"}` | `{"ok":true,"room_key":"...","message":"关闭请求已提交"}` |
| `pull_results` | `{"command":"pull_results","clear":true}` | `{"ok":true,"results":[{"room_key":"...","winner_id":0,"reason":"lp_zero","status":"finished","metadata":{...},"completed_at_unix":...}]}` |

**create_room 参数说明：**
- `player_decks`: 必填，2个 deck_id 字符串数组。deck_id 对应 `res://data/decks/<deck_id>.json`，如 `"dark_magician_deck"`、`"blue_eyes_deck"`
- `player_tokens`: 可选，DS 自动生成（格式 `"{room_key}-{player_id}-{random}"`）
- `room_key`: 可选，DS 自动生成
- `shuffle_seed`: 可选，DS 自动生成
- `metadata`: 可选字典，会透传到 result 中。建议传 `{"uids":[1001,1002]}` 用于 DUE 映射玩家

**pull_results 返回的 result 字段（来自 `_build_result`）：**
```json
{
  "room_key": "xxx",
  "winner_id": 0,          // 0=player0胜, 1=player1胜, -1=异常关闭
  "reason": "lp_zero",     // lp_zero/surrender/deck_out/closed
  "status": "finished",    // finished/closed
  "player_decks": ["deck_a","deck_b"],
  "metadata": {"uids":[1001,1002]},  // 透传自 create_room
  "completed_at_unix": 1234567890
}
```
**DUE 需要通过 metadata.uids[winner_id] 映射到 uid 来记录胜负。**

## 配置参数

| 参数 | 值 | 说明 |
|------|-----|------|
| WebSocket 端口 | `:3553` | DUE 监听端口 |
| DS 控制地址 | `127.0.0.1:7001` | DUE → DS 控制连接（同机部署） |
| DS 外部地址 | `127.0.0.1` | **客户端**连接 DS 用的 IP/域名，必须客户端可达 |
| DS 战斗端口 | `7000` | DS ENet 端口 |
| JWT Secret | `due-game-server-secret-change-me` | |
| access TTL | 7200s | |
| refresh TTL | 604800s | |
| 心跳超时 | 20s（空闲10s发心跳） | |
| 结果轮询间隔 | 2s | |

**⚠️ `ds_external_host` 是本次的关键新增配置。** MatchReady 推送里必须用这个 IP：
```json
{"ds_host": "<ds_external_host>", "battle_port": 7000, ...}
```
本地测试填 `127.0.0.1`，Tailscale 部署填 `100.68.33.127`。
