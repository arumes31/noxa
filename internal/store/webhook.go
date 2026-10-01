package store

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"errors"
	"noxa/internal/netproto"
	"strconv"
	"time"
)

var ErrWebhookDenied = errors.New("webhook unavailable")
var ErrWebhookRate = errors.New("webhook rate limit exceeded")

type WebhookIdentity struct {
	ID, ChannelID, CreatorID int64
	CreatorUID, Name         string
}

func (s *Store) ManageWebhook(ctx context.Context, r netproto.WebhookRequest, creator int64, hash []byte) (_ netproto.WebhookResult, retErr error) {
	out := netproto.WebhookResult{Action: r.Action, ChannelID: r.ChannelID, Hooks: []netproto.WebhookEntry{}}
	if !r.Valid() {
		return out, ErrWebhookDenied
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer rollbackRoleTx(tx, &retErr)
	switch r.Action {
	case "create":
		// Serialize channel quotas without holding a process-wide lock.
		var channel int64
		if err = tx.QueryRowContext(ctx, `SELECT id FROM channels WHERE id=$1 FOR UPDATE`, r.ChannelID).Scan(&channel); err != nil {
			return out, err
		}
		var count int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM incoming_webhooks WHERE channel_id=$1`, r.ChannelID).Scan(&count); err != nil {
			return out, err
		}
		if count >= 20 || len(hash) != 32 {
			return out, ErrWebhookDenied
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO incoming_webhooks(channel_id,creator_id,name,token_hash) VALUES($1,$2,$3,$4) RETURNING id`, r.ChannelID, creator, r.Name, hash).Scan(&out.ID)
	case "revoke":
		_, err = tx.ExecContext(ctx, `DELETE FROM incoming_webhooks WHERE id=$1 AND channel_id=$2`, r.ID, r.ChannelID)
	}
	if err != nil {
		return out, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT id,channel_id,name,extract(epoch FROM created_at)::bigint FROM incoming_webhooks WHERE channel_id=$1 ORDER BY id DESC LIMIT 20`, r.ChannelID)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var entry netproto.WebhookEntry
		if err = rows.Scan(&entry.ID, &entry.ChannelID, &entry.Name, &entry.CreatedAt); err != nil {
			closeRows(rows)
			return out, err
		}
		out.Hooks = append(out.Hooks, entry)
	}
	err = rows.Err()
	closeRows(rows)
	if err != nil {
		return out, err
	}
	return out, tx.Commit()
}

// DeliverWebhook serializes post/revoke and rate consumption on the capability
// row. The caller already holds current policy and admission locks. Only the
// encrypted result of authorize is inserted, in the same transaction.
func (s *Store) DeliverWebhook(ctx context.Context, id int64, hash []byte, authorize func(WebhookIdentity) (string, uint32, error)) (_ WebhookIdentity, messageID int64, body string, keyID uint32, retErr error) {
	var hook WebhookIdentity
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return hook, 0, "", 0, err
	}
	defer rollbackRoleTx(tx, &retErr)
	var storedHash []byte
	var reset time.Time
	var count int
	err = tx.QueryRowContext(ctx, `SELECT h.id,h.channel_id,h.creator_id,u.unique_id,h.name,h.token_hash,h.rate_reset,h.rate_count FROM incoming_webhooks h JOIN users u ON u.id=h.creator_id WHERE h.id=$1 FOR UPDATE OF h`, id).Scan(&hook.ID, &hook.ChannelID, &hook.CreatorID, &hook.CreatorUID, &hook.Name, &storedHash, &reset, &count)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && subtle.ConstantTimeCompare(storedHash, hash) != 1) {
		return hook, 0, "", 0, ErrWebhookDenied
	}
	if err != nil {
		return hook, 0, "", 0, err
	}
	now := time.Now()
	if now.After(reset) {
		count = 0
		reset = now.Add(3 * time.Second)
	}
	if count >= 5 {
		return hook, 0, "", 0, ErrWebhookRate
	}
	body, keyID, err = authorize(hook)
	if err != nil {
		return hook, 0, "", 0, err
	}
	if body == "" || keyID == 0 {
		return hook, 0, "", 0, ErrWebhookDenied
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO chat_messages(scope,channel_id,from_unique_id,from_nickname,body_enc,key_id) VALUES(1,$1,$2,$3,$4,$5) RETURNING id`, hook.ChannelID, "webhook:"+strconv.FormatInt(hook.ID, 10), hook.Name+" [Webhook]", body, keyID).Scan(&messageID)
	if err != nil {
		return hook, 0, "", 0, err
	}
	_, err = tx.ExecContext(ctx, `UPDATE incoming_webhooks SET rate_reset=$2,rate_count=$3 WHERE id=$1`, id, reset, count+1)
	if err != nil {
		return hook, 0, "", 0, err
	}
	return hook, messageID, body, keyID, tx.Commit()
}
