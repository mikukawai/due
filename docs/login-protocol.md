# 登录服务协议

> 版本：v1.0 | 最后更新：2026-08-05

本文档定义登录相关的协议号、参数和返回值。通信层的包格式与心跳机制请参阅 **[通信协议规范（公共篇）](packet-protocol.md)**。

---

## 1. 协议列表

| 协议号 | 名称 | 需登录 | 说明 |
|--------|------|--------|------|
| 1001 | login | 否 | Token 登录 |
| 1002 | reconnect | 否 | 断线重连 |
| 1003 | logout | **是** | 主动登出 |
| 1004 | refreshToken | **是** | 刷新令牌 |
| 1 | greet | 否 | 问候测试（调试用） |

> **需登录 = 是**：调用前必须已通过 login/reconnect 完成认证，否则 Gate 层直接拒绝，返回 `code=9`。

---

## 2. 协议详细定义

### 2.1 登录 — route=1001

客户端连接成功后发送登录请求完成身份认证。成功后服务器自动将连接与用户 ID 绑定。

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| token | string | 是 | JWT 访问令牌 |

```json
{
    "token": "eyJhbGciOiJIUzI1NiIs..."
}
```

#### 成功响应 (code=0)

| 字段 | 类型 | 说明 |
|------|------|------|
| code | int | 0 = 成功 |
| uid | int64 | 用户 ID |
| accessToken | string | 新的访问令牌（替换旧 token） |
| refreshToken | string | 刷新令牌（用于续期，7 天有效） |
| player | object | 玩家基础信息 |
| player.uid | int64 | 用户 ID |
| player.nickname | string | 昵称 |
| player.level | int | 等级（新玩家默认为 1） |

```json
{
    "code": 0,
    "uid": 10001,
    "accessToken": "eyJhbGciOiJIUzI1NiIs...",
    "refreshToken": "eyJhbGciOiJIUzI1NiIs...",
    "player": {
        "uid": 10001,
        "nickname": "Player_10001",
        "level": 1
    }
}
```

#### 失败响应

```json
{"code": 7}    // Unauthorized — Token 无效或已过期
{"code": 6}    // InternalError — 服务器内部错误
```

---

### 2.2 断线重连 — route=1002

客户端断线后用旧 token 重新连接。服务器自动处理旧连接的顶号。

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| token | string | 是 | 之前获取的访问令牌（未过期即可） |

```json
{
    "token": "eyJhbGciOiJIUzI1NiIs..."
}
```

#### 成功响应 (code=0)

| 字段 | 类型 | 说明 |
|------|------|------|
| code | int | 0 = 成功 |
| uid | int64 | 用户 ID |

```json
{
    "code": 0,
    "uid": 10001
}
```

#### 失败响应

```json
{"code": 7}    // Unauthorized — Token 无效或已过期
```

---

### 2.3 主动登出 — route=1003

**需要已登录。** 解绑连接与用户的关联。

#### 请求

无参数，发送空 JSON 对象。

```json
{}
```

#### 响应

```json
{
    "code": 0,
    "message": "logout success"
}
```

---

### 2.4 刷新令牌 — route=1004

**需要已登录。** 用 refresh token 换取新的令牌对，避免重新登录。

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| token | string | 是 | 刷新令牌（login 返回的 refreshToken） |

```json
{
    "token": "eyJhbGciOiJIUzI1NiIs..."
}
```

#### 成功响应 (code=0)

| 字段 | 类型 | 说明 |
|------|------|------|
| code | int | 0 = 成功 |
| accessToken | string | 新的访问令牌 |
| refreshToken | string | 新的刷新令牌 |

```json
{
    "code": 0,
    "accessToken": "eyJhbGciOiJIUzI1NiIs...",
    "refreshToken": "eyJhbGciOiJIUzI1NiIs..."
}
```

#### 失败响应

```json
{"code": 7}    // Unauthorized — Token 无效或已过期
```

---

### 2.5 问候测试 — route=1

调试用的回声路由。

#### 请求

| 字段 | 类型 | 必填 | 说明 |
|------|------|------|------|
| message | string | 是 | 任意字符串 |

```json
{
    "message": "Hello Server!"
}
```

#### 响应

```json
{
    "code": 0,
    "message": "I'm server, and the current time is: 2026/08/05 11:00:00.000000"
}
```

