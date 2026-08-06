package match

import (
	"context"
	"fmt"
	"math/rand"
	"net"
	"sync"
	"time"

	"due/node/internal/dsclient"

	"github.com/dobyte/due/v2/cluster"
	"github.com/dobyte/due/v2/cluster/node"
	"github.com/dobyte/due/v2/log"
	"github.com/dobyte/due/v2/session"
)

const (
	matchTimeout       = 30 * time.Second // 匹配等待超时
	resultPollInterval = 2 * time.Second  // 结果轮询间隔
	dsBattlePort       = 7000             // DS 战斗端口
)

// playerEntry 快速匹配队列条目
type playerEntry struct {
	uid      int64
	gid      string
	deck     string
	resultCh chan *MatchReadyData // 配对结果通知通道
}

// Manager 匹配管理器
type Manager struct {
	proxy        *node.Proxy
	store        *PlayerStore
	dsClient     *dsclient.Client
	dsHost       string
	quickQueue   chan *playerEntry
	activeRooms  map[string]*ActiveRoom
	inviteRooms  map[string]*InviteRoom
	uidGIDMap    map[int64]string // uid → gid 缓存
	mu           sync.RWMutex
	ctx          context.Context
	cancel       context.CancelFunc
	matching     sync.Map // uid(int64) → true，防止同一用户重复匹配
	matchCancels sync.Map // uid(int64) → context.CancelFunc，排队中玩家匹配等待的取消函数
}

// NewManager 创建匹配管理器
// dsAddress：DUE → DS 控制连接地址（含端口，如 127.0.0.1:7001），仅用于 node 与 DS 之间的控制通信。
// dsExternalHost：对外（客户端）返回的 DS 主机地址（不含端口，如 100.68.33.127），
// 客户端按 ds_host:battle_port(7000) 连接战斗服。为空时回退为 dsAddress 的主机部分。
func NewManager(proxy *node.Proxy, dsAddress, dsExternalHost string) *Manager {
	ctx, cancel := context.WithCancel(context.Background())

	dsHost := dsExternalHost
	if dsHost == "" {
		if host, _, err := net.SplitHostPort(dsAddress); err == nil {
			dsHost = host
		} else {
			dsHost = dsAddress
		}
	}

	m := &Manager{
		proxy:       proxy,
		store:       NewPlayerStore(),
		dsClient:    dsclient.New(dsAddress),
		dsHost:      dsHost,
		quickQueue:  make(chan *playerEntry),
		activeRooms: make(map[string]*ActiveRoom),
		inviteRooms: make(map[string]*InviteRoom),
		uidGIDMap:   make(map[int64]string),
		ctx:         ctx,
		cancel:      cancel,
	}

	// 启动结果轮询
	go m.pollResults()
	// 定期清理过期邀请房
	go m.cleanupInviteRooms()

	log.Infof("match manager started, ds_control=%s, ds_external_host=%s", dsAddress, dsHost)
	return m
}

// Shutdown 关闭管理器
func (m *Manager) Shutdown() {
	m.cancel()
}

// ============================ 快速匹配 ============================

// MatchReadyData 匹配成功推送数据
type MatchReadyData struct {
	DSHost      string `json:"ds_host"`
	BattlePort  int    `json:"battle_port"`
	RoomKey     string `json:"room_key"`
	PlayerToken string `json:"player_token"`
	PlayerID    int    `json:"player_id"`
	OpponentUID int64  `json:"opponent_uid"`
}

