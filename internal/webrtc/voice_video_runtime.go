package webrtc

import "context"

// SetVideoLimits coordinates codec bounds for new/rebuilt peers with the
// router's live packet limits. Callers using Voice must update limits through
// this method, rather than independently mutating its engine and router.
// Invalid values or a closed engine leave both configurations unchanged.
//
// Concurrent updates are serialized. Peers created during an update can still
// use its previous codec configuration; peers created after it returns use the
// new one. Existing peers retain their codecs until rebuilt with HandleOffer.
// Client notification, capture changes and rebuild scheduling belong to the
// caller. Like Router.SetVideoLimits, this waits for router output writes and
// does not flush downstream Pion transport buffers.
func (v *Voice) SetVideoLimits(bitsPerSecond int, bounds VideoBounds) error {
	return v.CommitVideoLimits(context.Background(), bitsPerSecond, bounds, nil)
}

// CommitVideoLimits prepares codec and relay changes, drains active router
// output, and invokes commit before installing the new settings. Canceled lock
// waits and failed preparation/persistence leave both runtime settings intact.
// A nil commit is an in-memory update. A successful commit is installed even
// if ctx has since been canceled; cancellation cannot undo persisted values.
//
// commit runs synchronously with the engine and video-output gates held. It
// must honor ctx, report success only on confirmed persistence, and must not
// reenter the engine, router or voice. Callers publish notifications afterward.
// A deadline bounds waiting for stalled output, not WriteRTP itself or any
// downstream congestion-control/NACK buffers. No locking goroutine survives
// a canceled wait.
func (v *Voice) CommitVideoLimits(ctx context.Context, bitsPerSecond int, bounds VideoBounds, commit func(context.Context) error) error {
	if err := validateVideoLimits(bitsPerSecond, bounds); err != nil {
		return err
	}
	if err := v.videoLimitsMu.LockContext(ctx); err != nil {
		return err
	}
	defer v.videoLimitsMu.Unlock()
	if err := v.router.videoPolicyMu.LockContext(ctx); err != nil {
		return err
	}
	defer v.router.videoPolicyMu.Unlock()
	if err := v.engine.mu.LockContext(ctx); err != nil {
		return err
	}
	defer v.engine.mu.Unlock()
	api, err := v.engine.prepareVideoBoundsLocked(bounds)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if commit != nil {
		if err := commit(ctx); err != nil {
			return err
		}
	}
	v.engine.api, v.engine.videoBounds = api, bounds
	return v.router.setVideoLimitsLocked(bitsPerSecond, bounds)
}
