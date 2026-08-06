package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"due/node/internal/auth"
	"due/node/internal/match"

	"github.com/dobyte/due/locate/redis/v2"
	"github.com/dobyte/due/registry/consul/v2"
	"github.com/dobyte/due/v2"
	"github.com/dobyte/due/v2/cluster"
	"github.com/dobyte/due/v2/cluster/node"
	"github.com/dobyte/due/v2/codes"
	"github.com/dobyte/due/v2/etc"
	"github.com/dobyte/due/v2/log"
	"github.com/dobyte/due/v2/utils/xtime"
)

const (
	greet        = 1    // 问候测试
	login        = 1001 // Token 登录
	reconnect    = 1002 // 断线重连
	logout       = 1003 // 主动登出
	refreshToken = 1004 // 刷新令牌

	quickMatch       = 2001 // 快速匹配
	cancelMatch      = 2002 // 取消匹配
	matchReady       = 2003 // 匹配成功（S→C 推送）
	spectateList     = 2004 // 观战列表
	spectateJoin     = 2005 // 加入观战
	matchResult      = 2006 // 结算通知（S→C 推送）
	spectateRooms    = 2007 // 观战房间列表（S→C 推送）
	createInviteRoom = 2008 // 创建邀请房
	joinInviteRoom   = 2009 // 加入邀请房
	inviteCreated    = 2010 // 邀请房已创建（S→C 推送）
	getStats         = 2011 // 查询战绩
	statsResult      = 2012 // 战绩返回（S→C 推送）
)

// 内存模拟用户数据（后续可替换为数据库）
var players sync.Map // uid(int64) -> *Player

// 匹配管理器
var matchMgr *match.Manager

// Player 玩家基础信息
type Player struct {
	UID      int64  `json:"uid"`
	Nickname string `json:"nickname"`
	Level    int    `json:"level"`
}

func main() {
	// 初始化认证模块
	auth.Init("due-game-server-secret-change-me", 2*time.Hour, 7*24*time.Hour)

	// 创建容器
	container := due.NewContainer()
	// 创建用户定位器
	locator := redis.NewLocator()
	// 创建服务发现
	registry := consul.NewRegistry()
	// 创建节点组件
	component := node.NewNode(
		node.WithLocator(locator),
		node.WithRegistry(registry),
	)
	// 初始化监听（需在 container.Add 之前完成路由注册）
	initListen(component.Proxy())
	// 添加节点组件
	container.Add(component)
	// 启动容器
	container.Serve()
}

