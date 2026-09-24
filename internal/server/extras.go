// extras.go implements avatar, channel icon, complaint, client information,
// and screen-share control handlers.
package server

import (
	"context"
	"encoding/base64"
	"errors"
	"strconv"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/channels"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

// maxImageBytes is the maximum decoded size of an avatar or channel icon.
const maxImageBytes = 256 * 1024

// Event types for the Phase 9 features.
const (
	eventAvatarChanged       = "avatar_changed"
	eventChannelIconChanged  = "channel_icon_changed"
	eventScreenshareChanged  = "screenshare_changed"
	eventServerBannerChanged = "server_banner_changed"
)

// decodeImage validates a base64-encoded avatar/icon image: it decodes,
// enforces the size cap, and sniffs the content type, returning the raw
// bytes and the file extension for the detected type.
func decodeImage(dataBase64 string) ([]byte, string, error) {
	raw, err := base64.StdEncoding.DecodeString(dataBase64)
	if err != nil {
		return nil, "", errors.New("invalid base64 data")
	}
	if len(raw) == 0 {
		return nil, "", errors.New("empty image data")
	}
	if len(raw) > maxImageBytes {
		return nil, "", errors.New("image too large (max 256 KiB)")
	}
	format, err := inspectAssetImage(raw)
	if err != nil {
		return nil, "", errors.New("unsupported image type (want png, jpeg, gif, or webp)")
	}
	return raw, format.extension, nil
}

// handleAvatarSet validates and stores the client's avatar, replacing any
// previous one, and announces the change.
func (s *TCPServer) handleAvatarSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.AvatarSet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed avatar_set: "+err.Error())
	}
	return s.roleAction(ctx, client, 0, authorization.UploadAvatar, func(ctx context.Context) error {
		return s.setAvatar(ctx, client, msg)
	})
}

func (s *TCPServer) setAvatar(ctx context.Context, client *Client, msg netproto.AvatarSet) error {
	if client.userID() == 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "guests cannot upload avatars")
	}
	raw, ext, err := decodeImage(msg.DataBase64)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
	}
	if _, err := s.assets().writeAvatar(client.UniqueID, ext, raw); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "storing avatar failed")
	}

	s.broadcastEvent(eventAvatarChanged, userEvent{ClientID: client.ID, UniqueID: client.UniqueID})
	return s.acknowledgeAsset(client, msg.AckRequested, netproto.MsgAvatarSet, "", "")
}

// handleAvatarGet returns another user's avatar, if set.
func (s *TCPServer) handleAvatarGet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.AvatarGet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed avatar_get: "+err.Error())
	}
	return s.roleAvatarGet(ctx, client, msg)
}

func (s *TCPServer) readAvatar(ctx context.Context, client *Client, msg netproto.AvatarGet) error {
	raw, image, err := s.assets().readAvatar(msg.UniqueID)
	if err == nil {
		return s.writeMessage(client, netproto.MsgAvatarData, netproto.AvatarData{
			UniqueID:    msg.UniqueID,
			DataBase64:  base64.StdEncoding.EncodeToString(raw),
			ContentType: image.contentType,
		})
	}
	return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "no avatar for this user")
}

// handleChannelIconSet authorizes the target channel before storing its icon.
func (s *TCPServer) handleChannelIconSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ChannelIconSet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed channel_icon_set: "+err.Error())
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.ManageChannels, func(ctx context.Context) error {
		return s.setChannelIcon(ctx, client, msg, false)
	})
}

func (s *TCPServer) handleRoleChannelIconSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ChannelIconSet
	if err := netproto.Decode(f, &msg); err != nil || msg.ChannelID < 1 || msg.CopyFromChannelID < 0 ||
		(msg.CopyFromChannelID != 0 && msg.DataBase64 != "") {
		return s.roleError(ctx, client, authorization.ErrRoleInvalid)
	}
	if s.deps == nil || s.deps.Authority == nil {
		return s.roleError(ctx, client, authorization.ErrRolesNotConfigured)
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.ManageChannels, func(ctx context.Context) error {
		return s.setChannelIcon(ctx, client, msg, true)
	})
}