// QueueQuickMatch 加入快速匹配队列
func (m *Manager) QueueQuickMatch(ctx context.Context, uid int64, gid, deck string) (*MatchReadyData, error) {
	// 同一用户不可重复入队
	if _, loaded := m.matching.LoadOrStore(uid, true); loaded {
		return nil, fmt.Errorf("player already in match queue")
	}
	defer m.matching.Delete(uid)

	// 支持外部取消（cancel_match 路由 / 玩家断线）：取消后等待中的 select 立即返回
	waitCtx, cancel := context.WithCancel(ctx)
	m.matchCancels.Store(uid, cancel)
	defer func() {
		cancel()
		m.matchCancels.Delete(uid)
	}()

	m.cacheGID(uid, gid)
	self := &playerEntry{uid: uid, gid: gid, deck: deck, resultCh: make(chan *MatchReadyData, 1)}

	// 尝试读对手（unbuffered channel，读到说明有人已在队列阻塞等待）
	select {
	case opponent := <-m.quickQueue:
		if opponent.uid == self.uid {
			// 同一用户多连接，放回对手并拒绝
			select {
			case m.quickQueue <- opponent:
			default:
			}
			return nil, fmt.Errorf("player already in match queue")
		}
		return m.doPairAndNotify(self, opponent)
	default:
	}

	// 无人等待，自己入队阻塞（unbuffered channel，会阻塞直到对手来读）
	select {
	case m.quickQueue <- self:
		log.Infof("player queued for quick match: uid=%d", uid)
	case <-waitCtx.Done():
		return nil, waitCtx.Err()
	}

	// 入队成功（已被对手取出），等待对手完成配对通知
	select {
	case data := <-self.resultCh:
		return data, nil
	case <-waitCtx.Done():
		log.Infof("player cancelled quick match: uid=%d", uid)
		return nil, waitCtx.Err()
	}
}

// doPairAndNotify 配对者执行配对并通知双方
func (m *Manager) doPairAndNotify(self, opponent *playerEntry) (*MatchReadyData, error) {
	data, err := m.doPair(self, opponent)
	if err != nil {
		return nil, err
	}

	// 匹配成功（2003）推送双方
	m.pushMatchReady(opponent.gid, opponent.uid, data, self.uid)
	m.pushMatchReady(self.gid, self.uid, data, opponent.uid)

	// 推结果给对手的 resultCh
	opponentData := m.buildOpponentData(data, self.uid)
	select {
	case opponent.resultCh <- opponentData:
	default:
	}

	return data, nil
}

// buildOpponentData 为对手构建 MatchReadyData
func (m *Manager) buildOpponentData(pairData *MatchReadyData, opponentUID int64) *MatchReadyData {
	m.mu.RLock()
	room, ok := m.activeRooms[pairData.RoomKey]
	m.mu.RUnlock()
	if !ok {
		return pairData
	}

	// 对手是 player 1
	return &MatchReadyData{
		DSHost:      pairData.DSHost,
		BattlePort:  pairData.BattlePort,
		RoomKey:     pairData.RoomKey,
		PlayerToken: room.Tokens[1],
		PlayerID:    1,
		OpponentUID: opponentUID,
	}
}

// CancelMatch 取消匹配
// 触发排队中玩家的匹配等待取消，使其立即退出匹配队列（不再等待对手）。
func (m *Manager) CancelMatch(uid int64) {
	if cancel, ok := m.matchCancels.Load(uid); ok {
		if fn, ok := cancel.(context.CancelFunc); ok {
			fn()
		}
	}
}

func (m *Manager) tryRemoveFromQueue(uid int64) {
	// unbuffered channel 不需要此操作，由 context 取消处理
}

// ============================ 邀请房 ============================

// CreateInviteRoom 创建邀请房
func (m *Manager) CreateInviteRoom(uid int64, gid, deck string) string {
	code := generateCode()
	m.mu.Lock()
	for {
		if _, ok := m.inviteRooms[code]; !ok {
			break
		}
		code = generateCode()
	}
	m.inviteRooms[code] = &InviteRoom{
		Code:       code,
		CreatorUID: uid,
		DeckID:     deck,
		CreatedAt:  time.Now(),
	}
	m.mu.Unlock()

	// 缓存创建者 gid，供加入者触发匹配后向创建者推送 MatchReady(2003) 使用
	m.cacheGID(uid, gid)

	log.Infof("invite room created: code=%s, uid=%d", code, uid)
	return code
}

// JoinInviteRoom 加入邀请房
func (m *Manager) JoinInviteRoom(ctx context.Context, joinerUID int64, joinerGID, code, joinerDeck string) (*MatchReadyData, error) {
	m.mu.Lock()
	invite, ok := m.inviteRooms[code]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("invite room not found")
	}
	delete(m.inviteRooms, code)
	m.mu.Unlock()

	creator := &playerEntry{uid: invite.CreatorUID, deck: invite.DeckID}
	joiner := &playerEntry{uid: joinerUID, gid: joinerGID, deck: joinerDeck}

	// 缓存双方 gid
	m.cacheGID(joinerUID, joinerGID)
	creator.gid = m.getCachedGID(creator.uid)

	data, err := m.doPair(creator, joiner)
	if err != nil {
		return nil, err
	}

	// 匹配成功（2003）推送双方
	if creator.gid != "" {
		m.pushMatchReady(creator.gid, creator.uid, data, joiner.uid)
	} else {
		log.Warnf("invite creator offline: uid=%d", creator.uid)
	}

	if joiner.gid != "" {
		m.pushMatchReady(joiner.gid, joiner.uid, data, creator.uid)
	} else {
		log.Warnf("invite joiner offline: uid=%d", joiner.uid)
	}

	log.Infof("invite room matched: code=%s, creator=%d, joiner=%d", code, creator.uid, joiner.uid)

	return data, nil
}

