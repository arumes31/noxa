package server

import (
	"context"
	"noxa/internal/linkpreview"
	"noxa/internal/netproto"
)

func (s *TCPServer) handleLinkPreview(ctx context.Context, client *Client, f *netproto.Frame) error {
	var req netproto.LinkPreviewRequest
	if netproto.Decode(f, &req) != nil || len(req.Resource) > 2048 || len(req.Provider) > 20 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid link preview")
	}
	// One in-flight fetch per user keeps a session from holding every global slot.
	key := client.uniqueID()
	if _, busy := s.previewInFlight.LoadOrStore(key, struct{}{}); busy {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "link preview busy")
	}
	defer s.previewInFlight.Delete(key)
	s.previewOnce.Do(func() { s.previewService = linkpreview.New() })
	result, err := s.previewService.Preview(ctx, req)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "link preview unavailable")
	}
	if !client.isAuthed() || client.rulesBlocked() {
		return nil
	}
	return s.writeMessage(client, netproto.MsgLinkPreviewResult, result)
}