func (s *TCPServer) setChannelIcon(ctx context.Context, client *Client, msg netproto.ChannelIconSet, acknowledge bool) error {
	if s.deps == nil || s.deps.State == nil || s.deps.Channels == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	if _, ok := s.deps.State.GetChannel(msg.ChannelID); !ok {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "channel not found")
	}

	raw, ext, err := decodeImage(msg.DataBase64)
	if msg.CopyFromChannelID != 0 && msg.DataBase64 == "" {
		// (271) icon library: reuse another channel's stored icon.
		if err := s.withRoleAccess(ctx, client, msg.CopyFromChannelID, authorization.ViewChannel, func(context.Context) error { return nil }); err != nil {
			return err
		}
		if _, ok := s.deps.State.GetChannel(msg.CopyFromChannelID); !ok {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "source channel not found")
		}
		var source assetImage
		raw, source, err = s.assets().readImage("icons", strconv.FormatInt(msg.CopyFromChannelID, 10))
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "source channel has no icon")
		}
		ext = source.extension
	} else if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
	}
	name := strconv.FormatInt(msg.ChannelID, 10)
	if acknowledge && ctx.Err() != nil {
		return s.roleError(ctx, client, ctx.Err())
	}
	err = s.deps.Channels.WithChannelLifecycle(msg.ChannelID, func() error {
		if _, err := s.assets().writeImage("icons", name, ext, raw); err != nil {
			return err
		}
		if !s.deps.State.SetChannelHasIcon(msg.ChannelID, true) {
			return channels.ErrChannelNotFound
		}
		return nil
	})
	if errors.Is(err, channels.ErrChannelNotFound) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "channel not found")
	}
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "storing icon failed")
	}

	s.broadcastEvent(eventChannelIconChanged, channelEvent{ChannelID: msg.ChannelID})
	if acknowledge {
		auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		s.auditInChannels(auditCtx, client.uniqueID(), "channel_icon_set", name, "", msg.ChannelID)
		cancel()
		replyCtx, cancelReply := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancelReply()
		return s.writeMessageInContext(replyCtx, client, netproto.MsgRoleChannelIconSaved, netproto.RoleChannelIconSaved{ChannelID: msg.ChannelID})
	}
	return nil
}

// handleChannelIconGet requires channel visibility in role mode. An empty
// payload means a visible channel has no icon, which is a normal answer.
func (s *TCPServer) handleChannelIconGet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ChannelIconGet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed channel_icon_get: "+err.Error())
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.ViewChannel, func(ctx context.Context) error {
		return s.readChannelIcon(ctx, client, msg)
	})
}

func (s *TCPServer) readChannelIcon(ctx context.Context, client *Client, msg netproto.ChannelIconGet) error {
	if s.deps == nil || s.deps.State == nil || s.deps.Channels == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	var (
		raw   []byte
		image assetImage
	)
	err := s.deps.Channels.WithChannelLifecycle(msg.ChannelID, func() error {
		channel, ok := s.deps.State.GetChannel(msg.ChannelID)
		if !ok || !channel.HasIcon {
			return channels.ErrChannelNotFound
		}
		var err error
		raw, image, err = s.assets().readImage("icons", strconv.FormatInt(msg.ChannelID, 10))
		return err
	})
	if err == nil {
		return s.writeMessage(client, netproto.MsgChannelIconData, netproto.ChannelIconData{
			ChannelID:   msg.ChannelID,
			DataBase64:  base64.StdEncoding.EncodeToString(raw),
			ContentType: image.contentType,
		})
	}
	return s.writeMessage(client, netproto.MsgChannelIconData, netproto.ChannelIconData{ChannelID: msg.ChannelID})
}

// --- server icon + banner (270) -----------------------------------------------