// ============================ 观战 ============================

type RoomInfo struct {
	RoomKey string `json:"room_key"`
	UID1    int64  `json:"uid1"`
	UID2    int64  `json:"uid2"`
}

func (m *Manager) SpectateList() []*RoomInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	list := make([]*RoomInfo, 0, len(m.activeRooms))
	for _, room := range m.activeRooms {
		list = append(list, &RoomInfo{
			RoomKey: room.RoomKey,
			UID1:    room.UIDs[0],
			UID2:    room.UIDs[1],
		})
	}
	return list
}

type SpectateJoinData struct {
	DSHost     string `json:"ds_host"`
	BattlePort int    `json:"battle_port"`
	RoomKey    string `json:"room_key"`
}

func (m *Manager) SpectateJoin(roomKey string) (*SpectateJoinData, error) {
	m.mu.RLock()
	room, ok := m.activeRooms[roomKey]
	m.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("room not found")
	}

	return &SpectateJoinData{
		DSHost:     room.DSHost,
		BattlePort: room.BattlePort,
		RoomKey:    room.RoomKey,
	}, nil
}

// ============================ 战绩 ============================

func (m *Manager) GetStats(uid int64) *Record {
	return m.store.GetRecord(uid)
}

// ============================ 内部方法 ============================

// doPair 执行配对：调用 DS create_room 创建房间
func (m *Manager) doPair(a, b *playerEntry) (*MatchReadyData, error) {
	decks := [2]string{a.deck, b.deck}
	metadata := map[string]any{
		"uid1": a.uid,
		"uid2": b.uid,
	}

	resp, err := m.dsClient.CreateRoom(decks, metadata)
	if err != nil {
		return nil, fmt.Errorf("ds create_room failed: %w", err)
	}

	if !resp.OK {
		return nil, fmt.Errorf("ds create_room returned ok=false")
	}

	room := &ActiveRoom{
		RoomKey:    resp.RoomKey,
		UIDs:       [2]int64{a.uid, b.uid},
		Tokens:     [2]string{resp.PlayerTokens[0], resp.PlayerTokens[1]},
		DSHost:     m.dsHost,
		BattlePort: resp.BattlePort,
	}

	m.mu.Lock()
	m.activeRooms[resp.RoomKey] = room
	m.mu.Unlock()

	log.Infof("match paired: room_key=%s, uid1=%d, uid2=%d, ds=%s:%d",
		resp.RoomKey, a.uid, b.uid, m.dsHost, resp.BattlePort)

	return &MatchReadyData{
		DSHost:      m.dsHost,
		BattlePort:  resp.BattlePort,
		RoomKey:     resp.RoomKey,
		PlayerToken: resp.PlayerTokens[0],
		PlayerID:    0,
		OpponentUID: b.uid,
	}, nil
}

// pushMatchReady 向指定玩家推送 MatchReady(2003) 消息
func (m *Manager) pushMatchReady(gid string, uid int64, pairData *MatchReadyData, opponentUID int64) {
	tokenIdx := 1
	playerID := 1

	m.mu.RLock()
	room, ok := m.activeRooms[pairData.RoomKey]
	m.mu.RUnlock()
	if !ok {
		log.Errorf("pushMatchReady: room not found, room_key=%s", pairData.RoomKey)
		return
	}

	// 判断该玩家是房间中的 player 0 还是 player 1
	if room.UIDs[0] == uid {
		tokenIdx = 0
		playerID = 0
	}

	m.pushMessage(gid, uid, 2003, &MatchReadyData{
		DSHost:      pairData.DSHost,
		BattlePort:  pairData.BattlePort,
		RoomKey:     pairData.RoomKey,
		PlayerToken: room.Tokens[tokenIdx],
		PlayerID:    playerID,
		OpponentUID: opponentUID,
	})
}

