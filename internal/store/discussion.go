package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"

	"github.com/lib/pq"
	"noxa/internal/netproto"
)

// DiscussionFollowers resolves only online identities, keeping notification
// fanout bounded by the server's current connection count.
func (s *Store) DiscussionFollowers(ctx context.Context, threadID int64, online []string) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT unique_id FROM discussion_members WHERE thread_id=$1 AND subscribed AND unique_id=ANY($2::text[])`, threadID, pq.Array(online))
	if err != nil {
		return nil, err
	}
	defer closeRows(rows)
	result := map[string]bool{}
	for rows.Next() {
		var uid string
		if err = rows.Scan(&uid); err != nil {
			return nil, err
		}
		result[uid] = true
	}
	return result, rows.Err()
}

// Discussion executes a channel-scoped operation. Authorization is the server's
// responsibility; row locks serialize sending with archive and membership changes.
func (s *Store) Discussion(ctx context.Context, r netproto.DiscussionRequest, uid, nickname string) (_ netproto.DiscussionResult, retErr error) {
	out := netproto.DiscussionResult{Action: r.Action, ChannelID: r.ChannelID, ThreadID: r.ThreadID, Tags: []string{}, Threads: []netproto.DiscussionThread{}, Messages: []netproto.ChatHistoryEntry{}}
	if err := r.Validate(); err != nil {
		return out, err
	}
	if r.Tags == nil {
		r.Tags = []string{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return out, err
	}
	defer rollbackRoleTx(tx, &retErr)
	var tags pq.StringArray
	err = tx.QueryRowContext(ctx, `SELECT forum,tags,auto_archive_hours FROM discussion_channels WHERE channel_id=$1`, r.ChannelID).Scan(&out.Forum, &tags, &out.AutoArchiveHours)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return out, err
	}
	out.Tags = append(out.Tags, tags...)
	if r.Action == "configure" {
		_, err = tx.ExecContext(ctx, `INSERT INTO discussion_channels(channel_id,forum,tags,auto_archive_hours) VALUES($1,$2,$3,$4) ON CONFLICT(channel_id) DO UPDATE SET forum=excluded.forum,tags=excluded.tags,auto_archive_hours=excluded.auto_archive_hours`, r.ChannelID, r.Forum, pq.Array(r.Tags), r.AutoArchiveHours)
		if err != nil {
			return out, err
		}
		out.Forum, out.Tags = r.Forum, r.Tags
		out.AutoArchiveHours = r.AutoArchiveHours
	}
	if out.AutoArchiveHours > 0 {
		_, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET archived=TRUE WHERE channel_id=$1 AND deleted_at IS NULL AND NOT archived AND last_activity_at <= NOW()-($2 * INTERVAL '1 hour')`, r.ChannelID, out.AutoArchiveHours)
		if err != nil {
			return out, err
		}
	}
	if r.Action == "create" {
		for _, tag := range r.Tags {
			if !slices.Contains(out.Tags, tag) {
				return out, netproto.ErrDiscussionInvalid
			}
		}
		if r.RootMessageID > 0 {
			var exists bool
			if err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM chat_messages WHERE id=$1 AND channel_id=$2 AND deleted_at IS NULL)`, r.RootMessageID, r.ChannelID).Scan(&exists); err != nil {
				return out, err
			}
			if !exists {
				return out, sql.ErrNoRows
			}
		}
		err = tx.QueryRowContext(ctx, `INSERT INTO discussion_threads(channel_id,root_message_id,title,tags,author,request_id) VALUES($1,NULLIF($2,0),$3,$4,$5,$6) ON CONFLICT(channel_id,author,request_id) DO UPDATE SET request_id=excluded.request_id RETURNING id`, r.ChannelID, r.RootMessageID, r.Title, pq.Array(r.Tags), uid, r.RequestID).Scan(&out.ThreadID)
		if err != nil {
			return out, err
		}
		r.ThreadID = out.ThreadID
		_, err = tx.ExecContext(ctx, `INSERT INTO discussion_members(thread_id,unique_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.ThreadID, uid)
		if err != nil {
			return out, err
		}
	}
	if r.ThreadID > 0 {
		var archived bool
		err = tx.QueryRowContext(ctx, `SELECT archived FROM discussion_threads WHERE id=$1 AND channel_id=$2 AND deleted_at IS NULL FOR UPDATE`, r.ThreadID, r.ChannelID).Scan(&archived)
		if err != nil {
			return out, err
		}
		switch r.Action {
		case "delete":
			if _, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET deleted_at=NOW(),title='',tags='{}',root_message_id=NULL,pinned=FALSE,resolved=FALSE,archived=TRUE WHERE id=$1`, r.ThreadID); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE discussion_messages SET body_enc='',deleted_at=COALESCE(deleted_at,NOW()) WHERE thread_id=$1`, r.ThreadID); err != nil {
				return out, err
			}
			if _, err = tx.ExecContext(ctx, `DELETE FROM discussion_members WHERE thread_id=$1`, r.ThreadID); err != nil {
				return out, err
			}
			return out, tx.Commit()
		case "delete_message":
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE discussion_messages SET body_enc='',deleted_at=COALESCE(deleted_at,NOW()) WHERE id=$1 AND thread_id=$2`, r.MessageID, r.ThreadID)
			if err == nil {
				var count int64
				count, err = result.RowsAffected()
				if err == nil && count == 0 {
					return out, sql.ErrNoRows
				}
			}
		case "edit":
			for _, tag := range r.Tags {
				if !slices.Contains(out.Tags, tag) {
					return out, netproto.ErrDiscussionInvalid
				}
			}
			_, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET title=$2,tags=$3,updated_at=NOW() WHERE id=$1`, r.ThreadID, r.Title, pq.Array(r.Tags))
		case "pin":
			_, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET pinned=$2 WHERE id=$1`, r.ThreadID, r.Pinned)
		case "resolve":
			_, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET resolved=$2 WHERE id=$1`, r.ThreadID, r.Resolved)
		case "join":
			_, err = tx.ExecContext(ctx, `INSERT INTO discussion_members(thread_id,unique_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, r.ThreadID, uid)
		case "leave":
			_, err = tx.ExecContext(ctx, `DELETE FROM discussion_members WHERE thread_id=$1 AND unique_id=$2`, r.ThreadID, uid)
		case "subscribe":
			var result sql.Result
			result, err = tx.ExecContext(ctx, `UPDATE discussion_members SET subscribed=$3 WHERE thread_id=$1 AND unique_id=$2`, r.ThreadID, uid, r.Subscribed)
			if err == nil {
				var n int64
				n, err = result.RowsAffected()
				if err == nil && n == 0 {
					return out, netproto.ErrDiscussionMembership
				}
			}
		case "archive", "reopen":
			_, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET archived=$2,updated_at=NOW(),last_activity_at=CASE WHEN $2 THEN last_activity_at ELSE NOW() END WHERE id=$1`, r.ThreadID, r.Action == "archive")
		case "send", "create":
			if archived {
				return out, netproto.ErrDiscussionArchived
			}
			var joined bool
			err = tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM discussion_members WHERE thread_id=$1 AND unique_id=$2)`, r.ThreadID, uid).Scan(&joined)
			if err != nil {
				return out, err
			}
			if !joined {
				return out, netproto.ErrDiscussionMembership
			}
			if r.BodyEnc == "" || r.KeyID == 0 {
				return out, netproto.ErrDiscussionInvalid
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO discussion_messages(thread_id,from_unique_id,from_nickname,body_enc,key_id,request_id) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(thread_id,from_unique_id,request_id) DO NOTHING`, r.ThreadID, uid, nickname, r.BodyEnc, r.KeyID, r.RequestID)
			if err == nil {
				// A retried send keeps its original message even after newer replies.
				err = tx.QueryRowContext(ctx, `SELECT id FROM discussion_messages WHERE thread_id=$1 AND from_unique_id=$2 AND request_id=$3`, r.ThreadID, uid, r.RequestID).Scan(&out.MessageID)
			}
			if err == nil {
				_, err = tx.ExecContext(ctx, `UPDATE discussion_threads SET updated_at=NOW(),last_activity_at=NOW() WHERE id=$1`, r.ThreadID)
			}
		}
		if err != nil {
			return out, err
		}
		if r.Action != "state" {
			rows, e := tx.QueryContext(ctx, `SELECT id,from_unique_id,from_nickname,body_enc,key_id,extract(epoch FROM sent_at)::bigint,deleted_at IS NOT NULL FROM discussion_messages WHERE thread_id=$1 AND ($2::bigint=0 OR id<$2) ORDER BY id DESC LIMIT 51`, r.ThreadID, r.BeforeID)
			if e != nil {
				return out, e
			}
			for rows.Next() {
				var m netproto.ChatHistoryEntry
				if e = rows.Scan(&m.ID, &m.FromUniqueID, &m.FromNickname, &m.BodyEnc, &m.KeyID, &m.SentAt, &m.Deleted); e != nil {
					closeRows(rows)
					return out, e
				}
				out.Messages = append(out.Messages, m)
			}
			e = rows.Err()
			closeRows(rows)
			if e != nil {
				return out, e
			}
			if len(out.Messages) > 50 {
				out.HasMore = true
				out.Messages = out.Messages[:50]
			}
			if len(out.Messages) > 0 && r.BeforeID == 0 && r.Action != "history" {
				_, err = tx.ExecContext(ctx, `UPDATE discussion_members SET last_read_id=GREATEST(last_read_id,$3) WHERE thread_id=$1 AND unique_id=$2`, r.ThreadID, uid, out.Messages[0].ID)
				if err != nil {
					return out, err
				}
			}
		}
	}
	if r.Action == "configure" {
		r.Tags = []string{}
	}
	rows, err := tx.QueryContext(ctx, `SELECT t.id,t.channel_id,COALESCE(t.root_message_id,0),t.title,t.tags,t.author,t.archived,m.unique_id IS NOT NULL,COALESCE(m.subscribed,FALSE),COALESCE(m.subscribed,FALSE) AND COALESCE(m.last_read_id,0)<COALESCE((SELECT max(id) FROM discussion_messages WHERE thread_id=t.id AND deleted_at IS NULL),0),(SELECT count(*) FROM discussion_messages WHERE thread_id=t.id AND deleted_at IS NULL),extract(epoch FROM t.updated_at)::bigint,t.pinned,t.resolved FROM discussion_threads t LEFT JOIN discussion_members m ON m.thread_id=t.id AND m.unique_id=$2 WHERE t.channel_id=$1 AND t.deleted_at IS NULL AND ($3::bigint=0 OR t.id=$3) AND ($3::bigint>0 OR (t.archived=$4 AND ($5::bigint=0 OR t.pinned<$7 OR (t.pinned=$7 AND t.id<$5)))) AND ($3::bigint>0 OR t.tags @> $6::text[]) AND (NOT $8 OR COALESCE(m.subscribed,FALSE)) ORDER BY t.pinned DESC,t.id DESC LIMIT 51`, r.ChannelID, uid, r.ThreadID, r.Archived, func() int64 {
		if r.ThreadID > 0 {
			return 0
		}
		return r.BeforeID
	}(), pq.Array(r.Tags), r.BeforePinned, r.Action == "list" && r.Subscribed)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var thread netproto.DiscussionThread
		var threadTags pq.StringArray
		if err = rows.Scan(&thread.ID, &thread.ChannelID, &thread.RootMessageID, &thread.Title, &threadTags, &thread.Author, &thread.Archived, &thread.Joined, &thread.Subscribed, &thread.Unread, &thread.MessageCount, &thread.UpdatedAt, &thread.Pinned, &thread.Resolved); err != nil {
			closeRows(rows)
			return out, err
		}
		thread.Tags = append([]string{}, threadTags...)
		out.Threads = append(out.Threads, thread)
	}
	err = rows.Err()
	closeRows(rows)
	if err != nil {
		return out, err
	}
	if len(out.Threads) > 50 {
		out.HasMore = true
		out.Threads = out.Threads[:50]
	}
	return out, tx.Commit()
}
