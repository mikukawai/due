package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"due/mcp-client/internal/gameclient"
	"due/mcp-client/internal/jwttoken"
)

const (
	defaultTimeoutMs = 5000
	tokenTTL         = 2 * time.Hour
)

// New 创建 MCP server 并注册全部工具
func New(mgr *gameclient.Manager) *server.MCPServer {
	s := server.NewMCPServer("due-mcp-client", "0.1.0")

	s.AddTools(
		toolServerInfo(mgr),
		toolGenerateToken(),
		toolConnectClient(mgr),
		toolListClients(mgr),
		toolDisconnectClient(mgr),
		toolSendProtocol(mgr),
		toolRequestProtocol(mgr),
		toolWaitMessage(mgr),
		toolPollMessages(mgr),
		toolLogin(mgr),
		toolLogout(mgr),
		toolGreet(mgr),
		toolQuickMatch(mgr),
		toolCancelMatch(mgr),
		toolCreateInviteRoom(mgr),
		toolJoinInviteRoom(mgr),
		toolSpectateList(mgr),
		toolSpectateJoin(mgr),
		toolGetStats(mgr),
	)

	return s
}

// newServerTool 组装 mcp.Tool 与处理器
func newServerTool(
	name string,
	handler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error),
	props ...mcp.ToolOption,
) server.ServerTool {
	return server.ServerTool{Tool: mcp.NewTool(name, props...), Handler: handler}
}

// ============================ 辅助函数 ============================

// ok 返回 JSON 格式的成功结果
func ok(v any) *mcp.CallToolResult {
	out, err := mcp.NewToolResultJSON(v)
	if err != nil {
		return mcp.NewToolResultErrorf("序列化结果失败: %v", err)
	}
	return out
}

// view 将内部消息转换为结构化输出
func view(msg *gameclient.Message) map[string]any {
	var data any
	_ = json.Unmarshal(msg.Data, &data)
	return map[string]any{
		"route": msg.Route,
		"seq":   msg.Seq,
		"data":  data,
		"time":  msg.Time.Format(time.RFC3339Nano),
	}
}

// timeout 解析 timeout_ms 参数
func timeout(req mcp.CallToolRequest) time.Duration {
	ms := mcp.ParseInt(req, "timeout_ms", defaultTimeoutMs)
	if ms <= 0 {
		ms = defaultTimeoutMs
	}
	return time.Duration(ms) * time.Millisecond
}

// parseData 解析 data 参数（支持 JSON 对象或 JSON 字符串），未传返回 nil
func parseData(req mcp.CallToolRequest) ([]byte, error) {
	raw, ok := req.GetArguments()["data"]
	if !ok || raw == nil {
		return nil, nil
	}
	switch v := raw.(type) {
	case string:
		if v == "" {
			return nil, nil
		}
		return []byte(v), nil
	default:
		return json.Marshal(v)
	}
}

// requireClient 根据 client_id 参数查找客户端
func requireClient(mgr *gameclient.Manager, req mcp.CallToolRequest) (*gameclient.SimClient, *mcp.CallToolResult) {
	id := int64(mcp.ParseInt(req, "client_id", 0))
	sc, err := mgr.Get(id)
	if err != nil {
		return nil, mcp.NewToolResultErrorf("client_id=%d 不存在，请先调用 connect_client", id)
	}
	return sc, nil
}

// requestHandler 便捷请求类工具：发送指定路由并等待同路由响应
func requestHandler(mgr *gameclient.Manager, route int32, build func(mcp.CallToolRequest) ([]byte, error)) func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sc, errRes := requireClient(mgr, req)
		if errRes != nil {
			return errRes, nil
		}

		body, err := build(req)
		if err != nil {
			return mcp.NewToolResultErrorf("构造请求体失败: %v", err), nil
		}

		msg, err := sc.Request(route, body, route, timeout(req))
		if err != nil {
			return mcp.NewToolResultErrorf("发送/等待路由 %d 响应失败: %v", route, err), nil
		}

		return ok(map[string]any{
			"client_id": sc.ID(),
			"route":     route,
			"response":  view(msg),
		}), nil
	}
}

// bodyString 从字符串参数构造 JSON 请求体
func bodyString(req mcp.CallToolRequest, key string) ([]byte, error) {
	return json.Marshal(map[string]any{key: mcp.ParseString(req, key, "")})
}

// ============================ 工具定义 ============================