// ============================ 结果轮询 ============================

// MatchResultData 结算推送数据
type MatchResultData struct {
	RoomKey   string `json:"room_key"`
	WinnerUID int64  `json:"winner_uid"`
	LoserUID  int64  `json:"loser_uid"`
	Reason    string `json:"reason"`
}

// pollResults 后台结果轮询
func (m *Manager) pollResults() {
	ticker := time.NewTicker(resultPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.doPollResults()
		}
	}
}

func (m *Manager) doPollResults() {
	resp, err := m.dsClient.PullResults(true)
	if err != nil {
		log.Errorf("pull_results failed: %v", err)
		return
	}

	if !resp.OK || len(resp.Results) == 0 {
		return
	}

	for _, result := range resp.Results {
		m.handleResult(result)
	}
}

// handleResult 处理单个战斗结果
func (m *Manager) handleResult(result *dsclient.Result) {
	m.mu.Lock()
	room, ok := m.activeRooms[result.RoomKey]
	if ok {
		delete(m.activeRooms, result.RoomKey)
	}
	m.mu.Unlock()

	if !ok {
		log.Warnf("result for unknown room: room_key=%s", result.RoomKey)
		return
	}

	var winnerUID, loserUID int64

	// winner_id: 0 = player0, 1 = player1, -1 = 无效（平局等）
	switch result.WinnerID {
	case 0:
		winnerUID = room.UIDs[0]
		loserUID = room.UIDs[1]
	case 1:
		winnerUID = room.UIDs[1]
		loserUID = room.UIDs[0]
	default:
		// 平局或无效，不记录胜负
		log.Infof("match result invalid: room_key=%s, winner_id=%d, reason=%s",
			result.RoomKey, result.WinnerID, result.Reason)
		return
	}

	// 记录胜负
	m.store.RecordWin(winnerUID)
	m.store.RecordLoss(loserUID)

	// 推送结算给双方
	resultData := &MatchResultData{
		RoomKey:   result.RoomKey,
		WinnerUID: winnerUID,
		LoserUID:  loserUID,
		Reason:    result.Reason,
	}

	m.pushToUID(winnerUID, 2006, resultData)
	m.pushToUID(loserUID, 2006, resultData)

	log.Infof("match result: room_key=%s, winner=%d, loser=%d, reason=%s",
		result.RoomKey, winnerUID, loserUID, result.Reason)
}

// ============================ 清理 ============================

// cleanupInviteRooms 定期清理过期邀请房
func (m *Manager) cleanupInviteRooms() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-m.ctx.Done():
			return
		case <-ticker.C:
			m.mu.Lock()
			now := time.Now()
			for code, room := range m.inviteRooms {
				if now.Sub(room.CreatedAt) > 10*time.Minute {
					delete(m.inviteRooms, code)
					log.Infof("invite room expired: code=%s", code)
				}
			}
			m.mu.Unlock()
		}
	}
}

// ============================ GID 缓存 ============================

func (m *Manager) cacheGID(uid int64, gid string) {
	m.mu.Lock()
	m.uidGIDMap[uid] = gid
	m.mu.Unlock()
}

func (m *Manager) getCachedGID(uid int64) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.uidGIDMap[uid]
}

// ============================ 消息推送 ============================

func (m *Manager) pushMessage(gid string, uid int64, route int32, data any) {
	if gid == "" {
		log.Warnf("pushMessage: empty gid, uid=%d, route=%d", uid, route)
		return
	}

	if err := m.proxy.Push(context.Background(), &cluster.PushArgs{
		GID:    gid,
		Kind:   session.User,
		Target: uid,
		Message: &cluster.Message{
			Route: route,
			Data:  data,
		},
	}); err != nil {
		log.Errorf("pushMessage failed: uid=%d, route=%d, err=%v", uid, route, err)
	}
}

func (m *Manager) pushToUID(uid int64, route int32, data any) {
	gid := m.getCachedGID(uid)
	if gid == "" {
		log.Warnf("pushToUID: unknown gid, uid=%d, route=%d", uid, route)
		return
	}
	m.pushMessage(gid, uid, route, data)
}

// ============================ 邀请码 ============================

func generateCode() string {
	const charset = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	code := make([]byte, 6)
	for i := range code {
		code[i] = charset[rand.Intn(len(charset))]
	}
	return string(code)
}
