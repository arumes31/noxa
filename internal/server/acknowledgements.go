package server

import (
	"context"
	"time"

	"noxa/internal/netproto"
)

// writeCommittedReply gives an already completed action a fresh reply window.
// Its deadline includes waiting for the socket writer, not just transmission.
func (s *TCPServer) writeCommittedReply(client *Client, kind netproto.MessageType, message any) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.writeMessageInContext(ctx, client, kind, message)
}
