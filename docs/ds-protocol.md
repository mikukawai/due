# DS 控制服务协议

> 版本：v1.0 | 最后更新：2026-08-05

本文档定义 **DUE Node → DS 战斗服务器**之间的控制协议：房间创建、房间查询、结果拉取等。通信层为 TCP 短连接 JSON Line 协议，与客户端无关。客户端侧的对战协议请参阅 **[匹配服务协议](match-protocol.md)**。

---

## 1. 连接信息

| 项目 | 值 | 说明 |
|------|-----|------|
| 协议 | TCP | 短连接，每命令一次 Dial |
| 地址 | `ds_address` 配置项 | 如 `100.114.203.26:7001` |
| 编码 | JSON（UTF-8） | 请求/响应均为单行 JSON，以 `\n` 结尾 |
| 命令字段 | `action` | 请求中标识命令的字段名 |

> **注意**：每命令发起的是一次独立 TCP 连接：Dial → Write(`json + "\n"`) → ReadLine → Close。不存在长连接会话。

---

## 2. 通用格式

### 2.1 请求

所有命令请求为单行 JSON + `\n`，命令字段名为 `action`：

```json
{"action": "<command>", "<param1>": <value1>, ...}
```

### 2.2 响应

DS 返回单行 JSON（不含换行，读至 `\n`）。通用成功标记为 `ok`（bool）。

---

## 3. 命令列表

| action | 方向 | 说明 |
|--------|------|------|
| `ping` | DUE→DS | 连通性测试 |
| `create_room` | DUE→DS | **创建对战房间** |
| `list_rooms` | DUE→DS | 列出所有房间 |
| `close_room` | DUE→DS | 关闭房间 |
| `pull_results` | DUE→DS | 拉取已完成战斗结果 |

---

## 4. 命令详细定义

### 4.1 ping — 连通性测试

#### 请求

```json
{"action": "ping"}
```

#### 响应

| 字段 | 类型 | 说明 |
|------|------|------|
| ok | bool | 是否成功 |
| service | string | 服务标识，如 `battle_server` |
| battle_port | int | DS 战斗端口（客户端连接用） |
| control_port | int | DS 控制端口 |
| room_count | int | 当前房间数 |

```json
{"ok": true, "service": "battle_server", "battle_port": 7000, "control_port": 7001, "room_count": 0}
```

---

### 4.2 create_room — 创建房间

快速匹配配对成功、或邀请房加入成功后，DUE 调用该命令在 DS 上创建对战房间。

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| player_decks | array[string] | **是** | 长度为 2 的卡组 ID 数组，顺序对应 player 0 / player 1 |
| metadata | object | 否 | 自定义透传数据，战斗结束后随结果原样返回 |

```json
{
    "action": "create_room",
    "player_decks": ["deck_a", "deck_b"],
    "metadata": {"uid1": 10001, "uid2": 10002}
}
```

> `metadata` 建议传 `{"uid1": <uid>, "uid2": <uid>}`，用于 DUE 在拉取结果时将 winner_id 映射回用户 UID。

#### 响应

| 字段 | 类型 | 说明 |
|------|------|------|
| ok | bool | 是否成功 |
| room_key | string | 房间唯一标识 |
| battle_port | int | 该房间战斗端口（客户端连接用） |
| player_tokens | array[string] | 长度为 2 的玩家对战令牌，顺序对应 player 0 / player 1，DS 自动生成 |
| player_decks | array[string] | 回显的卡组 ID |
| status | string | 房间状态，如 `waiting` / `playing` |

```json
{
    "ok": true,
    "room_key": "a1b2c3d4-...",
    "battle_port": 7000,
    "player_tokens": ["tok0-...", "tok1-..."],
    "player_decks": ["deck_a", "deck_b"],
    "status": "waiting"
}
```

---

### 4.3 list_rooms — 列出所有房间

#### 请求

```json
{"action": "list_rooms"}
```

#### 响应

| 字段 | 类型 | 说明 |
|------|------|------|
| ok | bool | 是否成功 |
| rooms | array | 房间列表 |
| rooms[].room_key | string | 房间 key |
| rooms[].player_tokens | array[string] | 玩家令牌 |
| rooms[].player_decks | array[string] | 卡组 ID |
| rooms[].status | string | 房间状态 |

