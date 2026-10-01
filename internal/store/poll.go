package store

import (
	"context"
	"database/sql"
	"errors"
	"slices"
	"time"

	"github.com/lib/pq"
	"noxa/internal/netproto"
)

func (s *Store) StoreChatPoll(ctx context.Context, channelID int64, fromUID, fromNickname, bodyEnc string, keyID uint32, clientMsgID string, poll netproto.PollDefinition) (_ int64, _ bool, retErr error) {
	if err := poll.Validate(time.Now()); err != nil {
		return 0, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer rollbackRoleTx(tx, &retErr)
	scope := 1
	if channelID == 0 {
		scope = 0
	}
	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO chat_messages(scope,channel_id,from_unique_id,from_nickname,body_enc,key_id,client_msg_id)
	VALUES($1,$2,$3,$4,$5,$6,NULLIF($7,'')) ON CONFLICT(channel_id,from_unique_id,client_msg_id)
	WHERE client_msg_id IS NOT NULL AND client_msg_id<>'' DO NOTHING RETURNING id`, scope, channelID, fromUID, fromNickname, bodyEnc, keyID, clientMsgID).Scan(&id)
	inserted := err == nil
	if errors.Is(err, sql.ErrNoRows) && clientMsgID != "" {
		err = tx.QueryRowContext(ctx, `SELECT m.id FROM chat_messages m JOIN chat_polls p ON p.message_id=m.id WHERE m.channel_id=$1 AND m.from_unique_id=$2 AND m.client_msg_id=$3 AND m.deleted_at IS NULL`, channelID, fromUID, clientMsgID).Scan(&id)
	}
	if err != nil {
		return 0, false, err
	}
	if inserted {
		_, err = tx.ExecContext(ctx, `INSERT INTO chat_polls(message_id,option_count,multiple,closes_at) VALUES($1,$2,$3,$4)`, id, len(poll.Options), poll.Multiple, time.Unix(poll.ClosesAt, 0))
		if err != nil {
			return 0, false, err
		}
	}
	return id, inserted, tx.Commit()
}

func (s *Store) ReadPoll(ctx context.Context, messageID int64, uniqueID string) (_ netproto.PollState, retErr error) {
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return netproto.PollState{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	state, err := readPoll(ctx, tx, messageID, uniqueID)
	if err != nil {
		return state, err
	}
	return state, tx.Commit()
}

func readPoll(ctx context.Context, tx *sql.Tx, messageID int64, uniqueID string) (netproto.PollState, error) {
	state := netproto.PollState{MessageID: messageID, Choices: []int{}}
	var count int
	err := tx.QueryRowContext(ctx, `SELECT p.option_count,p.closed OR p.closes_at<=clock_timestamp(),extract(epoch FROM p.closes_at)::bigint,p.version FROM chat_polls p JOIN chat_messages m ON m.id=p.message_id WHERE p.message_id=$1 AND m.deleted_at IS NULL`, messageID).Scan(&count, &state.Closed, &state.ClosesAt, &state.Version)
	if err != nil {
		return state, err
	}
	state.Counts = make([]int, count)
	rows, err := tx.QueryContext(ctx, `SELECT choice,count(*) FROM chat_poll_votes v CROSS JOIN unnest(v.choices) choice WHERE message_id=$1 GROUP BY choice`, messageID)
	if err != nil {
		return state, err
	}
	defer closeRows(rows)
	for rows.Next() {
		var choice, votes int
		if err := rows.Scan(&choice, &votes); err != nil {
			return state, err
		}
		if choice >= 0 && choice < count {
			state.Counts[choice] = votes
		}
	}
	if err := rows.Err(); err != nil {
		return state, err
	}
	if err := rows.Close(); err != nil {
		return state, err
	}
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM chat_poll_votes WHERE message_id=$1`, messageID).Scan(&state.TotalVoters); err != nil {
		return state, err
	}
	var choices pq.Int64Array
	err = tx.QueryRowContext(ctx, `SELECT choices FROM chat_poll_votes WHERE message_id=$1 AND unique_id=$2`, messageID, uniqueID).Scan(&choices)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return state, err
	}
	for _, choice := range choices {
		state.Choices = append(state.Choices, int(choice))
	}
	return state, nil
}

// ChangePoll serializes ballots and closure on the poll row, while the message
// lock prevents a concurrent deletion from admitting a vote on a tombstone.
func (s *Store) ChangePoll(ctx context.Context, messageID int64, uniqueID string, choices []int, closePoll bool) (_ netproto.PollState, retErr error) {
	if uniqueID == "" || len(choices) > 10 {
		return netproto.PollState{}, netproto.ErrPollInvalid
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return netproto.PollState{}, err
	}
	defer rollbackRoleTx(tx, &retErr)
	var count int
	var multiple, closed, expired bool
	// Lock the message before the poll, matching deletion's parent-first order.
	var id int64
	err = tx.QueryRowContext(ctx, `SELECT id FROM chat_messages WHERE id=$1 AND deleted_at IS NULL FOR SHARE`, messageID).Scan(&id)
	if err != nil {
		return netproto.PollState{}, err
	}
	err = tx.QueryRowContext(ctx, `SELECT option_count,multiple,closed,closes_at<=clock_timestamp() FROM chat_polls WHERE message_id=$1 FOR UPDATE`, messageID).Scan(&count, &multiple, &closed, &expired)
	if err != nil {
		return netproto.PollState{}, err
	}
	if closePoll {
		if !closed {
			_, err = tx.ExecContext(ctx, `UPDATE chat_polls SET closed=TRUE,version=version+1 WHERE message_id=$1`, messageID)
		}
	} else {
		if closed || expired {
			return netproto.PollState{}, netproto.ErrPollClosed
		}
		if !multiple && len(choices) > 1 {
			return netproto.PollState{}, netproto.ErrPollInvalid
		}
		choices = slices.Clone(choices)
		slices.Sort(choices)
		for i, choice := range choices {
			if choice < 0 || choice >= count || (i > 0 && choices[i-1] == choice) {
				return netproto.PollState{}, netproto.ErrPollInvalid
			}
		}
		if len(choices) == 0 {
			_, err = tx.ExecContext(ctx, `DELETE FROM chat_poll_votes WHERE message_id=$1 AND unique_id=$2`, messageID, uniqueID)
		} else {
			_, err = tx.ExecContext(ctx, `INSERT INTO chat_poll_votes(message_id,unique_id,choices) VALUES($1,$2,$3) ON CONFLICT(message_id,unique_id) DO UPDATE SET choices=EXCLUDED.choices`, messageID, uniqueID, pq.Array(choices))
		}
		if err == nil {
			_, err = tx.ExecContext(ctx, `UPDATE chat_polls SET version=version+1 WHERE message_id=$1`, messageID)
		}
	}
	if err != nil {
		return netproto.PollState{}, err
	}
	state, err := readPoll(ctx, tx, messageID, uniqueID)
	if err != nil {
		return state, err
	}
	return state, tx.Commit()
}
