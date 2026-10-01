package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"noxa/internal/netproto"
)

// lockConversation also serializes history reads with membership removal. All
// callers acquire the parent row before touching membership or message rows.
func lockConversation(ctx context.Context, tx *sql.Tx, id string, write bool) (netproto.Conversation, error) {
	c := netproto.Conversation{Members: []netproto.ConversationMember{}}
	lock := " FOR SHARE"
	if write {
		lock = " FOR UPDATE"
	}
	err := tx.QueryRowContext(ctx, `SELECT id::text,name,owner_uid,revision,epoch FROM private_conversations WHERE id=$1`+lock, id).Scan(&c.ID, &c.Name, &c.Owner, &c.Revision, &c.Epoch)
	if err != nil {
		return c, err
	}
	rows, err := tx.QueryContext(ctx, `SELECT unique_id,pending,joined_epoch FROM private_conversation_members WHERE conversation_id=$1 ORDER BY unique_id`, id)
	if err != nil {
		return c, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var member netproto.ConversationMember
		if err := rows.Scan(&member.UniqueID, &member.Pending, &member.JoinedEpoch); err != nil {
			return c, err
		}
		c.Members = append(c.Members, member)
	}
	return c, rows.Err()
}

// A user's row serializes group quota checks across different conversations.
func conversationQuota(ctx context.Context, tx *sql.Tx, uid string) error {
	var id int64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE unique_id=$1 FOR NO KEY UPDATE`, uid).Scan(&id); err != nil {
		return netproto.ErrConversationDenied
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM private_conversation_members WHERE unique_id=$1`, uid).Scan(&count); err != nil {
		return err
	}
	if count >= 64 {
		return netproto.ErrConversationInvalid
	}
	return nil
}

