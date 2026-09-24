package webrtc

import (
	"bytes"
	"errors"
	"image/jpeg"
	"time"
)

const MaxPreviewBytes = 48 * 1024

type videoPreview struct {
	jpeg     []byte
	captured time.Time
}

type previewBudget struct{ attempt time.Time }

// SetVideoPreview accepts bounded JPEGs from an authenticated publisher's
// existing capture. It never receives continuous media on a viewer's behalf.
func (r *Router) SetVideoPreview(publisher, slot string, generation uint64, data []byte) error {
	if slot != SlotScreen || len(data) == 0 || len(data) > MaxPreviewBytes {
		return errors.New("invalid video preview")
	}
	key := publicationKey{publisher, slot}
	r.watchMu.Lock()
	if generation == 0 || r.publications[key] != generation {
		r.watchMu.Unlock()
		return ErrVideoPublication
	}
	budget := r.previewBudgets[publisher]
	if time.Since(budget.attempt) < 2*time.Second {
		r.watchMu.Unlock()
		return errors.New("video preview update too frequent")
	}
	if r.previewBudgets == nil {
		r.previewBudgets = make(map[string]previewBudget)
	}
	budget.attempt = time.Now()
	r.previewBudgets[publisher] = budget
	r.watchMu.Unlock()
	config, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 640 || config.Height > 360 {
		return errors.New("invalid video preview dimensions")
	}
	if _, err := jpeg.Decode(bytes.NewReader(data)); err != nil {
		return errors.New("invalid video preview JPEG")
	}
	r.watchMu.Lock()
	defer r.watchMu.Unlock()
	if generation == 0 || r.publications[key] != generation {
		return ErrVideoPublication
	}
	if time.Since(r.previews[key].captured) < 110*time.Second {
		return errors.New("video preview update too frequent")
	}
	if r.previews == nil {
		r.previews = make(map[publicationKey]videoPreview)
	}
	r.previews[key] = videoPreview{append([]byte(nil), data...), time.Now()}
	return nil
}

func (r *Router) VideoPreview(subscriber, publisher, slot string, generation uint64) ([]byte, int64, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.clientChan[subscriber] <= 0 || r.clientChan[subscriber] != r.clientChan[publisher] || !r.publisherAllowedLocked(subscriber, publisher) {
		return nil, 0, ErrVideoPublication
	}
	r.watchMu.RLock()
	defer r.watchMu.RUnlock()
	key := publicationKey{publisher, slot}
	if generation == 0 || r.publications[key] != generation {
		return nil, 0, ErrVideoPublication
	}
	preview := r.previews[key]
	if preview.captured.IsZero() {
		return nil, 0, nil
	}
	return append([]byte(nil), preview.jpeg...), preview.captured.UnixMilli(), nil
}

func (v *Voice) SetVideoPreview(publisher, slot string, generation uint64, data []byte) error {
	return v.router.SetVideoPreview(publisher, slot, generation, data)
}
func (v *Voice) VideoPreview(subscriber, publisher, slot string, generation uint64) ([]byte, int64, error) {
	return v.router.VideoPreview(subscriber, publisher, slot, generation)
}
