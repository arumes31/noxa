// files.go implements the file-transfer control handlers of the TCP control
// server: issuing transfer tokens, listing/managing channel files (folders,
// rename, delete, versions), and minting download links. The actual byte
// transfer happens on the file-transfer port (internal/filetransfer); these
// handlers only do permission checks and bookkeeping.
package server

import (
	"context"
	"fmt"
	"net"
	"strconv"

	"go.uber.org/zap"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

func portFromAddress(address string) (int, error) {
	_, portText, err := net.SplitHostPort(address)
	if err != nil {
		return 0, fmt.Errorf("parsing listen address %q: %w", address, err)
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("parsing port in listen address %q: %w", address, err)
	}
	if port == 0 {
		return 0, fmt.Errorf("listen address %q has no fixed port", address)
	}
	return int(port), nil
}

// handleFileTransferInit issues a single-use transfer token after a
// permission check (upload/download power; unset = allowed, negated =
// denied) and the file-transfer backend's own validation (size cap, both
// quota axes, name/folder sanitization).
func (s *TCPServer) handleFileTransferInit(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.FileTransferInit
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed file_transfer_init: "+err.Error())
	}
	if s.deps == nil || s.deps.FileTransfer == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "file transfer backend unavailable")
	}
	return s.roleFileTransferInit(ctx, client, msg)
}

// fileEntries maps store records to wire entries.
func fileEntries(files []store.FileRecord) []netproto.FileEntry {
	out := make([]netproto.FileEntry, 0, len(files))
	for _, rec := range files {
		out = append(out, netproto.FileEntry{
			Name:       rec.Name,
			Folder:     rec.Folder,
			Size:       rec.Size,
			SHA256:     rec.SHA256,
			Uploader:   rec.Uploader,
			UploadedAt: rec.UploadedAt,
			// (91-135) sending the flag lets the browser stop inferring
			// sealedness from the ".vcx" suffix.
			Encrypted: rec.Encrypted,
		})
	}
	return out
}

// handleFileList returns the channel's file listing for one folder plus the
// channel quota state (265). Listing requires the download permission.
func (s *TCPServer) handleFileList(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.FileList
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed file_list: "+err.Error())
	}
	if s.deps == nil || s.deps.FileTransfer == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "file transfer backend unavailable")
	}
	folder := msg.Folder
	if folder == "" && msg.Path != "" && msg.Path != "/" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "legacy path field supports only empty or /")
	}

	return s.fileReadAction(ctx, client, msg.ChannelID, func(ctx context.Context) error {
		files, err := s.deps.FileTransfer.ListFiles(ctx, msg.ChannelID, folder)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "listing files failed")
		}
		folders, err := s.deps.FileTransfer.ListFileFolders(ctx, msg.ChannelID)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "listing folders failed")
		}
		used, quota, err := s.deps.FileTransfer.ChannelQuota(ctx, msg.ChannelID)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "reading quota failed")
		}

		return s.writeMessage(client, netproto.MsgFileListResponse, netproto.FileListResponse{
			Entries:    fileEntries(files),
			Folders:    folders,
			UsedBytes:  used,
			QuotaBytes: quota,
		})
	})
}

func (s *TCPServer) fileReadAction(ctx context.Context, client *Client, channelID int64, effect func(context.Context) error) error {
	return s.roleAction(ctx, client, channelID, authorization.DownloadFiles, effect)
}

// fileManageAllowed gates file delete/rename (263): the uploader and holders
// of b_ft_delete (admins bypass) may manage a file.
func (s *TCPServer) fileManageAllowed(ctx context.Context, client *Client, channelID int64, folder, name string) error {
	if s.roleAllowed(ctx, client, channelID, authorization.ManageFiles) {
		return nil
	}
	files, err := s.deps.FileTransfer.ListFiles(ctx, channelID, folder)
	if err != nil {
		return fmt.Errorf("checking file owner failed")
	}
	for _, rec := range files {
		if rec.Name == name {
			if rec.Uploader == client.UniqueID {
				return nil
			}
			return authorization.ErrRoleForbidden
		}
	}
	return fmt.Errorf("file not found")
}

// handleFileDelete deletes a channel file (record + blob), audited.
func (s *TCPServer) handleFileDelete(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.FileDelete
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed file_delete: "+err.Error())
	}
	if msg.AckRequested && (msg.ChannelID < 0 || msg.Name == "") {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid file mutation")
	}
	if s.deps == nil || s.deps.FileTransfer == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "file transfer backend unavailable")
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.ViewChannel, func(ctx context.Context) error {
		if err := s.fileManageAllowed(ctx, client, msg.ChannelID, msg.Folder, msg.Name); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, err.Error())
		}
		ctx = s.roleFileContext(ctx, client, msg.ChannelID)
		if err := s.deps.FileTransfer.DeleteFile(ctx, msg.ChannelID, msg.Folder, msg.Name); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "delete failed: "+err.Error())
		}
		s.auditInChannels(ctx, client.UniqueID, "file_delete", fmt.Sprintf("%d:%s/%s", msg.ChannelID, msg.Folder, msg.Name), "", msg.ChannelID)
		return s.acknowledgeFile(client, msg.AckRequested, netproto.FileMutationSaved{
			Operation: netproto.MsgFileDelete, ChannelID: msg.ChannelID, Folder: msg.Folder, Name: msg.Name,
		})
	})
}

