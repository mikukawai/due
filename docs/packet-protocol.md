# 通信协议规范（公共篇）

> 版本：v1.0 | 最后更新：2026-08-05

本文档定义 due 框架的**二进制包格式、心跳机制和编解码规范**，所有服务协议共用。各服务的具体协议号定义请参阅对应文档。

---

## 1. 传输层

| 项目 | 值 |
|------|-----|
| 协议 | WebSocket |
| 地址 | `ws://{gate_host}:3553` |
| 路径 | `/` |
| TLS | 否（开发环境） |
| 消息编码 | JSON（UTF-8） |
| 字节序 | **大端（Big Endian）** |

---

## 2. 二进制包格式

每个消息（发送和接收）都遵循以下固定格式：

```
┌──────────┬──────────┬───────────┬──────────┬──────────────┐
│ size     │ header   │ route     │ seq      │ body         │
│ 4 bytes  │ 1 byte   │ 4 bytes   │ 0 bytes  │ N bytes      │
│ uint32   │ uint8    │ int32     │ (禁用)   │ UTF-8 JSON   │
│ 大端序   │          │ 大端序    │          │              │
└──────────┴──────────┴───────────┴──────────┴──────────────┘
```

### 字段定义

| 字段 | 偏移 | 字节数 | 类型 | 说明 |
|------|------|--------|------|------|
| size | 0 | 4 | uint32 BE | 从 header 到 body 结束的总字节数。size = 1 + 4 + 0 + len(body) |
| header | 4 | 1 | uint8 | bit7=0 → 数据包；bit7=1 → 心跳包 |
| route | 5 | 4 | int32 BE | 协议号，标识消息类型。范围：-2147483648 ~ 2147483647 |
| seq | 9 | 0 | — | 序列号，**当前配置为 0 字节，不启用** |
| body | 9 | N | bytes | 消息体，UTF-8 编码的 JSON 字符串 |

### 常量速查

```
SIZE_OFFSET   = 0
HEADER_OFFSET = 4
ROUTE_OFFSET  = 5
BODY_OFFSET   = 9

HEADER_DATA      = 0x00   // 数据包
HEADER_HEARTBEAT = 0x80   // 心跳包
```

### 封包公式

```
bodyBytes = UTF8(JSON.stringify(data))
size      = 1 + 4 + 0 + bodyBytes.length
total     = 4 + size

输出: [4B size] [1B header] [4B route] [0B seq] [NB body]
```

### 解包公式

```
1. 读取前 4 字节 → size (uint32 BE)
2. 读取第 5 字节 → header (uint8)
3. 判断 header & 0x80：
   - true  → 心跳包，丢弃（或原样回复）
   - false → 数据包，继续
4. 跳过 4 字节 route + 0 字节 seq
5. 读取剩余字节 → body (UTF-8) → JSON.parse()
```

---

## 3. 心跳

### 3.1 WebSocket 层心跳（自动处理）

服务器配置了响应式心跳（`resp`），WebSocket 库会自动收发 ping/pong 帧，**应用层无需介入**。

| 配置项 | 值 |
|--------|-----|
| 心跳机制 | resp（响应式） |
| 检测间隔 | 10 秒 |
| 携带时间 | 是（服务器时间戳放在 pong 帧中） |

### 3.2 应用层心跳（可选）

如需在应用层主动探测连接：

**发送**：`[0x00, 0x00, 0x00, 0x01, 0x80]`

| 字节 | 值 | 含义 |
|------|-----|------|
| 0-3 | `0x00 0x00 0x00 0x01` | size = 1（只有 header） |
| 4 | `0x80` | header，bit7=1 = 心跳 |

**接收**：服务器原样返回 `[0x00, 0x00, 0x00, 0x01, 0x80]`。

---

## 4. TypeScript 编解码工具

以下代码是**所有服务协议共用的基础工具**，可直接复制到项目中使用。

```typescript
/**
 * Due 通信协议编解码工具
 * 所有服务协议共用，无需修改
 */

const SIZE_OFFSET   = 0;
const HEADER_OFFSET = 4;
const ROUTE_OFFSET  = 5;
const BODY_OFFSET   = 9;

const HEADER_DATA      = 0x00;
const HEADER_HEARTBEAT = 0x80;

const encoder = new TextEncoder();
const decoder = new TextDecoder();

/** 打包消息为 ArrayBuffer */
export function packMessage(route: number, data: object): ArrayBuffer {
  const json = JSON.stringify(data);
  const body = encoder.encode(json);
  const size = 1 + 4 + 0 + body.byteLength;
  const total = 4 + size;

  const buf = new ArrayBuffer(total);
  const dv = new DataView(buf);

  dv.setUint32(SIZE_OFFSET, size, false);          // size, 大端
  dv.setUint8(HEADER_OFFSET, HEADER_DATA);          // header
  dv.setInt32(ROUTE_OFFSET, route, false);           // route, 大端

  new Uint8Array(buf, BODY_OFFSET).set(body);       // body

  return buf;
}

/** 解包消息，返回 { route, body }，心跳包返回 null */
export function unpackMessage(data: ArrayBuffer): { route: number; body: any } | null {
  if (data.byteLength < 5) return null;

  const dv = new DataView(data);
  const header = dv.getUint8(HEADER_OFFSET);

  // 心跳包
  if (header & HEADER_HEARTBEAT) return null;

  const route = dv.getInt32(ROUTE_OFFSET, false);
  const bodyLen = 4 + dv.getUint32(SIZE_OFFSET, false) - BODY_OFFSET;
  const bodyBytes = new Uint8Array(data, BODY_OFFSET, bodyLen);
  const body = JSON.parse(decoder.decode(bodyBytes));

  return { route, body };
}

/** 打包心跳包 */
export function packHeartbeat(): Uint8Array {
  const buf = new ArrayBuffer(5);
  const dv = new DataView(buf);
  dv.setUint32(SIZE_OFFSET, 1, false);
  dv.setUint8(HEADER_OFFSET, HEADER_HEARTBEAT);
  return new Uint8Array(buf);
}
```

---

## 5. 配置参考

以下配置来自 `gate/etc/etc.toml` 的 `[packet]` 段，如有修改需同步更新本文档。

```toml
[packet]
byteOrder = "big"
routeBytes = 4
seqBytes   = 0
bufferBytes = 100000
```

| 配置项 | 当前值 | 影响 |
|--------|--------|------|
| byteOrder | big | 所有多字节整数的字节序 |
| routeBytes | 4 | route 字段占 4 字节，BODY_OFFSET = 9 |
| seqBytes | 0 | seq 不启用，占 0 字节 |
| bufferBytes | 100000 | 单消息 body 最大字节数 |

> **注意**：若修改 `routeBytes` 或 `seqBytes`，BODY_OFFSET 和封包/解包偏移量均需同步调整。
