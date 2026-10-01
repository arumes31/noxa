package main

import (
	"errors"
	"fmt"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func checkRoleFiles(s *roleScenario, c *checkCtx, access authorization.ChannelPolicy) error {
	if c.opts.filePayload <= 0 || c.opts.filePayload > 16<<20 {
		return errors.New("role file payload must be between 1 byte and 16 MiB")
	}
	policy, err := s.policy()
	if err != nil {
		return err
	}
	for _, capability := range []authorization.Capability{authorization.UploadFiles, authorization.DownloadFiles} {
		access.Overrides = append(access.Overrides, authorization.RoleOverride{RoleID: policy.EveryoneID, Capability: capability, Effect: authorization.Allow})
	}
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	if err := checkFiles(c); err != nil {
		return fmt.Errorf("role file round trip: %w", err)
	}
	// Keep the second token completely unused until after regrant. Trying a
	// single-use token while denied could consume it and mask failed revocation.
	names := [2]string{"e2e-revoked-" + randHex(12) + ".bin", "e2e-revoked-" + randHex(12) + ".bin"}
	var pending [2]netproto.FileTransferInitResponse
	for i, name := range names {
		if err := writeMsg(c.alice.conn, netproto.MsgFileTransferInit, netproto.FileTransferInit{ChannelID: c.channelID, Direction: "upload", Name: name, Size: 1}); err != nil {
			return err
		}
		f, err := readOfType(c.alice.conn, netproto.MsgFileTransferInitResponse, readTimeout)
		if err != nil {
			return err
		}
		if err := netproto.Decode(f, &pending[i]); err != nil {
			return err
		}
		if pending[i].TransferID == "" || pending[i].Token == "" {
			return errors.New("pending upload did not receive a token")
		}
	}
	if pending[0].TransferID == pending[1].TransferID || pending[0].Token == pending[1].Token {
		return errors.New("independent uploads received reused transfer credentials")
	}
	access.Overrides = append(access.Overrides,
		authorization.RoleOverride{UserID: s.alice.UserID, Capability: authorization.UploadFiles, Effect: authorization.Deny},
		authorization.RoleOverride{UserID: s.bob.UserID, Capability: authorization.DownloadFiles, Effect: authorization.Deny})
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	for _, target := range []struct {
		cl        *client
		direction string
	}{{c.alice, "upload"}, {c.bob, "download"}} {
		if err := writeMsg(target.cl.conn, netproto.MsgFileTransferInit, netproto.FileTransferInit{ChannelID: c.channelID, Direction: target.direction, Name: names[0], Size: 1}); err != nil {
			return err
		}
		if err := readRoleFileDenial(target.cl); err != nil {
			return err
		}
	}
	if err := expectRevokedRoleTransfer(c.fileTransferAddr(pending[0].Port), pending[0]); err != nil {
		return err
	}
	// Restore only this run's channel overrides and prove the same sessions work.
	access.Overrides = access.Overrides[:len(access.Overrides)-2]
	if _, err := s.changeRole(authorization.RoleChange{Kind: authorization.ChannelAccessSet, Channel: access}); err != nil {
		return err
	}
	if err := expectRevokedRoleTransfer(c.fileTransferAddr(pending[1].Port), pending[1]); err != nil {
		return fmt.Errorf("revoked token revived after regrant: %w", err)
	}
	if err := checkFiles(c); err != nil {
		return fmt.Errorf("role file recovery: %w", err)
	}
	if err := writeMsg(c.alice.conn, netproto.MsgFileList, netproto.FileList{ChannelID: c.channelID}); err != nil {
		return err
	}
	f, err := readOfType(c.alice.conn, netproto.MsgFileListResponse, readTimeout)
	if err != nil {
		return err
	}
	var listing netproto.FileListResponse
	if err := netproto.Decode(f, &listing); err != nil {
		return err
	}
	for _, entry := range listing.Entries {
		if entry.Name == names[0] || entry.Name == names[1] {
			return errors.New("revoked upload left a published file")
		}
	}
	return nil
}

func readRoleFileDenial(cl *client) error {
	if err := cl.conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return err
	}
	defer clearE2EReadDeadline(cl.conn)
	denied := false
	for {
		f, err := netproto.ReadFrame(cl.conn)
		if err != nil {
			return err
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgFileTransferInitResponse:
			return errors.New("denied file initiation returned a transfer token")
		case netproto.MsgError:
			var result netproto.Error
			if err := netproto.Decode(f, &result); err != nil {
				return err
			}
			if denied {
				return errors.New("unexpected second error after file denial")
			}
			if err := validateRoleNativeError(result, netproto.MsgFileTransferInit, 4, ""); err != nil {
				return err
			}
			denied = true
			// Ping follows the error on the same ordered request stream. Keep
			// rejecting leaked tokens until its response has arrived.
			if err := writeMsg(cl.conn, netproto.MsgPing, netproto.Ping{}); err != nil {
				return err
			}
		case netproto.MsgPong:
			if denied {
				return nil
			}
		case netproto.MsgPing:
			if err := writeMsg(cl.conn, netproto.MsgPong, netproto.Pong{}); err != nil {
				return err
			}
		case netproto.MsgChannelKey:
			captureChannelKey(cl.conn, f)
		}
	}
}

func expectRevokedRoleTransfer(address string, token netproto.FileTransferInitResponse) error {
	conn, err := dialFileTransfer(address, token)
	if err != nil {
		return err
	}
	defer closeE2EResource(conn)
	if err := writeFTJSON(conn, ftInit, map[string]string{"token": token.Token, "transfer_id": token.TransferID}); err != nil {
		return err
	}
	ok, reason, err := readStatusFrame(conn)
	if err != nil {
		return err
	}
	if ok || (reason != "invalid transfer token" && reason != "file transfer access revoked" && reason != authorization.ErrRoleForbidden.Error()) {
		return errors.New("revoked token did not receive an explicit authorization refusal")
	}
	return nil
}
