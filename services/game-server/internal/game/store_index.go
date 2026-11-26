package game

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// memIndex is MemStore's live-game index. Keys are table, partition, game id.
type memIndex struct {
	rows map[IndexTable]map[string]map[string]memRow
}

type memRow struct {
	entry UserEntry
	live  LiveEntry
	exp   time.Time
}

var _ IndexedStore = (*MemStore)(nil)

func (m *MemStore) idx() *memIndex {
	if m.index == nil {
		m.index = &memIndex{rows: map[IndexTable]map[string]map[string]memRow{}}
	}
	return m.index
}

func (x *memIndex) part(t IndexTable, key string) map[string]memRow {
	if x.rows[t] == nil {
		x.rows[t] = map[string]map[string]memRow{}
	}
	if x.rows[t][key] == nil {
		x.rows[t][key] = map[string]memRow{}
	}
	return x.rows[t][key]
}

// AppendIndexed commits evs and ops atomically: the seq check and the Update
// existence check both pass before anything is written.
func (m *MemStore) AppendIndexed(ctx context.Context, streamID string, expectedSeq uint32, evs []Event, ops []IndexOp) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("memstore: context: %w", err)
	}
	if len(evs) == 0 {
		return fmt.Errorf("memstore: AppendIndexed needs at least one event")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failAppend != nil {
		err := m.failAppend
		m.failAppend = nil
		return fmt.Errorf("memstore: append-indexed failed (not committed): %w", err)
	}
	if int(expectedSeq) != len(m.streams[streamID]) {
		return fmt.Errorf("memstore: indexed expected seq %d, next is %d: %w",
			expectedSeq, len(m.streams[streamID]), ErrSeqConflict)
	}
	x := m.idx()
	for _, op := range ops {
		if op.Kind == IndexUpdate {
			if _, ok := x.part(op.Table, op.Key)[op.GameID]; !ok {
				return fmt.Errorf("memstore: %s/%s/%s: %w", op.Table, op.Key, op.GameID, ErrIndexMissing)
			}
		}
	}
	m.streams[streamID] = append(m.streams[streamID], evs...)
	for _, op := range ops {
		p := x.part(op.Table, op.Key)
		switch op.Kind {
		case IndexPut:
			p[op.GameID] = memRow{
				entry: UserEntry{GameID: op.GameID, Seat: op.Seat, EndedAt: op.EndedAt},
				live:  LiveEntry{GameID: op.GameID, OwnerNode: op.OwnerNode, OwnerIncarnation: op.OwnerIncarnation},
				exp:   op.ExpiresAt,
			}
		case IndexDelete:
			delete(p, op.GameID)
		case IndexUpdate:
			r := p[op.GameID]
			r.live.OwnerNode, r.live.OwnerIncarnation = op.OwnerNode, op.OwnerIncarnation
			p[op.GameID] = r
		}
	}
	return nil
}

// LiveGames returns the shard's entries in game id order.
func (m *MemStore) LiveGames(ctx context.Context, shard int) ([]LiveEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: context: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []LiveEntry
	for _, r := range m.idx().part(IdxLive, ShardKey(shard)) {
		out = append(out, r.live)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID < out[j].GameID })
	return out, nil
}

func (m *MemStore) userRows(t IndexTable, userID string, limit int) []UserEntry {
	var out []UserEntry
	for _, r := range m.idx().part(t, userID) {
		if !r.exp.IsZero() && !r.exp.After(time.Now()) {
			continue
		}
		out = append(out, r.entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GameID > out[j].GameID })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}

// UserLiveGames returns the user's live games, newest game id first.
func (m *MemStore) UserLiveGames(ctx context.Context, userID string, limit int) ([]UserEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: context: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.userRows(IdxUserLive, userID, limit), nil
}

// UserRecentGames returns the user's unexpired recently ended games.
func (m *MemStore) UserRecentGames(ctx context.Context, userID string, limit int) ([]UserEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("memstore: context: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.userRows(IdxUserRecent, userID, limit), nil
}
