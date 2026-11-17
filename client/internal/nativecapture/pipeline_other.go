//go:build !windows

package nativecapture

// Session is unavailable off Windows.
type Session struct{}

// Start always fails off Windows.
func Start(Config, func(Frame)) (*Session, error) { return nil, ErrUnsupported }

func (*Session) RequestKeyframe()    {}
func (*Session) SetPaused(bool)      {}
func (*Session) SetFPS(int)          {}
func (*Session) SetBitrate(int)      {}
func (*Session) SetMaxSize(int, int) {}
func (*Session) Fatal() <-chan error { return nil }
func (*Session) Info() EncoderInfo   { return EncoderInfo{} }
func (*Session) Stats() Stats        { return Stats{} }
func (*Session) Close()              {}

// Monitors is empty off Windows.
func Monitors() []Monitor { return nil }

// Windows is empty off Windows.
func Windows() []Window { return nil }

// ForegroundWindow is 0 off Windows.
func ForegroundWindow() uint64 { return 0 }

// WindowState reports no window off Windows.
func WindowState(uint64) (bool, bool) { return false, false }
