package server

import (
	"context"
	"encoding/json"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func (s *TCPServer) auditInChannels(ctx context.Context, actor, action, target, detail string, channels ...int64) {
	if s.deps == nil || s.deps.Groups == nil {
		return
	}
	body, err := json.Marshal(store.AuditDetail{Version: store.AuditDetailVersion, Channels: append([]int64{}, channels...), Text: detail})
	if err != nil {
		return
	}
	detail = string(body)
	s.deps.Groups.AuditScoped(ctx, actor, action, target, detail, append([]int64{}, channels...))
}

// audit writes an audit entry without a protected channel scope.
func (s *TCPServer) audit(ctx context.Context, actor, action, target, detail string) {
	s.auditInChannels(ctx, actor, action, target, detail)
}

// roleAuditEntry runs inside the read action's pinned policy lease, which lasts
// through response delivery. Redacted placeholders preserve pagination without
// revealing actor, action, resource identifiers or detail from protected rows.
func (s *TCPServer) roleAuditEntry(ctx context.Context, client *Client, entry store.AuditEntry) netproto.AuditEntry {
	return projectAuditEntry(entry, s.roleAllowed(ctx, client, 0, authorization.Administrator), func(channel int64) bool {
		return s.roleAllowed(ctx, client, channel, authorization.ViewChannel)
	})
}

// handleAuditLog returns a paged audit log with current protected-scope filtering.
func (s *TCPServer) handleAuditLog(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.AuditLog
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed audit_log: "+err.Error())
	}
	return s.roleAction(ctx, client, 0, authorization.ViewAuditLog, func(ctx context.Context) error {
		if s.deps == nil || s.deps.Groups == nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "audit store unavailable")
		}
		entries, err := s.deps.Groups.AuditList(ctx, msg.BeforeID, msg.Limit)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "audit query failed")
		}
		resp := netproto.AuditLogResponse{Entries: []netproto.AuditEntry{}, Capabilities: authorization.Capabilities()}
		for _, entry := range entries {
			resp.Entries = append(resp.Entries, s.roleAuditEntry(ctx, client, entry))
		}
		return s.writeMessage(client, netproto.MsgAuditLogResponse, resp)
	})
}

// The caller pins the policy used by administrator/visible through delivery.
// Only trusted storage scope can authorize detail; embedded JSON cannot.
func projectAuditEntry(entry store.AuditEntry, administrator bool, visible func(int64) bool) netproto.AuditEntry {
	row := netproto.AuditEntry{ID: entry.ID, Actor: entry.Actor, Action: entry.Action, Target: entry.Target, Detail: entry.Detail, CreatedAt: entry.CreatedAt.Unix()}
	row.Structured = entry.ChannelIDs != nil
	if administrator {
		return row
	}
	if entry.ChannelIDs != nil {
		allVisible := true
		for _, channel := range entry.ChannelIDs {
			if channel < 0 || !visible(channel) {
				allVisible = false
				break
			}
		}
		if allVisible {
			return row
		}
	}
	// Older unscoped audit text cannot safely establish its authorization scope.
	return netproto.AuditEntry{ID: entry.ID, CreatedAt: entry.CreatedAt.Unix(), Restricted: true}
}
