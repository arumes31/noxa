package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"time"

	"noxa/internal/netproto"
)

// UploadChatAttachmentProgressForTab preserves encrypted attachment semantics
// while reporting actual ciphertext transfer progress through ft_progress.
func (a *App) UploadChatAttachmentProgressForTab(tabID, id string, channelID int64, name, dataBase64 string) (string, error) {
	if len(id) < 1 || len(id) > 128 {
		return "", fmt.Errorf("invalid upload identifier")
	}
	cm, err := a.requireTabCM(tabID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cm.mu.Lock()
	epoch := cm.transferEpoch
	cm.mu.Unlock()
	untrack, tracked := cm.trackTransferAt(id, epoch, &chatUploadCancellation{cancel: cancel})
	if !tracked {
		return "", errTransferCanceled
	}
	defer untrack()
	token, err := uploadChatAttachmentUsing(name, dataBase64, func(storage string, blob []byte) error {
		p := ftProgress{ID: id, Direction: "upload", Name: name, Total: int64(len(blob)), Status: "active"}
		cm.ftEmit(p)
		err := uploadChatCiphertext(ctx, cm, id, channelID, storage, blob, &p)
		if err != nil {
			p.Status = "error"
			p.Error = err.Error()
			if errors.Is(err, errTransferCanceled) {
				p.Status = "canceled"
			}
		} else {
			p.Status = "done"
			p.Transferred = p.Total
		}
		cm.ftEmit(p)
		return err
	})
	if err == nil {
		a.recordUploadedAttachment(cm, channelID, token, name)
	}
	return token, err
}

type chatUploadCancellation struct {
	net.Conn
	cancel context.CancelFunc
}

func (c *chatUploadCancellation) Close() error { c.cancel(); return nil }

func uploadChatCiphertext(ctx context.Context, cm *connManager, id string, channelID int64, storage string, blob []byte, p *ftProgress) error {
	if ctx.Err() != nil {
		return errTransferCanceled
	}
	frame, err := cm.request(netproto.MsgFileTransferInit, netproto.MsgFileTransferInitResponse,
		netproto.FileTransferInit{ChannelID: channelID, Direction: "upload", Name: storage, Size: int64(len(blob))}, 10*time.Second)
	if err != nil {
		return err
	}
	var init netproto.FileTransferInitResponse
	if err := json.Unmarshal(frame.Payload, &init); err != nil {
		return err
	}
	endpoint, err := cm.ftTarget(init)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return errTransferCanceled
	}
	return cm.ftUploadProgress(id, endpoint, init.Token, init.TransferID, blob, p, func() bool { return ctx.Err() != nil })
}
