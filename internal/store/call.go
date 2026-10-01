package store

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/lib/pq"
	"noxa/internal/netproto"
)

func (s *Store) SavePrivateCall(ctx context.Context, call netproto.CallSession) error {
	return savePrivateCall(ctx, s.db, call)
}

func savePrivateCall(ctx context.Context, db interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, call netproto.CallSession) error {
	data, err := json.Marshal(call)
	if err != nil {
		return err
	}
	uids := make([]string, 0, len(call.Participants))
	for _, participant := range call.Participants {
		uids = append(uids, participant.UniqueID)
	}
	_, err = db.ExecContext(ctx, `INSERT INTO private_call_history(call_id,revision,created_at,ended_at,participant_uids,state) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(call_id) DO UPDATE SET revision=excluded.revision,ended_at=excluded.ended_at,state=excluded.state WHERE private_call_history.revision < excluded.revision`, call.ID, call.Revision, call.CreatedAt, call.EndedAt, pq.Array(uids), data)
	return err
}

func (s *Store) StartPrivateGroupCall(ctx context.Context, uid, groupID string, build func(netproto.Conversation) (netproto.CallSession, error)) (_ netproto.CallSession, retErr error) {
	var call netproto.CallSession
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return call, err
	}
	defer rollbackRoleTx(tx, &retErr)
	group, err := lockConversation(ctx, tx, groupID, false)
	if err != nil {
		return call, err
	}
	member, found := group.Member(uid)
	if !found || member.Pending {
		return call, netproto.ErrConversationDenied
	}
	call, err = build(group)
	if err != nil {
		return call, err
	}
	if err := savePrivateCall(ctx, tx, call); err != nil {
		return call, err
	}
	return call, tx.Commit()
}

func (s *Store) PrivateCallHistory(ctx context.Context, uid string) ([]netproto.CallSession, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT state FROM private_call_history WHERE participant_uids @> ARRAY[$1]::text[] ORDER BY created_at DESC,call_id DESC LIMIT 50`, uid)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := []netproto.CallSession{}
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var call netproto.CallSession
		if err := json.Unmarshal(data, &call); err != nil {
			return nil, err
		}
		result = append(result, call)
	}
	return result, rows.Err()
}
