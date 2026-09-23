package store

import (
	"context"
	"database/sql"

	"noxa/internal/netproto"
)

// conversationReadState is requester-only metadata, obtained under the same
// membership lease as the conversation. Read pointers are never in the roster.
func conversationReadState(ctx context.Context, tx *sql.Tx, c *netproto.Conversation, uid string, joinedEpoch int64) error {
	if err := tx.QueryRowContext(ctx, `SELECT read_message_id FROM private_conversation_members WHERE conversation_id=$1 AND unique_id=$2`, c.ID, uid).Scan(&c.ReadMessageID); err != nil {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE((SELECT m.id FROM private_conversation_messages m JOIN private_conversation_envelopes e ON e.message_id=m.id AND e.recipient_uid=$2 WHERE m.conversation_id=$1 AND m.epoch >= $3 ORDER BY m.id DESC LIMIT 1),0)`, c.ID, uid, joinedEpoch).Scan(&c.LatestMessageID); err != nil {
		return err
	}
	return tx.QueryRowContext(ctx, `SELECT count(*) FROM private_conversation_messages m JOIN private_conversation_envelopes e ON e.message_id=m.id AND e.recipient_uid=$2 WHERE m.conversation_id=$1 AND m.epoch >= $3 AND m.id>$4 AND m.from_uid<>$2`, c.ID, uid, joinedEpoch, c.ReadMessageID).Scan(&c.UnreadCount)
}

// MarkConversationRead advances only the authenticated member's durable cursor.
// The parent lock serializes this operation with sends and membership changes.
func (s *Store) MarkConversationRead(ctx context.Context, uid, id string, messageID int64) (retErr error) {
	if messageID <= 0 {
		return netproto.ErrConversationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer rollbackRoleTx(tx, &retErr)
	c, err := lockConversation(ctx, tx, id, true)
	if err != nil {
		return err
	}
	member, found := c.Member(uid)
	if !found || member.Pending {
		return netproto.ErrConversationDenied
	}
	var accessible bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM private_conversation_messages m JOIN private_conversation_envelopes e ON e.message_id=m.id AND e.recipient_uid=$2 WHERE m.conversation_id=$1 AND m.id=$3 AND m.epoch >= $4)`, id, uid, messageID, member.JoinedEpoch).Scan(&accessible); err != nil {
		return err
	}
	if !accessible {
		return netproto.ErrConversationInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE private_conversation_members SET read_message_id=GREATEST(read_message_id,$3) WHERE conversation_id=$1 AND unique_id=$2`, id, uid, messageID); err != nil {
		return err
	}
	return tx.Commit()
}