// handleFileRename renames/moves a channel file (record + blob), audited.
func (s *TCPServer) handleFileRename(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.FileRename
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed file_rename: "+err.Error())
	}
	if msg.AckRequested && (msg.ChannelID < 0 || msg.NewChannelID < 0 || msg.Name == "") {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid file mutation")
	}
	if s.deps == nil || s.deps.FileTransfer == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "file transfer backend unavailable")
	}
	if msg.NewName == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "new_name is required")
	}
	return s.roleAction(ctx, client, msg.ChannelID, authorization.ViewChannel, func(ctx context.Context) error {
		if err := s.fileManageAllowed(ctx, client, msg.ChannelID, msg.Folder, msg.Name); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, err.Error())
		}
		target := msg.NewChannelID
		if target == 0 {
			target = msg.ChannelID
		}
		if target != msg.ChannelID {
			if !s.roleAllowed(ctx, client, target, authorization.UploadFiles) {
				return authorization.ErrRoleForbidden
			}
			// (262) a cross-channel move is an upload into the destination as much
			// as a delete from the source, so it needs the upload right THERE:
			// managing a file in one channel must not be a way to push it into a
			// channel the mover cannot write to.
			if s.deps.State == nil {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "state backend unavailable")
			}
			if _, ok := s.deps.State.GetChannel(target); !ok {
				return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "target channel not found")
			}
		}
		ctx = s.roleFileContext(ctx, client, msg.ChannelID)
		if err := s.deps.FileTransfer.MoveFile(ctx, msg.ChannelID, msg.Folder, msg.Name, target, msg.NewFolder, msg.NewName); err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "rename failed: "+err.Error())
		}
		s.auditInChannels(ctx, client.UniqueID, "file_rename", fmt.Sprintf("%d:%s/%s", msg.ChannelID, msg.Folder, msg.Name),
			fmt.Sprintf("to %d:%s/%s", target, msg.NewFolder, msg.NewName), msg.ChannelID, target)
		return s.acknowledgeFile(client, msg.AckRequested, netproto.FileMutationSaved{
			Operation: netproto.MsgFileRename, ChannelID: msg.ChannelID, Folder: msg.Folder, Name: msg.Name,
			NewChannelID: msg.NewChannelID, NewFolder: msg.NewFolder, NewName: msg.NewName,
		})
	})
}

// handleFileVersions lists a file's rotated old versions (264).
func (s *TCPServer) handleFileVersions(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.FileVersions
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed file_versions: "+err.Error())
	}
	if s.deps == nil || s.deps.FileTransfer == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "file transfer backend unavailable")
	}
	return s.fileReadAction(ctx, client, msg.ChannelID, func(ctx context.Context) error {
		files, err := s.deps.FileTransfer.ListFileVersions(ctx, msg.ChannelID, msg.Folder, msg.Name)
		if err != nil {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "listing versions failed")
		}
		return s.writeMessage(client, netproto.MsgFileVersionsResponse, netproto.FileVersionsResponse{
			Entries: fileEntries(files),
		})
	})
}

// handleFileLink mints an expiring download link (267). Gated by admin or
// the file's uploader (links bypass auth on the HTTP path, so issuance is
// restricted).
func (s *TCPServer) handleFileLink(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.FileLink
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed file_link: "+err.Error())
	}
	if s.deps == nil || s.deps.FileTransfer == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "file transfer backend unavailable")
	}
	return s.roleFileLink(ctx, client, msg)
}

func (s *TCPServer) fileLinkAllowed(ctx context.Context, client *Client, msg netproto.FileLink) error {
	port, err := portFromAddress(s.cfg.HealthAddr)
	if err != nil {
		s.logger.Warn("invalid health address for download link", zap.String("addr", s.cfg.HealthAddr), zap.Error(err))
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "download links are unavailable")
	}
	token, expires, err := s.deps.FileTransfer.CreateLink(ctx, msg.ChannelID, msg.Folder, msg.Name)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "link failed: "+err.Error())
	}
	s.auditInChannels(ctx, client.UniqueID, "file_link", fmt.Sprintf("%d:%s/%s", msg.ChannelID, msg.Folder, msg.Name), "", msg.ChannelID)

	// The client builds the final URL from its own control host (the server
	// cannot know its published address behind Docker/NAT). The health listener
	// serving /dl is plaintext, regardless of the TLS control connection.
	return s.writeMessage(client, netproto.MsgFileLinkResponse, netproto.FileLinkResponse{
		Path:         "/dl/" + token,
		Scheme:       "http",
		HealthPort:   port,
		ExpiresAt:    expires.Unix(),
		SessionBound: true,
	})
}
