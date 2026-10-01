package server

import (
	"context"

	"noxa/internal/authorization"
	"noxa/internal/filetransfer"
	"noxa/internal/netproto"
)

func fileCapability(direction string) authorization.Capability {
	switch direction {
	case "upload":
		return authorization.UploadFiles
	case "download":
		return authorization.DownloadFiles
	default:
		return ""
	}
}

func (s *TCPServer) roleTransferClient(principal filetransfer.Principal) *Client {
	client, ok := s.clientByID(principal.SessionID)
	if !ok || !client.isAuthed() || client.userID() != principal.UserID || client.rulesBlocked() {
		return nil
	}
	return client
}

func (s *TCPServer) roleFileContext(ctx context.Context, client *Client, scope int64) context.Context {
	ctx = filetransfer.WithPrincipal(ctx, filetransfer.Principal{UserID: client.userID(), SessionID: client.ID})
	return filetransfer.WithMutationRights(ctx, scope, client.uniqueID(), s.roleAllowed(ctx, client, scope, authorization.ManageFiles))
}

func (s *TCPServer) guardRoleFileTransfer(ctx context.Context, principal filetransfer.Principal, scope int64, direction string, effect func(context.Context) error) error {
	if s.deps == nil || s.deps.Authority == nil {
		return filetransfer.ErrAccessRevoked
	}
	client := s.roleTransferClient(principal)
	if client == nil {
		return filetransfer.ErrAccessRevoked
	}
	return s.withRoleAccess(ctx, client, scope, fileCapability(direction), func(ctx context.Context) error {
		if s.roleTransferClient(principal) != client {
			return filetransfer.ErrAccessRevoked
		}
		if scope < 0 || s.deps == nil || s.deps.State == nil {
			return filetransfer.ErrAccessRevoked
		}
		if scope > 0 {
			if _, ok := s.deps.State.GetChannel(scope); !ok {
				return filetransfer.ErrAccessRevoked
			}
		}
		return effect(s.roleFileContext(ctx, client, scope))
	})
}

func (s *TCPServer) roleFileLink(ctx context.Context, client *Client, msg netproto.FileLink) error {
	return s.fileReadAction(ctx, client, msg.ChannelID, func(ctx context.Context) error {
		if client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		if err := s.fileManageAllowed(ctx, client, msg.ChannelID, msg.Folder, msg.Name); err != nil {
			return authorization.ErrRoleForbidden
		}
		ctx = s.roleFileContext(ctx, client, msg.ChannelID)
		return s.fileLinkAllowed(ctx, client, msg)
	})
}

// reconcileRoleFiles runs under the same exclusive gate as media/chat
// reconciliation. File chunks/finalization and token activation share its gate.
func (s *TCPServer) reconcileRoleFiles(after *authorization.RoleEvaluator) {
	if s.deps == nil || s.deps.FileTransfer == nil {
		return
	}
	s.deps.FileTransfer.RevokeTransfers(func(p filetransfer.Principal, scope int64, direction string) bool {
		return s.roleTransferClient(p) != nil && after.Evaluate(p.UserID, scope, fileCapability(direction)).Allowed
	})
}

func (s *TCPServer) roleFileTransferInit(ctx context.Context, client *Client, msg netproto.FileTransferInit) error {
	capability := fileCapability(msg.Direction)
	if capability == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "direction must be upload or download")
	}
	return s.roleAction(ctx, client, msg.ChannelID, capability, func(ctx context.Context) error {
		if client.rulesBlocked() {
			return authorization.ErrRoleForbidden
		}
		if s.deps.State == nil || msg.ChannelID < 0 {
			return authorization.ErrRoleForbidden
		}
		if msg.ChannelID > 0 {
			if _, ok := s.deps.State.GetChannel(msg.ChannelID); !ok {
				return authorization.ErrRoleForbidden
			}
		}
		ctx = s.roleFileContext(ctx, client, msg.ChannelID)
		var id, token string
		var err error
		if msg.Direction == "upload" {
			// Resource ceilings apply independently of role grants, including
			// to the owner and Administrator.
			id, token, err = s.deps.FileTransfer.InitUpload(ctx, msg.ChannelID, msg.Folder, msg.Name, msg.Size, client.uniqueID(), s.cfg.FileUserQuotaMB)
		} else {
			id, token, err = s.deps.FileTransfer.InitDownload(ctx, msg.ChannelID, msg.Folder, msg.Name)
		}
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "file transfer unavailable")
		}
		fingerprint := s.deps.FileTransfer.Fingerprint()
		return s.writeMessage(client, netproto.MsgFileTransferInitResponse, netproto.FileTransferInitResponse{TransferID: id, Token: token, Port: s.deps.FileTransfer.Port(), TLS: fingerprint != "", TLSFingerprint: fingerprint})
	})
}
