package gameclient

// 路由号与 node 服务器保持一致（见 node/main.go）
const (
	RouteGreet            = 1    // 问候测试
	RouteLogin            = 1001 // Token 登录
	RouteReconnect        = 1002 // 断线重连
	RouteLogout           = 1003 // 主动登出
	RouteRefreshToken     = 1004 // 刷新令牌
	RouteQuickMatch       = 2001 // 快速匹配
	RouteCancelMatch      = 2002 // 取消匹配
	RouteMatchReady       = 2003 // 匹配成功（S→C 推送）
	RouteSpectateList     = 2004 // 观战列表
	RouteSpectateJoin     = 2005 // 加入观战
	RouteMatchResult      = 2006 // 结算通知（S→C 推送）
	RouteSpectateRooms    = 2007 // 观战房间列表（S→C 推送）
	RouteCreateInviteRoom = 2008 // 创建邀请房
	RouteJoinInviteRoom   = 2009 // 加入邀请房
	RouteInviteCreated    = 2010 // 邀请房已创建（S→C 推送）
	RouteGetStats         = 2011 // 查询战绩
	RouteStatsResult      = 2012 // 战绩返回（S→C 推送）
)

// 请求/响应结构体（与 node/main.go 保持一致）

type LoginReq struct {
	Token string `json:"token"`
}

type LoginRes struct {
	Code         int     `json:"code"`
	UID          int64   `json:"uid,omitempty"`
	AccessToken  string  `json:"accessToken,omitempty"`
	RefreshToken string  `json:"refreshToken,omitempty"`
	Player       *Player `json:"player,omitempty"`
}

type Player struct {
	UID      int64  `json:"uid"`
	Nickname string `json:"nickname"`
	Level    int    `json:"level"`
}

// MatchReadyData 匹配成功数据（2001 响应 / 2003 推送 / 2009 响应共用）
type MatchReadyData struct {
	Code        int    `json:"code"`
	DSHost      string `json:"ds_host,omitempty"`
	BattlePort  int    `json:"battle_port,omitempty"`
	RoomKey     string `json:"room_key,omitempty"`
	PlayerToken string `json:"player_token,omitempty"`
	PlayerID    int    `json:"player_id,omitempty"`
	OpponentUID int64  `json:"opponent_uid,omitempty"`
}

type QuickMatchReq struct {
	DeckID string `json:"deck_id"`
}

type CreateInviteRoomReq struct {
	DeckID string `json:"deck_id"`
}

type JoinInviteRoomReq struct {
	InviteCode string `json:"invite_code"`
	DeckID     string `json:"deck_id"`
}

type SpectateJoinReq struct {
	RoomKey string `json:"room_key"`
}

type GreetReq struct {
	Message string `json:"message"`
}
