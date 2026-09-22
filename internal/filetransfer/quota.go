package filetransfer

import (
	"context"
	"fmt"
)

// checkMoveQuota runs under fileOpsMu, before changing either metadata or disk.
// Moving the last reference releases source usage, while moving one of several
// identical references can increase the uploader's total across channels.
func (s *Server) checkMoveQuota(ctx context.Context, source int64, folder, name string, target int64) error {
	if s.cfg.ChannelQuotaMB <= 0 && s.cfg.UserQuotaMB <= 0 {
		return nil
	}
	rec, err := s.store.GetFile(ctx, source, folder, name)
	if err != nil {
		return err
	}
	channelContent, uploaderContent, err := s.store.FileContentUsage(ctx, target, rec.SHA256, rec.Uploader)
	if err != nil {
		return fmt.Errorf("checking target content usage: %w", err)
	}
	if s.cfg.ChannelQuotaMB > 0 {
		q, err := s.ChannelQuotaState(ctx, target)
		if err != nil {
			return fmt.Errorf("checking target channel quota: %w", err)
		}
		if q.Exceeded(max(0, rec.Size-channelContent)) {
			return fmt.Errorf("%w: %d MiB", ErrQuotaExceeded, s.cfg.ChannelQuotaMB)
		}
	}
	if s.cfg.UserQuotaMB > 0 {
		q, err := s.UploaderQuotaState(ctx, rec.Uploader, s.cfg.UserQuotaMB)
		if err != nil {
			return fmt.Errorf("checking upload quota: %w", err)
		}
		remaining, err := s.store.UploaderContentUsageExcept(ctx, source, rec.SHA256, rec.Uploader, folder, name)
		if err != nil {
			return fmt.Errorf("checking remaining source content: %w", err)
		}
		// Apply the released source bytes first so even a limit reduced below
		// current usage can be satisfied by a move that consolidates content.
		q.Used -= max(0, rec.Size-remaining)
		if q.Exceeded(max(0, rec.Size-uploaderContent)) {
			return fmt.Errorf("%w: %d MiB", ErrUploaderQuotaExceeded, s.cfg.UserQuotaMB)
		}
	}
	return nil
}
