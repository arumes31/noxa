//go:build !windows

package nativecapture

// AudioSession is unavailable off Windows.
type AudioSession struct{ Mode AudioMode }

// StartAudio always fails off Windows.
func StartAudio(AudioMode, uint64, func(AudioPacket)) (*AudioSession, error) {
	return nil, ErrAudioUnsupported
}

// Close does nothing off Windows.
func (*AudioSession) Close() {}