// server_info：查看服务信息与协议路由对照
func toolServerInfo(_ *gameclient.Manager) server.ServerTool {
	return newServerTool("server_info",
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			return ok(map[string]any{
				"name":     "due-mcp-client",
				"version":  "0.1.0",
				"protocol": "二进制协议（size+header+route+JSON body），routeBytes=4，seqBytes=0",
				"routes": map[string]any{
					"greet":                gameclient.RouteGreet,
					"login":                gameclient.RouteLogin,
					"reconnect":            gameclient.RouteReconnect,
					"logout":               gameclient.RouteLogout,
					"refresh_token":        gameclient.RouteRefreshToken,
					"quick_match":          gameclient.RouteQuickMatch,
					"cancel_match":         gameclient.RouteCancelMatch,
					"match_ready(push)":    gameclient.RouteMatchReady,
					"spectate_list":        gameclient.RouteSpectateList,
					"spectate_join":        gameclient.RouteSpectateJoin,
					"match_result(push)":   gameclient.RouteMatchResult,
					"spectate_rooms(push)": gameclient.RouteSpectateRooms,
					"create_invite_room":   gameclient.RouteCreateInviteRoom,
					"join_invite_room":     gameclient.RouteJoinInviteRoom,
					"invite_created(push)": gameclient.RouteInviteCreated,
					"get_stats":            gameclient.RouteGetStats,
					"stats_result(push)":   gameclient.RouteStatsResult,
				},
			}), nil
		},
		mcp.WithDescription("查看本 MCP 客户端的信息、可用的游戏协议路由及协议-路由号对照表"),
	)
}

// generate_token：为指定 uid 生成访问令牌
func toolGenerateToken() server.ServerTool {
	return newServerTool("generate_token",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			uid := int64(mcp.ParseInt(req, "uid", 0))
			token, err := jwttoken.GenerateAccessToken(uid, tokenTTL)
			if err != nil {
				return mcp.NewToolResultErrorf("生成令牌失败: %v", err), nil
			}
			return ok(map[string]any{
				"uid":          uid,
				"access_token": token,
			}), nil
		},
		mcp.WithDescription("为指定 uid 生成访问令牌（JWT，签名与 node 服务器 auth 一致），用于 login 协议"),
		mcp.WithNumber("uid", mcp.Required(), mcp.Description("玩家 uid")),
	)
}

// connect_client：建立新的游戏客户端连接
func toolConnectClient(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("connect_client",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			label := mcp.ParseString(req, "label", "")
			addr := mcp.ParseString(req, "addr", "")
			sc, err := mgr.Connect(label, addr)
			if err != nil {
				return mcp.NewToolResultErrorf("连接 gate 失败: %v", err), nil
			}
			if sc.Label() == "" {
				sc.SetLabel(fmt.Sprintf("client-%d", sc.ID()))
			}
			return ok(sc.Info()), nil
		},
		mcp.WithDescription("建立一个新的游戏客户端连接（支持多个客户端同时在线）"),
		mcp.WithString("label", mcp.Description("客户端标签，便于区分多个客户端；为空时自动命名")),
		mcp.WithString("addr", mcp.Description("gate 拨号地址，默认 ws://127.0.0.1:3553")),
	)
}

// list_clients：查看所有已连接的客户端
func toolListClients(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("list_clients",
		func(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			clients := mgr.List()
			list := make([]map[string]any, 0, len(clients))
			for _, sc := range clients {
				list = append(list, sc.Info())
			}
			return ok(map[string]any{"count": len(list), "clients": list}), nil
		},
		mcp.WithDescription("列出当前所有已连接的模拟客户端及其状态"),
	)
}

// disconnect_client：断开指定客户端
func toolDisconnectClient(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("disconnect_client",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sc, errRes := requireClient(mgr, req)
			if errRes != nil {
				return errRes, nil
			}
			if err := mgr.Disconnect(sc.ID()); err != nil {
				return mcp.NewToolResultErrorf("断开失败: %v", err), nil
			}
			return ok(map[string]any{"client_id": sc.ID(), "status": "disconnected"}), nil
		},
		mcp.WithDescription("断开指定客户端连接"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
	)
}

