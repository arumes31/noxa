package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
	"noxa/internal/webrtc"
)

type serverSettingsBatch interface {
	SetServerSettings(context.Context, map[string]string, uint32) error
}

type mediaLimitsCommitter interface {
	CommitVideoLimits(context.Context, int, webrtc.VideoBounds, func(context.Context) error) error
}

func (s *TCPServer) supportsMediaLimitsManagement() bool {
	if s.deps == nil || s.deps.Authority == nil {
		return false
	}
	_, hasSettings := s.deps.Chat.(serverSettingsBatch)
	_, hasMedia := s.deps.Voice.(mediaLimitsCommitter)
	return hasSettings && hasMedia
}

// saveMediaLimits requires the caller's ManageServer authorization lease.
// The same save gate protects ordinary server settings and media publication.
// Management transports authorize before calling it and acknowledge this
// returned revision, not a later snapshot.
func (s *TCPServer) saveMediaLimits(ctx context.Context, actor string, limits netproto.MediaLimits) (netproto.MediaLimitsChanged, error) {
	if !limits.Valid() {
		return netproto.MediaLimitsChanged{}, authorization.ErrRoleInvalid
	}
	if s.deps == nil {
		return netproto.MediaLimitsChanged{}, authorization.ErrAuthorizationUnavailable
	}
	batch, ok := s.deps.Chat.(serverSettingsBatch)
	if !ok {
		return netproto.MediaLimitsChanged{}, authorization.ErrAuthorizationUnavailable
	}
	media, ok := s.deps.Voice.(mediaLimitsCommitter)
	if !ok {
		return netproto.MediaLimitsChanged{}, authorization.ErrAuthorizationUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.configSaveMu.LockContext(ctx); err != nil {
		return netproto.MediaLimitsChanged{}, err
	}
	defer s.configSaveMu.Unlock()
	current := s.mediaLimitsSnapshot()
	if current.MediaLimits != limits && current.Revision == ^uint64(0) {
		return netproto.MediaLimitsChanged{}, errors.New("media limit revision exhausted")
	}
	values := map[string]string{
		"video_max_bitrate": strconv.Itoa(limits.VideoMaxBitrate),
		"video_max_width":   strconv.Itoa(limits.VideoMaxWidth),
		"video_max_height":  strconv.Itoa(limits.VideoMaxHeight),
	}
	err := media.CommitVideoLimits(ctx, limits.VideoMaxBitrate, webrtc.VideoBounds{Width: limits.VideoMaxWidth, Height: limits.VideoMaxHeight}, func(ctx context.Context) error {
		return batch.SetServerSettings(ctx, values, 0)
	})
	if err != nil {
		return netproto.MediaLimitsChanged{}, fmt.Errorf("saving media limits: %w", err)
	}
	// Once persistence confirms success, request cancellation must not suppress
	// application/publication. Validity and revision capacity were checked above
	// while holding configSaveMu, so this step cannot fail for this request.
	if err := s.publishMediaLimitsLocked(limits); err != nil {
		return netproto.MediaLimitsChanged{}, err
	}
	result := s.mediaLimitsSnapshot()
	auditCtx, stopAudit := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer stopAudit()
	s.audit(auditCtx, actor, "media_limits_set", "server", fmt.Sprintf("video_bitrate=%d width=%d height=%d", limits.VideoMaxBitrate, limits.VideoMaxWidth, limits.VideoMaxHeight))
	return result, nil
}