// 初始化监听
func initListen(proxy *node.Proxy) {
	// DS 配置：
	//  - ds_address：DUE → DS 控制连接地址（node 内部使用，含端口）
	//  - ds_external_host：返回给客户端的 DS 主机地址（不含端口），客户端按 ds_host:battle_port(7000) 连接战斗服
	dsAddress := etc.Get("etc.match.ds_address", "100.114.203.26:7001").String()
	dsExternalHost := etc.Get("etc.match.ds_external_host").String()

	// 初始化匹配管理器
	matchMgr = match.NewManager(proxy, dsAddress, dsExternalHost)

	// === 认证路由（1001-1004）===
	proxy.Router().AddRouteHandler(login, loginHandler)
	proxy.Router().AddRouteHandler(reconnect, reconnectHandler)
	proxy.Router().AddRouteHandler(logout, logoutHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(refreshToken, refreshTokenHandler, node.AuthorizedRoute)

	// === 匹配路由（2001-2012）===
	proxy.Router().AddRouteHandler(quickMatch, quickMatchHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(cancelMatch, cancelMatchHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(spectateList, spectateListHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(spectateJoin, spectateJoinHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(createInviteRoom, createInviteRoomHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(joinInviteRoom, joinInviteRoomHandler, node.AuthorizedRoute)
	proxy.Router().AddRouteHandler(getStats, getStatsHandler, node.AuthorizedRoute)

	// 调试路由
	proxy.Router().AddRouteHandler(greet, greetHandler)

	// 连接事件
	proxy.AddEventHandler(cluster.Connect, connectHandler)
	proxy.AddEventHandler(cluster.Disconnect, disconnectHandler)
}

// ============================ 请求/响应结构体 ============================

type greetReq struct {
	Message string `json:"message"`
}

type greetRes struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type loginReq struct {
	Token string `json:"token"`
}

type loginRes struct {
	Code         int     `json:"code"`
	UID          int64   `json:"uid,omitempty"`
	AccessToken  string  `json:"accessToken,omitempty"`
	RefreshToken string  `json:"refreshToken,omitempty"`
	Player       *Player `json:"player,omitempty"`
}

type reconnectReq struct {
	Token string `json:"token"`
}

type reconnectRes struct {
	Code int   `json:"code"`
	UID  int64 `json:"uid,omitempty"`
}

type refreshTokenReq struct {
	Token string `json:"token"`
}

type refreshTokenRes struct {
	Code         int    `json:"code"`
	AccessToken  string `json:"accessToken,omitempty"`
	RefreshToken string `json:"refreshToken,omitempty"`
}

// ---- 匹配相关 ----

type quickMatchReq struct {
	DeckID string `json:"deck_id"`
}

type quickMatchRes struct {
	Code         int                    `json:"code"`
	DSHost       string                 `json:"ds_host,omitempty"`
	BattlePort   int                    `json:"battle_port,omitempty"`
	RoomKey      string                 `json:"room_key,omitempty"`
	PlayerToken  string                 `json:"player_token,omitempty"`
	PlayerID     int                    `json:"player_id,omitempty"`
	OpponentUID  int64                  `json:"opponent_uid,omitempty"`
}

type spectateListRes struct {
	Code  int                  `json:"code"`
	Rooms []*match.RoomInfo     `json:"rooms,omitempty"`
}

type spectateJoinReq struct {
	RoomKey string `json:"room_key"`
}

type spectateJoinRes struct {
	Code       int    `json:"code"`
	DSHost     string `json:"ds_host,omitempty"`
	BattlePort int    `json:"battle_port,omitempty"`
	RoomKey    string `json:"room_key,omitempty"`
}

type createInviteRoomReq struct {
	DeckID string `json:"deck_id"`
}

type createInviteRoomRes struct {
	Code       int    `json:"code"`
	InviteCode string `json:"invite_code,omitempty"`
}

type joinInviteRoomReq struct {
	InviteCode string `json:"invite_code"`
	DeckID     string `json:"deck_id"`
}

type getStatsRes struct {
	Code   int `json:"code"`
	Wins   int `json:"wins,omitempty"`
	Losses int `json:"losses,omitempty"`
}

// ============================ 模拟用户数据 ============================

func getOrCreatePlayer(uid int64) *Player {
	if p, ok := players.Load(uid); ok {
		return p.(*Player)
	}
	p := &Player{
		UID:      uid,
		Nickname: fmt.Sprintf("Player_%d", uid),
		Level:    1,
	}
	players.Store(uid, p)
	return p
}

// ============================ 认证路由处理器 ============================

func greetHandler(ctx node.Context) {
	req := &greetReq{}
	res := &greetRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response greet failed: %v", err)
		}
	}()

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse greet request failed: %v", err)
		res.Code = codes.InternalError.Code()
		return
	}

	log.Info(req.Message)

	res.Code = codes.OK.Code()
	res.Message = fmt.Sprintf("I'm server, and the current time is: %s", xtime.Now().Format(xtime.DateTime))
}

func loginHandler(ctx node.Context) {
	req := &loginReq{}
	res := &loginRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response login failed: %v", err)
		}
	}()

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse login request failed: %v", err)
		res.Code = codes.InternalError.Code()
		return
	}

	claims, err := auth.ParseToken(req.Token)
	if err != nil {
		log.Warnf("token verification failed: %v", err)
		res.Code = codes.Unauthorized.Code()
		return
	}

	uid := claims.UID

	if err := ctx.BindGate(uid); err != nil {
		log.Errorf("bind gate failed: uid=%d, err=%v", uid, err)
		res.Code = codes.InternalError.Code()
		return
	}

	player := getOrCreatePlayer(uid)

	accessToken, refreshToken, err := auth.GenerateToken(uid)
	if err != nil {
		log.Errorf("generate token failed: uid=%d, err=%v", uid, err)
		res.Code = codes.InternalError.Code()
		return
	}

	log.Infof("player login success: uid=%d, nickname=%s", uid, player.Nickname)

	res.Code = codes.OK.Code()
	res.UID = uid
	res.AccessToken = accessToken
	res.RefreshToken = refreshToken
	res.Player = player
}