// send_protocol：发送协议（不等待响应）
func toolSendProtocol(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("send_protocol",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sc, errRes := requireClient(mgr, req)
			if errRes != nil {
				return errRes, nil
			}
			route := int32(mcp.ParseInt(req, "route", 0))
			body, err := parseData(req)
			if err != nil {
				return mcp.NewToolResultErrorf("解析 data 失败: %v", err), nil
			}
			if err := sc.Send(route, body); err != nil {
				return mcp.NewToolResultErrorf("发送失败: %v", err), nil
			}
			return ok(map[string]any{
				"client_id": sc.ID(),
				"route":     route,
				"sent":      true,
			}), nil
		},
		mcp.WithDescription("向指定客户端发送一条协议（fire-and-forget，不等待响应）。响应/推送请用 poll_messages 或 wait_message 获取"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithNumber("route", mcp.Required(), mcp.Description("路由号，如 1001(login)、2001(quick_match)")),
		mcp.WithObject("data", mcp.Description("协议 JSON 请求体，如 {\"token\":\"xxx\"}")),
	)
}

// request_protocol：发送协议并等待响应
func toolRequestProtocol(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("request_protocol",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sc, errRes := requireClient(mgr, req)
			if errRes != nil {
				return errRes, nil
			}
			route := int32(mcp.ParseInt(req, "route", 0))
			expect := int32(mcp.ParseInt(req, "expect_route", int(route)))
			body, err := parseData(req)
			if err != nil {
				return mcp.NewToolResultErrorf("解析 data 失败: %v", err), nil
			}

			msg, err := sc.Request(route, body, expect, timeout(req))
			if err != nil {
				return mcp.NewToolResultErrorf("发送/等待响应失败: %v", err), nil
			}
			return ok(map[string]any{
				"client_id": sc.ID(),
				"route":     route,
				"response":  view(msg),
			}), nil
		},
		mcp.WithDescription("向指定客户端发送一条协议，并等待 expect_route 路由的响应/推送返回"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithNumber("route", mcp.Required(), mcp.Description("要发送的路由号")),
		mcp.WithObject("data", mcp.Description("协议 JSON 请求体")),
		mcp.WithNumber("expect_route", mcp.Description("等待响应的路由号，默认等于 route")),
		mcp.WithNumber("timeout_ms", mcp.Description("等待超时毫秒数，默认 5000")),
	)
}

// wait_message：等待客户端收到下一条消息（用于服务端推送）
func toolWaitMessage(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("wait_message",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sc, errRes := requireClient(mgr, req)
			if errRes != nil {
				return errRes, nil
			}
			var route *int32
			if _, ok := req.GetArguments()["route"]; ok {
				r := int32(mcp.ParseInt(req, "route", 0))
				route = &r
			}
			msg, err := sc.WaitMessage(route, timeout(req))
			if err != nil {
				return mcp.NewToolResultErrorf("等待消息超时: %v", err), nil
			}
			return ok(map[string]any{
				"client_id": sc.ID(),
				"message":   view(msg),
			}), nil
		},
		mcp.WithDescription("等待指定客户端收到下一条消息（如 2003 匹配成功推送、2006 结算推送），route 为空则返回任意路由消息"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithNumber("route", mcp.Description("要等待的路由号，为空则等待任意消息")),
		mcp.WithNumber("timeout_ms", mcp.Description("等待超时毫秒数，默认 5000")),
	)
}

// poll_messages：立即取出客户端收到的所有待处理消息
func toolPollMessages(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("poll_messages",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sc, errRes := requireClient(mgr, req)
			if errRes != nil {
				return errRes, nil
			}
			msgs := sc.PollMessages()
			list := make([]map[string]any, 0, len(msgs))
			for _, msg := range msgs {
				list = append(list, view(msg))
			}
			return ok(map[string]any{
				"client_id": sc.ID(),
				"count":     len(list),
				"messages":  list,
			}), nil
		},
		mcp.WithDescription("立即取出指定客户端当前收到的所有待处理消息（响应与推送），不等待"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
	)
}