func (s *Store) CreateConversation(ctx context.Context, uid, name string) (_ netproto.Conversation, retErr error) {
	var c netproto.Conversation
	if uid == "" || !netproto.ValidConversationName(name) {
		return c, netproto.ErrConversationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return c, err
	}
	defer rollbackRoleTx(tx, &retErr)
	if err := conversationQuota(ctx, tx, uid); err != nil {
		return c, err
	}
	c.Name, c.Owner, c.Revision, c.Epoch = strings.TrimSpace(name), uid, 1, 1
	if err := tx.QueryRowContext(ctx, `INSERT INTO private_conversations(name,owner_uid) VALUES($1,$2) RETURNING id::text`, c.Name, uid).Scan(&c.ID); err != nil {
		return c, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO private_conversation_members(conversation_id,unique_id,pending,joined_epoch) VALUES($1,$2,FALSE,1)`, c.ID, uid); err != nil {
		return c, err
	}
	c.Members = []netproto.ConversationMember{{UniqueID: uid, JoinedEpoch: 1}}
	return c, tx.Commit()
}

func (s *Store) ChangeConversation(ctx context.Context, uid string, request netproto.ConversationRequest) (_ []string, retErr error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer rollbackRoleTx(tx, &retErr)
	current, err := lockConversation(ctx, tx, request.ID, true)
	if err != nil {
		return nil, err
	}
	next, err := netproto.ChangeConversation(current, uid, request)
	if err != nil {
		return nil, err
	}
	if request.Action == "invite" {
		if err := conversationQuota(ctx, tx, request.Target); err != nil {
			return nil, err
		}
	}
	recipients := make([]string, 0, len(current.Members)+1)
	for _, member := range current.Members {
		recipients = append(recipients, member.UniqueID)
	}
	if request.Action == "invite" {
		recipients = append(recipients, request.Target)
	}
	if len(next.Members) == 0 {
		_, err = tx.ExecContext(ctx, `DELETE FROM private_conversations WHERE id=$1`, current.ID)
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE private_conversations SET name=$2,owner_uid=$3,revision=$4,epoch=$5 WHERE id=$1`, next.ID, next.Name, next.Owner, next.Revision, next.Epoch)
		for _, member := range current.Members {
			if _, retained := next.Member(member.UniqueID); retained || err != nil {
				continue
			}
			_, err = tx.ExecContext(ctx, `DELETE FROM private_conversation_members WHERE conversation_id=$1 AND unique_id=$2`, next.ID, member.UniqueID)
		}
		for _, member := range next.Members {
			if err != nil {
				break
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO private_conversation_members(conversation_id,unique_id,pending,joined_epoch) VALUES($1,$2,$3,$4) ON CONFLICT(conversation_id,unique_id) DO UPDATE SET pending=EXCLUDED.pending,joined_epoch=EXCLUDED.joined_epoch`, next.ID, member.UniqueID, member.Pending, member.JoinedEpoch)
		}
	}
	if err != nil {
		return nil, err
	}
	return recipients, tx.Commit()
}

// WithConversations holds membership locks until the bounded socket write
// completes. A successful leave/remove cannot be followed by a stale page.
func (s *Store) WithConversations(ctx context.Context, uid string, request netproto.ConversationRequest, deliver func(netproto.ConversationResult) error) (retErr error) {
	// Do not let database/sql automatically release the membership lock when
	// the query deadline expires while a bounded network delivery is finishing.
	// Queries still use ctx; rollback/commit below owns the transaction lifetime.
	connection, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = connection.Close() }()
	tx, err := connection.BeginTx(context.WithoutCancel(ctx), nil)
	if err != nil {
		return err
	}
	defer rollbackRoleTx(tx, &retErr)
	ids := []string{request.ID}
	if request.Action == "list" {
		ids = nil
		rows, err := tx.QueryContext(ctx, `SELECT conversation_id::text FROM private_conversation_members WHERE unique_id=$1 ORDER BY conversation_id LIMIT 64`, uid)
		if err != nil {
			return err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				_ = rows.Close()
				return err
			}
			ids = append(ids, id)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	result := netproto.ConversationResult{Action: request.Action, Conversations: []netproto.Conversation{}, Messages: []netproto.ConversationMessage{}}
	for _, id := range ids {
		c, err := lockConversation(ctx, tx, id, false)
		if request.Action == "list" && errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		member, found := c.Member(uid)
		if !found {
			if request.Action == "list" {
				continue
			}
			return netproto.ErrConversationDenied
		}
		if !member.Pending {
			if err := conversationReadState(ctx, tx, &c, uid, member.JoinedEpoch); err != nil {
				return err
			}
		}
		result.Conversations = append(result.Conversations, c)
		if request.Action != "history" {
			continue
		}
		if member.Pending {
			return netproto.ErrConversationDenied
		}
		rows, err := tx.QueryContext(ctx, `SELECT m.id,m.epoch,m.from_uid,m.client_reference,e.ciphertext,extract(epoch FROM m.created_at)::bigint FROM private_conversation_messages m JOIN private_conversation_envelopes e ON e.message_id=m.id AND e.recipient_uid=$2 WHERE m.conversation_id=$1 AND m.epoch >= $3 AND ($4::bigint=0 OR m.id<$4) ORDER BY m.id DESC LIMIT 25`, id, uid, member.JoinedEpoch, request.BeforeID)
		if err != nil {
			return err
		}
		for rows.Next() {
			message := netproto.ConversationMessage{ConversationID: id}
			if err := rows.Scan(&message.ID, &message.Epoch, &message.FromUniqueID, &message.Reference, &message.Body, &message.CreatedAt); err != nil {
				_ = rows.Close()
				return err
			}
			result.Messages = append(result.Messages, message)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return err
		}
	}
	if err := deliver(result); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SendConversation(ctx context.Context, uid string, request netproto.ConversationRequest) (_ int64, _ []string, retErr error) {
	if request.Message == nil {
		return 0, nil, netproto.ErrConversationInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer rollbackRoleTx(tx, &retErr)
	c, err := lockConversation(ctx, tx, request.ID, true)
	if err != nil {
		return 0, nil, err
	}
	if request.Revision != c.Revision {
		return 0, nil, netproto.ErrConversationConflict
	}
	if err := request.Message.Validate(c, uid); err != nil {
		return 0, nil, err
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO private_conversation_messages(conversation_id,epoch,from_uid,client_reference) VALUES($1,$2,$3,$4) ON CONFLICT(conversation_id,from_uid,client_reference) DO NOTHING RETURNING id`, c.ID, c.Epoch, uid, request.Message.Reference).Scan(&id)
	inserted := err == nil
	if errors.Is(err, sql.ErrNoRows) {
		err = tx.QueryRowContext(ctx, `SELECT id FROM private_conversation_messages WHERE conversation_id=$1 AND from_uid=$2 AND client_reference=$3`, c.ID, uid, request.Message.Reference).Scan(&id)
	}
	if err != nil {
		return 0, nil, err
	}
	recipients := make([]string, 0, len(request.Message.Envelopes))
	for _, member := range c.Members {
		if member.Pending {
			continue
		}
		recipients = append(recipients, member.UniqueID)
		if inserted {
			if _, err := tx.ExecContext(ctx, `INSERT INTO private_conversation_envelopes(message_id,recipient_uid,ciphertext) VALUES($1,$2,$3)`, id, member.UniqueID, request.Message.Envelopes[member.UniqueID]); err != nil {
				return 0, nil, err
			}
		}
	}
	return id, recipients, tx.Commit()
}