func reconnectHandler(ctx node.Context) {
	req := &reconnectReq{}
	res := &reconnectRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response reconnect failed: %v", err)
		}
	}()

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse reconnect request failed: %v", err)
		res.Code = codes.InternalError.Code()
		return
	}

	claims, err := auth.ParseToken(req.Token)
	if err != nil {
		log.Warnf("reconnect token invalid: %v", err)
		res.Code = codes.Unauthorized.Code()
		return
	}

	uid := claims.UID

	if err := ctx.BindGate(uid); err != nil {
		log.Errorf("reconnect bind gate failed: uid=%d, err=%v", uid, err)
		res.Code = codes.InternalError.Code()
		return
	}

	log.Infof("player reconnect success: uid=%d", uid)

	res.Code = codes.OK.Code()
	res.UID = uid
}

func logoutHandler(ctx node.Context) {
	res := &greetRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response logout failed: %v", err)
		}
	}()

	uid := ctx.UID()

	if err := ctx.UnbindGate(uid); err != nil {
		log.Errorf("unbind gate failed: uid=%d, err=%v", uid, err)
		res.Code = codes.InternalError.Code()
		return
	}

	log.Infof("player logout: uid=%d", uid)

	res.Code = codes.OK.Code()
	res.Message = "logout success"
}

func refreshTokenHandler(ctx node.Context) {
	req := &refreshTokenReq{}
	res := &refreshTokenRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response refresh token failed: %v", err)
		}
	}()

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse refresh token request failed: %v", err)
		res.Code = codes.InternalError.Code()
		return
	}

	claims, err := auth.ParseToken(req.Token)
	if err != nil {
		log.Warnf("refresh token invalid: %v", err)
		res.Code = codes.Unauthorized.Code()
		return
	}

	accessToken, refreshToken, err := auth.GenerateToken(claims.UID)
	if err != nil {
		log.Errorf("generate token failed: uid=%d, err=%v", claims.UID, err)
		res.Code = codes.InternalError.Code()
		return
	}

	log.Infof("token refreshed: uid=%d", claims.UID)

	res.Code = codes.OK.Code()
	res.AccessToken = accessToken
	res.RefreshToken = refreshToken
}

// ============================ 匹配路由处理器 ============================

// quickMatchHandler 快速匹配：入队 → 等待配对 → 返回 MatchReady 数据
func quickMatchHandler(ctx node.Context) {
	req := &quickMatchReq{}

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse quick match request failed: %v", err)
		ctx.Response(&quickMatchRes{Code: codes.InvalidArgument.Code()})
		return
	}

	uid := ctx.UID()
	gid := ctx.GID()

	// 匹配是阻塞操作（等待对手入队、调用 DS 建房）。路由 handler 在节点单线程的分发协程里
	// 同步执行，直接在此阻塞会冻结整个节点，导致第二个玩家的匹配请求永远无法被处理（死锁），
	// 因此将阻塞逻辑放入任务池异步执行，分发协程得以继续处理后续消息。
	ctx.Task(func(ctx node.Context) {
		res := &quickMatchRes{}
		defer func() {
			if err := ctx.Response(res); err != nil {
				log.Errorf("response quick match failed: %v", err)
			}
		}()

		// 匹配超时 30s
		matchCtx, cancel := context.WithTimeout(ctx.Context(), 30*time.Second)
		defer cancel()

		data, err := matchMgr.QueueQuickMatch(matchCtx, uid, gid, req.DeckID)
		if err != nil {
			log.Warnf("quick match failed: uid=%d, err=%v", uid, err)
			if errors.Is(err, context.Canceled) {
				// 被 cancel_match 路由或断线主动取消
				res.Code = codes.Canceled.Code()
			} else {
				res.Code = codes.InternalError.Code()
			}
			return
		}

		res.Code = codes.OK.Code()
		res.DSHost = data.DSHost
		res.BattlePort = data.BattlePort
		res.RoomKey = data.RoomKey
		res.PlayerToken = data.PlayerToken
		res.PlayerID = data.PlayerID
		res.OpponentUID = data.OpponentUID
	})
}