// storeBranding writes one authorized branding image, replacing whatever
// extension was there before so a png does not linger behind a new jpg.
func (s *TCPServer) storeBranding(ctx context.Context, client *Client, base, dataBase64, action string, acknowledge bool, operation netproto.MessageType) error {
	return s.roleAction(ctx, client, 0, authorization.ManageServer, func(ctx context.Context) error {
		raw, ext, err := decodeImage(dataBase64)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, err.Error())
		}
		if _, err := s.assets().writeImage("", base, ext, raw); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "branding write failed")
		}
		s.audit(ctx, client.UniqueID, action, "", ext)
		if base == "server_banner" {
			// Announce only a successful write, while its authorization is pinned.
			s.broadcastEvent(eventServerBannerChanged, map[string]any{"by": client.UniqueID})
		}
		return s.acknowledgeAsset(client, acknowledge, operation, "", "")
	})
}

// handleServerBannerSet stores the server banner (admin only, 270).
func (s *TCPServer) handleServerBannerSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ServerBannerSet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_banner_set: "+err.Error())
	}
	return s.storeBranding(ctx, client, "server_banner", msg.DataBase64, "server_banner_set", msg.AckRequested, netproto.MsgServerBannerSet)
}

// handleServerBannerGet returns the server banner (empty payload when unset).
func (s *TCPServer) handleServerBannerGet(ctx context.Context, client *Client, f *netproto.Frame) error {
	if err := netproto.Decode(f, &netproto.ServerBannerGet{}); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_banner_get: "+err.Error())
	}
	raw, image, err := s.assets().readImage("", "server_banner")
	if err != nil {
		return s.writeMessage(client, netproto.MsgServerBannerDat, netproto.ServerBannerData{})
	}
	return s.writeMessage(client, netproto.MsgServerBannerDat, netproto.ServerBannerData{
		DataBase64:  base64.StdEncoding.EncodeToString(raw),
		ContentType: image.contentType,
	})
}

// handleServerIconSet stores the server icon (admin only, 270).
func (s *TCPServer) handleServerIconSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ServerIconSet
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_icon_set: "+err.Error())
	}
	return s.storeBranding(ctx, client, "server_icon", msg.DataBase64, "server_icon_set", msg.AckRequested, netproto.MsgServerIconSet)
}

// handleServerIconGet returns the server icon (empty payload when unset).
func (s *TCPServer) handleServerIconGet(ctx context.Context, client *Client, f *netproto.Frame) error {
	if err := netproto.Decode(f, &netproto.ServerIconGet{}); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_icon_get: "+err.Error())
	}
	raw, image, err := s.assets().readImage("", "server_icon")
	if err != nil {
		return s.writeMessage(client, netproto.MsgServerIconData, netproto.ServerIconData{})
	}
	return s.writeMessage(client, netproto.MsgServerIconData, netproto.ServerIconData{
		DataBase64:  base64.StdEncoding.EncodeToString(raw),
		ContentType: image.contentType,
	})
}

// handleClientInfoQuery returns the connection info of an online client.
// Self queries always return full data (including own IP/port). For other
// clients, IP and port are only included when the requester is admin or
// holds b_client_remoteaddress_view (deny-on-unset; IP is sensitive).
func (s *TCPServer) handleClientInfoQuery(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ClientInfoQuery
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed client_info_query: "+err.Error())
	}
	return s.roleClientInfo(ctx, client, msg.ClientID)
}

// handleComplaint files a complaint against a user. The store enforces the
// open-complaint limit per reporter.
func (s *TCPServer) handleComplaint(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.fileSessionComplaint(ctx, client, f)
	})
}

func (s *TCPServer) fileSessionComplaint(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.Complaint
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed complaint: "+err.Error())
	}
	if s.deps == nil || s.deps.Complaints == nil || s.deps.Auth == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "complaint backend unavailable")
	}
	if msg.Reason == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "reason must not be empty")
	}

	if _, err := s.deps.Auth.LookupUser(ctx, msg.TargetUniqueID); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target user not found")
	}
	if err := s.deps.Complaints.AddComplaint(ctx, client.UniqueID, msg.TargetUniqueID, msg.Reason); err != nil {
		if errors.Is(err, store.ErrComplaintLimit) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "too many open complaints")
		}
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "filing complaint failed")
	}
	return nil
}

// --- complaint review (173) --------------------------------------------------

