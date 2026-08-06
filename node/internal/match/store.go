package match

import "sync"

// Record 胜负记录
type Record struct {
	Wins   int `json:"wins"`
	Losses int `json:"losses"`
}

// PlayerStore 玩家战绩存储（内存实现）
type PlayerStore struct {
	mu   sync.RWMutex
	data map[int64]*Record
}

// NewPlayerStore 创建玩家战绩存储
func NewPlayerStore() *PlayerStore {
	return &PlayerStore{
		data: make(map[int64]*Record),
	}
}

// RecordWin 记录胜利
func (s *PlayerStore) RecordWin(uid int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getOrCreate(uid).Wins++
}

// RecordLoss 记录失败
func (s *PlayerStore) RecordLoss(uid int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.getOrCreate(uid).Losses++
}

// GetRecord 获取战绩
func (s *PlayerStore) GetRecord(uid int64) *Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if r, ok := s.data[uid]; ok {
		return &Record{Wins: r.Wins, Losses: r.Losses}
	}
	return &Record{}
}

func (s *PlayerStore) getOrCreate(uid int64) *Record {
	if r, ok := s.data[uid]; ok {
		return r
	}
	r := &Record{}
	s.data[uid] = r
	return r
}
