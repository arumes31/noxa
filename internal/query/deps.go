// Package query implements a TS3-style ServerQuery interface: a raw TCP,
// line-based text protocol for headless administration and bot integration.
//
// Protocol flavor (TS3-like, simplified):
//   - On connect the server sends a greeting banner.
//   - Commands are one per line: a command name followed by key=value pairs
//     (login takes positional arguments instead).
//   - Responses are zero or more result lines, then a final
//     "error id=<n> msg=<text>" line; id=0 means success.
//   - Values are escaped TS3-style (see protocol.go).
//
// The Server talks to the rest of voicex through the Backend interface only;
// production wiring lives in cmd/server/main.go.
package query

// ChannelInfo describes a channel (channellist/channelinfo rows).
type ChannelInfo struct {
	ChannelID   int64
	ParentID    int64
	Name        string
	Topic       string
	Type        int // 0=temporary, 1=semi-permanent, 2=permanent
	MaxClients  int // 0 = unlimited
	ClientCount int
	// Per-channel Opus audio quality (0 bitrate = server default 32k).
	OpusBitrate     int
	OpusFEC         bool
	OpusDTX         bool
	OpusStereo      bool
	SlowModeSeconds int
}

// Backend is the roles-v1 authority shared by the integration transports.
// Additional operations use focused role interfaces in role_*.go.
type Backend interface {
	RoleIntegrationBackend
}
