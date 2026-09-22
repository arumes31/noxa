package server

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// Internal queue marker, never a client event. The writer reads current limits
// rather than replaying a possibly obsolete configuration from the queue.
const mediaLimitsNotification = `{"type":"_media_limits_changed"}`

func (s *TCPServer) mediaLimitsSnapshot() netproto.MediaLimitsChanged {
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return netproto.MediaLimitsChanged{
		Revision: s.mediaLimitsRevision,
		MediaLimits: netproto.MediaLimits{VideoMaxBitrate: s.cfg.VideoMaxBitrate,
			VideoMaxWidth: s.cfg.VideoMaxWidth, VideoMaxHeight: s.cfg.VideoMaxHeight},
	}
}

// publishMediaLimitsLocked is the post-commit publication step. The caller
// holds configSaveMu and must first apply/persist these exact limits. It must
// also check revision exhaustion before starting those effects. This helper
// alone does not change enforcement or persistence and is not a management API.
func (s *TCPServer) publishMediaLimitsLocked(limits netproto.MediaLimits) error {
	if !limits.Valid() {
		return authorization.ErrRoleInvalid
	}
	s.configMu.Lock()
	current := netproto.MediaLimits{VideoMaxBitrate: s.cfg.VideoMaxBitrate,
		VideoMaxWidth: s.cfg.VideoMaxWidth, VideoMaxHeight: s.cfg.VideoMaxHeight}
	if current == limits {
		s.configMu.Unlock()
		return nil
	}
	if s.mediaLimitsRevision == ^uint64(0) {
		s.configMu.Unlock()
		return errors.New("media limit revision exhausted")
	}
	s.cfg.VideoMaxBitrate, s.cfg.VideoMaxWidth, s.cfg.VideoMaxHeight = limits.VideoMaxBitrate, limits.VideoMaxWidth, limits.VideoMaxHeight
	s.mediaLimitsRevision++
	s.configMu.Unlock()
	s.notifyMediaLimitsChanged()
	return nil
}

func (s *TCPServer) notifyMediaLimitsChanged() {
	revision := s.mediaLimitsSnapshot().Revision
	s.mu.RLock()
	clients := make([]*Client, 0, len(s.clients))
	for _, client := range s.clients {
		clients = append(clients, client)
	}
	s.mu.RUnlock()
	for _, client := range clients {
		if !client.beginMediaLimitsDelivery(revision, 5*time.Second) {
			continue
		}
		var err error
		if s.deps == nil || s.deps.Broadcast == nil {
			err = errors.New("media notification queue unavailable")
		} else {
			err = s.deps.Broadcast.BroadcastToClient(client.ID, []byte(mediaLimitsNotification))
		}
		if err != nil {
			s.logger.Warn("closing connection after media notification queue failure", zap.String("client_id", client.ID), zap.Error(err))
			client.stopMediaLimitsDelivery()
			_ = client.Conn.Close()
		}
	}
}

func (s *TCPServer) writeAuthenticationResponse(ctx context.Context, client *Client, response netproto.AuthResponse) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.wmu.LockContext(ctx); err != nil {
		return err
	}
	defer client.wmu.Unlock()
	// Queue registration has already been attempted. Updates before this
	// point are covered by the snapshot below; updates afterward must enqueue
	// successfully or close the connection, including a failed registration.
	client.mu.Lock()
	client.mediaLimitsReady = response.OK
	client.mu.Unlock()
	limits := s.mediaLimitsSnapshot()
	response.MediaLimits, response.MediaLimitsRevision = &limits.MediaLimits, limits.Revision
	frame, err := netproto.Encode(netproto.MsgAuthResponse, response)
	if err != nil {
		return err
	}
	if err := s.writeFrameLocked(ctx, client, frame); err != nil {
		return err
	}
	client.completeMediaLimitsDelivery(limits.Revision)
	return nil
}

func (s *TCPServer) writeCurrentMediaLimits(ctx context.Context, client *Client) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := client.wmu.LockContext(ctx); err != nil {
		return err
	}
	defer client.wmu.Unlock()
	if !client.isAuthed() {
		return nil
	}
	limits := s.mediaLimitsSnapshot()
	if limits.Revision == 0 {
		return nil
	}
	frame, err := netproto.Encode(netproto.MsgMediaLimitsChanged, limits)
	if err != nil {
		return err
	}
	if err := s.writeFrameLocked(ctx, client, frame); err != nil {
		return err
	}
	client.completeMediaLimitsDelivery(limits.Revision)
	return nil
}

func (c *Client) beginMediaLimitsDelivery(revision uint64, timeout time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.authed || c.revoked || !c.mediaLimitsReady || revision == 0 {
		return false
	}
	c.mediaLimitsPending = max(c.mediaLimitsPending, revision)
	if c.mediaLimitsTimer == nil {
		c.mediaLimitsTimerGeneration++
		generation := c.mediaLimitsTimerGeneration
		c.mediaLimitsTimer = time.AfterFunc(timeout, func() { c.expireMediaLimitsDelivery(generation) })
	}
	return true
}

func (c *Client) completeMediaLimitsDelivery(revision uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if revision < c.mediaLimitsPending {
		return
	}
	c.mediaLimitsPending = 0
	if c.mediaLimitsTimer != nil {
		c.mediaLimitsTimer.Stop()
		c.mediaLimitsTimer = nil
	}
}

func (c *Client) expireMediaLimitsDelivery(generation uint64) {
	c.mu.Lock()
	if c.mediaLimitsPending == 0 || c.mediaLimitsTimerGeneration != generation {
		c.mu.Unlock()
		return
	}
	c.mediaLimitsReady = false
	c.mediaLimitsPending = 0
	if c.mediaLimitsTimer != nil {
		c.mediaLimitsTimer.Stop()
		c.mediaLimitsTimer = nil
	}
	c.mu.Unlock()
	_ = c.Conn.Close()
}

func (c *Client) stopMediaLimitsDelivery() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.mediaLimitsReady = false
	c.mediaLimitsPending = 0
	if c.mediaLimitsTimer != nil {
		c.mediaLimitsTimer.Stop()
		c.mediaLimitsTimer = nil
	}
}