// nicknameResolver returns a memoized unique ID -> nickname lookup: the
// online client first, then the users row. Complaint rows store only unique
// IDs, which are unreadable in an admin list (173). Unknown IDs resolve to
// "" so the client can fall back to the unique ID.
func (s *TCPServer) nicknameResolver(ctx context.Context) func(string) string {
	cache := make(map[string]string)
	return func(uniqueID string) string {
		if uniqueID == "" {
			return ""
		}
		if n, ok := cache[uniqueID]; ok {
			return n
		}
		n := ""
		if s.deps != nil && s.deps.State != nil {
			if sc, ok := s.deps.State.GetClientByUniqueID(uniqueID); ok {
				n = sc.Nickname
			}
		}
		if n == "" && s.deps != nil && s.deps.Auth != nil {
			if u, err := s.deps.Auth.LookupUser(ctx, uniqueID); err == nil && u != nil {
				n = u.Nickname
			}
		}
		cache[uniqueID] = n
		return n
	}
}

// complaintsResponse builds the complaint list with display nicknames.
func (s *TCPServer) complaintsResponse(ctx context.Context) (netproto.Complaints, error) {
	rows, err := s.deps.Complaints.ListComplaints(ctx)
	if err != nil {
		return netproto.Complaints{}, err
	}
	return s.complaintRowsResponse(ctx, rows)
}

func (s *TCPServer) complaintRowsResponse(ctx context.Context, rows []store.Complaint) (netproto.Complaints, error) {
	nick := s.nicknameResolver(ctx)
	resp := netproto.Complaints{Entries: []netproto.ComplaintEntry{}}
	for _, c := range rows {
		if err := ctx.Err(); err != nil {
			return netproto.Complaints{}, err
		}
		resp.Entries = append(resp.Entries, netproto.ComplaintEntry{
			TargetUniqueID: c.Target,
			TargetNickname: nick(c.Target),
			FromUniqueID:   c.Reporter,
			FromNickname:   nick(c.Reporter),
			Reason:         c.Reason,
			CreatedAt:      c.CreatedAt.Unix(),
		})
	}
	return resp, nil
}

// withComplaintManagement gates complaint review. Complaints are moderation
// evidence naming both parties, so they ride the same gate as the ban list
// rather than a key of their own (173).
func (s *TCPServer) withComplaintManagement(ctx context.Context, client *Client, effect func(context.Context) error) error {
	return s.roleAction(ctx, client, 0, authorization.BanMembers, func(ctx context.Context) error {
		if s.deps == nil || s.deps.Complaints == nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "complaint backend unavailable")
		}
		return effect(ctx)
	})
}

// handleComplaintList returns every filed complaint (173).
func (s *TCPServer) handleComplaintList(ctx context.Context, client *Client, f *netproto.Frame) error {
	if err := netproto.Decode(f, &netproto.ComplaintList{}); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed complaint_list: "+err.Error())
	}
	return s.withComplaintManagement(ctx, client, func(ctx context.Context) error {
		resp, err := s.complaintsResponse(ctx)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "complaint list failed")
		}
		return s.writeMessage(client, netproto.MsgComplaints, resp)
	})
}

// handleComplaintClear resolves complaints against a target and replies with
// the refreshed list so the admin view cannot drift (173).
func (s *TCPServer) handleComplaintClear(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ComplaintClear
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed complaint_clear: "+err.Error())
	}
	return s.withComplaintManagement(ctx, client, func(ctx context.Context) error {
		if msg.TargetUniqueID == "" {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "target_unique_id must not be empty")
		}

		n, err := s.clearComplaints(ctx, client.UniqueID, msg)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "complaint clear failed")
		}
		if n == 0 {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "no matching complaint")
		}
		resp, err := s.complaintsResponse(ctx)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "complaint list failed")
		}
		return s.writeMessage(client, netproto.MsgComplaints, resp)
	})
}

// handleScreenShare relays the client's screen-share state to the other
// members of its channel. The video track itself is routed like camera
// video (one video track per peer; see the video SFU docs). Gated by the
// video publish permission.
func (s *TCPServer) handleScreenShare(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ScreenShare
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed screen_share: "+err.Error())
	}
	if s.deps == nil || s.deps.State == nil || s.deps.Broadcast == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
	}
	return s.roleScreenShare(ctx, client, msg)
}
