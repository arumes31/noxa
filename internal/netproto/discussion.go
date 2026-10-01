package netproto

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	MsgDiscussionRequest MessageType = 180
	MsgDiscussionResult  MessageType = 181
)

var ErrDiscussionInvalid = errors.New("invalid discussion request")
var ErrDiscussionArchived = errors.New("thread is archived")
var ErrDiscussionMembership = errors.New("join the thread before replying")

// DiscussionRequest is always scoped to a channel. Text is accepted only at the
// local client bridge; the wire carries BodyEnc and KeyID instead.
type DiscussionRequest struct {
	Action           string   `json:"action"`
	ChannelID        int64    `json:"channel_id"`
	ThreadID         int64    `json:"thread_id,omitempty"`
	RootMessageID    int64    `json:"root_message_id,omitempty"`
	BeforeID         int64    `json:"before_id,omitempty"`
	BeforePinned     bool     `json:"before_pinned,omitempty"`
	Title            string   `json:"title,omitempty"`
	Tags             []string `json:"tags,omitempty"`
	Text             string   `json:"text,omitempty"`
	BodyEnc          string   `json:"body_enc,omitempty"`
	KeyID            uint32   `json:"key_id,omitempty"`
	RequestID        string   `json:"request_id,omitempty"`
	Archived         bool     `json:"archived,omitempty"`
	Subscribed       bool     `json:"subscribed,omitempty"`
	Forum            bool     `json:"forum,omitempty"`
	Pinned           bool     `json:"pinned,omitempty"`
	Resolved         bool     `json:"resolved,omitempty"`
	AutoArchiveHours int      `json:"auto_archive_hours,omitempty"`
}

type DiscussionThread struct {
	ID            int64    `json:"id"`
	ChannelID     int64    `json:"channel_id"`
	RootMessageID int64    `json:"root_message_id,omitempty"`
	Title         string   `json:"title"`
	Tags          []string `json:"tags"`
	Author        string   `json:"author"`
	Archived      bool     `json:"archived"`
	Pinned        bool     `json:"pinned"`
	Resolved      bool     `json:"resolved"`
	Joined        bool     `json:"joined"`
	Subscribed    bool     `json:"subscribed"`
	Unread        bool     `json:"unread"`
	MessageCount  int64    `json:"message_count"`
	UpdatedAt     int64    `json:"updated_at"`
}

type DiscussionResult struct {
	Action           string             `json:"action"`
	ChannelID        int64              `json:"channel_id"`
	ThreadID         int64              `json:"thread_id,omitempty"`
	Forum            bool               `json:"forum"`
	AutoArchiveHours int                `json:"auto_archive_hours"`
	Tags             []string           `json:"tags"`
	Threads          []DiscussionThread `json:"threads"`
	Messages         []ChatHistoryEntry `json:"messages"`
	Keys             []ChannelKey       `json:"keys,omitempty"`
	Refused          []uint32           `json:"refused,omitempty"`
	HasMore          bool               `json:"has_more"`
	CanManage        bool               `json:"can_manage"`
	CanModerate      bool               `json:"can_moderate"`
}

func (r DiscussionRequest) Validate() error {
	if r.AutoArchiveHours != 0 && r.AutoArchiveHours != 24 && r.AutoArchiveHours != 72 && r.AutoArchiveHours != 168 {
		return ErrDiscussionInvalid
	}
	if r.ChannelID <= 0 || r.BeforeID < 0 || r.RootMessageID < 0 || r.ThreadID < 0 || len(r.Tags) > 12 || len(r.BodyEnc) > 32768 || len(r.RequestID) > 80 || len(r.Text) > 12000 || utf8.RuneCountInString(r.Title) > 120 {
		return ErrDiscussionInvalid
	}
	seen := map[string]bool{}
	for _, tag := range r.Tags {
		if strings.TrimSpace(tag) != tag || tag == "" || utf8.RuneCountInString(tag) > 32 || seen[tag] {
			return ErrDiscussionInvalid
		}
		seen[tag] = true
	}
	switch r.Action {
	case "list", "configure":
		if r.ThreadID != 0 {
			return ErrDiscussionInvalid
		}
	case "create":
		if strings.TrimSpace(r.Title) == "" || utf8.RuneCountInString(r.Title) > 120 || r.ThreadID != 0 || r.RequestID == "" {
			return ErrDiscussionInvalid
		}
	case "send":
		if r.ThreadID <= 0 || r.RequestID == "" {
			return ErrDiscussionInvalid
		}
	case "edit":
		if r.ThreadID <= 0 || strings.TrimSpace(r.Title) == "" {
			return ErrDiscussionInvalid
		}
	case "get", "history", "state", "join", "leave", "subscribe", "archive", "reopen", "pin", "resolve":
		if r.ThreadID <= 0 {
			return ErrDiscussionInvalid
		}
	default:
		return ErrDiscussionInvalid
	}
	return nil
}