// cancelMatchHandler 取消匹配
func cancelMatchHandler(ctx node.Context) {
	res := &greetRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response cancel match failed: %v", err)
		}
	}()

	matchMgr.CancelMatch(ctx.UID())

	res.Code = codes.OK.Code()
	res.Message = "match cancelled"
}

// spectateListHandler 观战列表
func spectateListHandler(ctx node.Context) {
	res := &spectateListRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response spectate list failed: %v", err)
		}
	}()

	res.Code = codes.OK.Code()
	res.Rooms = matchMgr.SpectateList()
}

// spectateJoinHandler 加入观战
func spectateJoinHandler(ctx node.Context) {
	req := &spectateJoinReq{}
	res := &spectateJoinRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response spectate join failed: %v", err)
		}
	}()

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse spectate join request failed: %v", err)
		res.Code = codes.InvalidArgument.Code()
		return
	}

	data, err := matchMgr.SpectateJoin(req.RoomKey)
	if err != nil {
		log.Warnf("spectate join failed: room_key=%s, err=%v", req.RoomKey, err)
		res.Code = codes.NotFound.Code()
		return
	}

	res.Code = codes.OK.Code()
	res.DSHost = data.DSHost
	res.BattlePort = data.BattlePort
	res.RoomKey = data.RoomKey
}

// createInviteRoomHandler 创建邀请房
func createInviteRoomHandler(ctx node.Context) {
	req := &createInviteRoomReq{}
	res := &createInviteRoomRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response create invite room failed: %v", err)
		}
	}()

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse create invite room request failed: %v", err)
		res.Code = codes.InvalidArgument.Code()
		return
	}

	code := matchMgr.CreateInviteRoom(ctx.UID(), ctx.GID(), req.DeckID)

	res.Code = codes.OK.Code()
	res.InviteCode = code
}

// joinInviteRoomHandler 加入邀请房
func joinInviteRoomHandler(ctx node.Context) {
	req := &joinInviteRoomReq{}

	if err := ctx.Parse(req); err != nil {
		log.Errorf("parse join invite room request failed: %v", err)
		ctx.Response(&quickMatchRes{Code: codes.InvalidArgument.Code()})
		return
	}

	uid := ctx.UID()
	gid := ctx.GID()

	// JoinInviteRoom 内部会同步调用 DS 建房（网络 IO），同样放入任务池异步执行，
	// 避免阻塞节点单线程的分发协程。
	ctx.Task(func(ctx node.Context) {
		res := &quickMatchRes{}
		defer func() {
			if err := ctx.Response(res); err != nil {
				log.Errorf("response join invite room failed: %v", err)
			}
		}()

		data, err := matchMgr.JoinInviteRoom(ctx.Context(), uid, gid, req.InviteCode, req.DeckID)
		if err != nil {
			log.Warnf("join invite room failed: uid=%d, code=%s, err=%v", uid, req.InviteCode, err)
			res.Code = codes.NotFound.Code()
			return
		}

		res.Code = codes.OK.Code()
		res.DSHost = data.DSHost
		res.BattlePort = data.BattlePort
		res.RoomKey = data.RoomKey
		res.PlayerToken = data.PlayerToken
		res.PlayerID = data.PlayerID
		res.OpponentUID = data.OpponentUID
	})
}

// getStatsHandler 查询战绩
func getStatsHandler(ctx node.Context) {
	res := &getStatsRes{}
	defer func() {
		if err := ctx.Response(res); err != nil {
			log.Errorf("response get stats failed: %v", err)
		}
	}()

	record := matchMgr.GetStats(ctx.UID())

	res.Code = codes.OK.Code()
	res.Wins = record.Wins
	res.Losses = record.Losses
}

// ============================ 连接事件处理器 ============================

func connectHandler(ctx node.Context) {
	log.Infof("new connection: gid=%s, cid=%d, uid=%d", ctx.GID(), ctx.CID(), ctx.UID())
}

func disconnectHandler(ctx node.Context) {
	uid := ctx.UID()
	if uid != 0 {
		// 玩家断线时从匹配队列移除
		matchMgr.CancelMatch(uid)
		log.Infof("player disconnected: uid=%d, gid=%s, cid=%d", uid, ctx.GID(), ctx.CID())
	} else {
		log.Infof("anonymous connection closed: gid=%s, cid=%d", ctx.GID(), ctx.CID())
	}
}