---

## 3. 错误码汇总

| code | 常量名 | 说明 |
|------|--------|------|
| 0 | OK | 成功 |
| 3 | InvalidArgument | 请求参数格式错误 |
| 6 | InternalError | 服务器内部错误 |
| 7 | Unauthorized | 未授权 / Token 无效或已过期 |
| 9 | IllegalRequest | 未登录时访问需授权的协议 |

---

## 4. 交互流程

### 正常登录

```
Client                           Gate                          Node
  │                                │                             │
  │── WS Connect ─────────────────→│                             │
  │                                │── Connect 事件 ────────────→│
  │                                │                             │
  │── [1001] {token} ─────────────→│── 投递 ────────────────────→│
  │                                │                   验证 token
  │                                │                   BindGate(uid)
  │                                │                   签发新 token
  │←── {code:0, uid, player,      │←────────────────────────────│
  │     accessToken, refreshToken} │                             │
  │                                │                             │
  │── [业务协议] 消息 ────────────→│ 已绑定 UID，放行 ──────────→│
  │                                │                             │
  │── [1003] {} ──────────────────→│── 投递 ────────────────────→│
  │                                │                   UnbindGate
  │←── {code:0, "logout success"}  │←────────────────────────────│
```

### 断线重连

```
Client                           Gate                          Node
  │                                │                             │
  │ ××× 断线 ×××                   │                             │
  │                                │── Disconnect 事件 ─────────→│
  │                                │                             │
  │── WS Reconnect ───────────────→│                             │
  │── [1002] {token:旧token} ─────→│── 投递 ────────────────────→│
  │                                │                   验证 token
  │                                │                   BindGate(uid)
  │←── {code:0, uid}               │←────────────────────────────│
```

---

## 5. TypeScript 使用示例

```typescript
import { packMessage, unpackMessage, packHeartbeat } from './packet-protocol';

const Route = {
  LOGIN:         1001,
  RECONNECT:     1002,
  LOGOUT:        1003,
  REFRESH_TOKEN: 1004,
} as const;

class DueClient {
  private ws: WebSocket | null = null;
  private listeners = new Map<number, (data: any) => void>();
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;

  constructor(private url: string) {}

  connect(): Promise<void> {
    return new Promise((resolve, reject) => {
      this.ws = new WebSocket(this.url);
      this.ws.binaryType = 'arraybuffer';
      this.ws.onopen = () => resolve();
      this.ws.onerror = (e) => reject(e);
      this.ws.onclose = () => this.stopHeartbeat();
      this.ws.onmessage = (e) => {
        const result = unpackMessage(e.data as ArrayBuffer);
        if (!result) return;
        this.listeners.get(result.route)?.(result.body);
      };
    });
  }

  /** 发送消息并等待响应 */
  async request(route: number, data: object): Promise<any> {
    return new Promise((resolve) => {
      this.listeners.set(route, resolve);
      this.ws!.send(packMessage(route, data));
    });
  }

  /** 登录 */
  login(token: string)     { return this.request(Route.LOGIN, { token }); }
  /** 重连 */
  reconnect(token: string) { return this.request(Route.RECONNECT, { token }); }
  /** 登出 */
  logout()                 { return this.request(Route.LOGOUT, {}); }
  /** 刷新令牌 */
  refreshToken(token: string) { return this.request(Route.REFRESH_TOKEN, { token }); }

  startHeartbeat(ms = 10000) {
    this.stopHeartbeat();
    this.heartbeatTimer = setInterval(() => {
      this.ws?.readyState === WebSocket.OPEN && this.ws.send(packHeartbeat());
    }, ms);
  }

  stopHeartbeat() {
    if (this.heartbeatTimer) { clearInterval(this.heartbeatTimer); this.heartbeatTimer = null; }
  }

  disconnect() { this.stopHeartbeat(); this.ws?.close(); }

  // ====== 使用示例 ======
  static async demo() {
    const client = new DueClient('ws://127.0.0.1:3553');
    await client.connect();

    const res = await client.login('your-jwt-token');
    if (res.code !== 0) return console.error('登录失败:', res.code);

    console.log('登录成功:', res.player.nickname);
    client.startHeartbeat();

    // ... 游戏逻辑 ...

    await client.logout();
    client.disconnect();
  }
}
```