// login：登录
func toolLogin(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("login",
		func(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			sc, errRes := requireClient(mgr, req)
			if errRes != nil {
				return errRes, nil
			}
			token := mcp.ParseString(req, "token", "")
			res, err := sc.Login(token, timeout(req))
			if err != nil {
				return mcp.NewToolResultErrorf("登录失败: %v", err), nil
			}
			return ok(map[string]any{
				"client_id": sc.ID(),
				"code":      res.Code,
				"uid":       res.UID,
				"player":    res.Player,
			}), nil
		},
		mcp.WithDescription("使用 access_token 登录（路由 1001），登录成功后客户端 uid 自动绑定"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("token", mcp.Required(), mcp.Description("access token，可用 generate_token 生成")),
		mcp.WithNumber("timeout_ms", mcp.Description("等待超时毫秒数，默认 5000")),
	)
}

// logout：主动登出
func toolLogout(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("logout",
		requestHandler(mgr, gameclient.RouteLogout, func(_ mcp.CallToolRequest) ([]byte, error) {
			return nil, nil
		}),
		mcp.WithDescription("主动登出（路由 1003，需已登录）"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
	)
}

// greet：问候测试
func toolGreet(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("greet",
		requestHandler(mgr, gameclient.RouteGreet, func(req mcp.CallToolRequest) ([]byte, error) {
			return bodyString(req, "message")
		}),
		mcp.WithDescription("发送问候测试协议（路由 1），无需登录"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("message", mcp.Description("问候内容")),
		mcp.WithNumber("timeout_ms", mcp.Description("等待超时毫秒数，默认 5000")),
	)
}

// quick_match：快速匹配（需要先登录）
func toolQuickMatch(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("quick_match",
		requestHandler(mgr, gameclient.RouteQuickMatch, func(req mcp.CallToolRequest) ([]byte, error) {
			return bodyString(req, "deck_id")
		}),
		mcp.WithDescription("发起快速匹配（路由 2001，需先登录）。第一个匹配者会额外收到 2003 推送"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("deck_id", mcp.Required(), mcp.Description("卡组 ID，如 dark_magician_deck")),
		mcp.WithNumber("timeout_ms", mcp.Description("匹配等待超时毫秒数，默认 5000")),
	)
}

// cancel_match：取消匹配
func toolCancelMatch(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("cancel_match",
		requestHandler(mgr, gameclient.RouteCancelMatch, func(_ mcp.CallToolRequest) ([]byte, error) {
			return nil, nil
		}),
		mcp.WithDescription("取消快速匹配（路由 2002，需已登录）"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
	)
}

// create_invite_room：创建邀请房
func toolCreateInviteRoom(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("create_invite_room",
		requestHandler(mgr, gameclient.RouteCreateInviteRoom, func(req mcp.CallToolRequest) ([]byte, error) {
			return bodyString(req, "deck_id")
		}),
		mcp.WithDescription("创建邀请房（路由 2008，需已登录），返回邀请码；好友加入后创建者收到 2003 推送"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("deck_id", mcp.Required(), mcp.Description("卡组 ID")),
		mcp.WithNumber("timeout_ms", mcp.Description("等待超时毫秒数，默认 5000")),
	)
}

// join_invite_room：加入邀请房
func toolJoinInviteRoom(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("join_invite_room",
		requestHandler(mgr, gameclient.RouteJoinInviteRoom, func(req mcp.CallToolRequest) ([]byte, error) {
			return json.Marshal(map[string]any{
				"invite_code": mcp.ParseString(req, "invite_code", ""),
				"deck_id":     mcp.ParseString(req, "deck_id", ""),
			})
		}),
		mcp.WithDescription("通过邀请码加入邀请房（路由 2009，需已登录），返回房间数据"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("invite_code", mcp.Required(), mcp.Description("邀请码")),
		mcp.WithString("deck_id", mcp.Required(), mcp.Description("卡组 ID")),
		mcp.WithNumber("timeout_ms", mcp.Description("等待超时毫秒数，默认 5000")),
	)
}

// spectate_list：观战列表
func toolSpectateList(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("spectate_list",
		requestHandler(mgr, gameclient.RouteSpectateList, func(_ mcp.CallToolRequest) ([]byte, error) {
			return nil, nil
		}),
		mcp.WithDescription("查询当前可观战的房间列表（路由 2004，需已登录）"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
	)
}

// spectate_join：加入观战
func toolSpectateJoin(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("spectate_join",
		requestHandler(mgr, gameclient.RouteSpectateJoin, func(req mcp.CallToolRequest) ([]byte, error) {
			return bodyString(req, "room_key")
		}),
		mcp.WithDescription("加入某房间观战（路由 2005，需已登录）"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
		mcp.WithString("room_key", mcp.Required(), mcp.Description("房间 key")),
	)
}

// get_stats：查询战绩
func toolGetStats(mgr *gameclient.Manager) server.ServerTool {
	return newServerTool("get_stats",
		requestHandler(mgr, gameclient.RouteGetStats, func(_ mcp.CallToolRequest) ([]byte, error) {
			return nil, nil
		}),
		mcp.WithDescription("查询当前玩家的战绩（路由 2011，需已登录）"),
		mcp.WithNumber("client_id", mcp.Required(), mcp.Description("客户端 ID")),
	)
}