```json
{
    "ok": true,
    "rooms": [
        {"room_key": "a1b2c3d4-...", "player_tokens": ["tok0", "tok1"], "player_decks": ["deck_a", "deck_b"], "status": "playing"}
    ]
}
```

---

### 4.4 close_room — 关闭房间

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| room_key | string | **是** | 要关闭的房间 key |
| reason | string | 否 | 关闭原因，如 `finished` |

```json
{"action": "close_room", "room_key": "a1b2c3d4-...", "reason": "finished"}
```

#### 响应

| 字段 | 类型 | 说明 |
|------|------|------|
| ok | bool | 是否成功 |
| room_key | string | 关闭的房间 key |
| message | string | 提示信息 |

```json
{"ok": true, "room_key": "a1b2c3d4-...", "message": "关闭请求已提交"}
```

---

### 4.5 pull_results — 拉取已完成战斗结果

DUE 每 2 秒轮询一次（`resultPollInterval`）。战斗结束后结果进入 DS 的待拉取队列，拉取后默认清除（`clear=true`）。

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| clear | bool | 否 | 拉取后是否清除队列，DUE 固定传 `true` |

```json
{"action": "pull_results", "clear": true}
```

#### 响应

| 字段 | 类型 | 说明 |
|------|------|------|
| ok | bool | 是否成功 |
| results | array | 结果列表 |
| results[].room_key | string | 房间 key |
| results[].winner_id | int | 胜者编号：`0`=player0 胜，`1`=player1 胜，`-1`=无效/平局 |
| results[].reason | string | 结束原因：`lp_zero` / `surrender` / `deck_out` / `closed` |
| results[].status | string | 状态：`finished` / `closed` |
| results[].player_decks | array[string] | 卡组 ID |

```json
{
    "ok": true,
    "results": [
        {
            "room_key": "a1b2c3d4-...",
            "winner_id": 0,
            "reason": "lp_zero",
            "status": "finished",
            "player_decks": ["deck_a", "deck_b"]
        }
    ]
}
```

> **胜负映射**：DUE 通过 `metadata.uid1 / metadata.uid2`（create_room 时透传）配合 `winner_id` 将结果对应到具体 UID，并推送结算通知（route=2006）给双方玩家。

---

## 5. 房间创建流程（时序）

快速匹配 / 邀请房两种入口最终都走到同一条房间创建链路：

```
客户端 A             Node                    DS
  |--- 2001 快速匹配 -->|                        |
  |                    |--- create_room ------->|
  |                    |<-- room_key/tokens ----|
  |<-- 2003 MatchReady-|                        |
  |                    |  (记录 ActiveRoom)     |
  |-- ws://ds_host:7000 连接战斗服 ------------>|

客户端 B              Node                    DS
  |--- 2009 加入邀请房 ->|                        |
  |                    |--- create_room ------->|
  |                    |<-- room_key/tokens ----|
  |<-- 2003 MatchReady-|                        |
```

- `quickMatchHandler`（route=2001）与 `joinInviteRoomHandler`（route=2009）内部均调用 `Manager.doPair` → `dsClient.CreateRoom`。
- 创建成功后房间存入 `activeRooms`，并同时向对战双方推送 `MatchReady`（route=2003，含 `ds_host` / `battle_port` / `room_key` / `player_token` / `player_id`）。
- 客户端凭推送数据连接 DS 战斗端口开始对局。

---

## 6. 配置项

| 配置 | 类型 | 默认 | 说明 |
|------|------|------|------|
| `etc.match.ds_address` | string | `100.114.203.26:7001` | DUE→DS 控制连接地址（含端口），node 内部使用 |
| `etc.match.ds_external_host` | string | — | 返回给客户端的 DS 主机地址（不含端口），客户端按 `ds_host:battle_port(7000)` 连接战斗服；为空时回退为 ds_address 的主机部分 |
| 战斗端口 | int | `7000` | DS 战斗端口，硬编码于 `Manager.dsBattlePort`（仅观战默认值，实际以 create_room 返回的 `battle_port` 为准） |
| 结果轮询间隔 | duration | `2s` | 硬编码于 `Manager.resultPollInterval` |
