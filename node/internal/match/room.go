package match

import "time"

// ActiveRoom 进行中的对战房间
type ActiveRoom struct {
	RoomKey    string   // DS 房间 key
	UIDs       [2]int64 // 双方 uid
	Tokens     [2]string // 双方 player_tokens
	DSHost     string   // DS 服务器地址
	BattlePort int      // DS 战斗端口
}

// GetPlayerID 获取玩家在房间中的编号（0 或 1）
func (r *ActiveRoom) GetPlayerID(uid int64) int {
	if r.UIDs[0] == uid {
		return 0
	}
	return 1
}

// OpponentUID 获取对手 uid
func (r *ActiveRoom) OpponentUID(uid int64) int64 {
	if r.UIDs[0] == uid {
		return r.UIDs[1]
	}
	return r.UIDs[0]
}

// InviteRoom 邀请房
type InviteRoom struct {
	Code       string    // 6 位随机码
	CreatorUID int64
	DeckID     string
	CreatedAt  time.Time // 创建时间（用于过期清理）
}
