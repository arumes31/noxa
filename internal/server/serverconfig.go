package server

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

// LoadPersistedServerConfig applies administrator-managed runtime settings to
// the startup configuration. Missing keys retain config.yaml/environment
// values, so the UI becomes authoritative only after an administrator saves.
func LoadPersistedServerConfig(ctx context.Context, cfg *config.Config, settings interface {
	GetPlainServerSettings(context.Context, []string) (map[string]string, error)
}) error {
	loaded := *cfg
	type setting struct {
		key   string
		apply func(string) error
	}
	parseInt := func(dst *int, min, max int) func(string) error {
		return func(value string) error {
			n, err := strconv.Atoi(value)
			if err != nil || n < min || n > max {
				return fmt.Errorf("invalid persisted integer %q", value)
			}
			*dst = n
			return nil
		}
	}
	parseBool := func(dst *bool) func(string) error {
		return func(value string) error {
			v, err := strconv.ParseBool(value)
			if err != nil {
				return fmt.Errorf("invalid persisted boolean %q", value)
			}
			*dst = v
			return nil
		}
	}
	entries := []setting{
		{"max_clients_override", parseInt(&loaded.MaxClients, 0, 100_000)},
		{"client_timeout_seconds", parseInt(&loaded.ClientTimeoutSeconds, 30, 86_400)},
		{"default_opus_bitrate", parseInt(&loaded.DefaultOpusBitrate, 6_000, 510_000)},
		{"default_opus_fec", parseBool(&loaded.DefaultOpusFEC)},
		{"default_opus_dtx", parseBool(&loaded.DefaultOpusDTX)},
		{"default_opus_stereo", parseBool(&loaded.DefaultOpusStereo)},
	}
	mediaEntries := []setting{
		{"video_max_bitrate", parseInt(&loaded.VideoMaxBitrate, 0, 100_000_000)},
		{"video_max_width", parseInt(&loaded.VideoMaxWidth, 0, 16383)},
		{"video_max_height", parseInt(&loaded.VideoMaxHeight, 0, 16383)},
	}
	keys := make([]string, 0, len(entries)+len(mediaEntries))
	for _, entry := range entries {
		keys = append(keys, entry.key)
	}
	for _, entry := range mediaEntries {
		keys = append(keys, entry.key)
	}
	values, err := settings.GetPlainServerSettings(ctx, keys)
	if err != nil {
		return fmt.Errorf("loading persisted server configuration: %w", err)
	}
	for _, entry := range entries {
		if value := values[entry.key]; value != "" {
			if err := entry.apply(value); err != nil {
				return fmt.Errorf("loading %s: %w", entry.key, err)
			}
		}
	}
	found := 0
	for _, entry := range mediaEntries {
		if value, exists := values[entry.key]; exists {
			found++
			if err := entry.apply(value); err != nil {
				return fmt.Errorf("loading %s: %w", entry.key, err)
			}
		}
	}
	if found != 0 {
		limits := netproto.MediaLimits{VideoMaxBitrate: loaded.VideoMaxBitrate, VideoMaxWidth: loaded.VideoMaxWidth, VideoMaxHeight: loaded.VideoMaxHeight}
		if found != len(mediaEntries) || !limits.Valid() {
			return fmt.Errorf("invalid or incomplete persisted media limits")
		}
	}
	*cfg = loaded
	return nil
}

func (s *TCPServer) serverConfig() netproto.ServerConfig {
	mediaLimitsManagement := s.supportsMediaLimitsManagement()
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return netproto.ServerConfig{
		MaxClients: s.cfg.MaxClients, ClientTimeoutSeconds: s.cfg.ClientTimeoutSeconds,
		OpusBitrate: s.cfg.DefaultOpusBitrate, OpusFEC: s.cfg.DefaultOpusFEC,
		OpusDTX: s.cfg.DefaultOpusDTX, OpusStereo: s.cfg.DefaultOpusStereo,
		MediaLimitsManagement: mediaLimitsManagement,
	}
}

func (s *TCPServer) handleServerConfigQuery(ctx context.Context, client *Client, f *netproto.Frame) error {
	if err := netproto.Decode(f, &netproto.ServerConfigQuery{}); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_config_query: "+err.Error())
	}
	return s.roleAction(ctx, client, 0, authorization.ManageServer, func(context.Context) error {
		return s.writeMessage(client, netproto.MsgServerConfigResponse, s.serverConfig())
	})
}

func (s *TCPServer) handleServerConfigSet(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.ServerConfig
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed server_config_set: "+err.Error())
	}
	return s.roleAction(ctx, client, 0, authorization.ManageServer, func(ctx context.Context) error {
		return s.applyServerConfig(ctx, client, msg)
	})
}

func (s *TCPServer) applyServerConfig(ctx context.Context, client *Client, msg netproto.ServerConfig) error {
	result, err := s.saveServerConfig(ctx, client.uniqueID(), msg)
	if errors.Is(err, authorization.ErrRoleInvalid) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid server configuration limits")
	}
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "saving server configuration failed")
	}
	// A saved result must not be replaced by a later concurrent configuration.
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return s.writeMessageInContext(replyCtx, client, netproto.MsgServerConfigResponse, result)
}

// saveServerConfig is called under the caller's management authorization lease.
// Serialize persistence and publication without holding configMu across I/O.
func (s *TCPServer) saveServerConfig(ctx context.Context, actor string, msg netproto.ServerConfig) (netproto.ServerConfig, error) {
	if !msg.ValidLimits() {
		return netproto.ServerConfig{}, authorization.ErrRoleInvalid
	}
	if s.deps == nil || s.deps.Chat == nil {
		return netproto.ServerConfig{}, authorization.ErrAuthorizationUnavailable
	}
	values := map[string]string{
		"max_clients_override":   strconv.Itoa(msg.MaxClients),
		"client_timeout_seconds": strconv.Itoa(msg.ClientTimeoutSeconds),
		"default_opus_bitrate":   strconv.Itoa(msg.OpusBitrate),
		"default_opus_fec":       strconv.FormatBool(msg.OpusFEC),
		"default_opus_dtx":       strconv.FormatBool(msg.OpusDTX),
		"default_opus_stereo":    strconv.FormatBool(msg.OpusStereo),
	}
	batch, ok := s.deps.Chat.(interface {
		SetServerSettings(context.Context, map[string]string, uint32) error
	})
	if !ok {
		return netproto.ServerConfig{}, authorization.ErrAuthorizationUnavailable
	}
	if err := s.configSaveMu.LockContext(ctx); err != nil {
		return netproto.ServerConfig{}, err
	}
	defer s.configSaveMu.Unlock()
	if err := batch.SetServerSettings(ctx, values, 0); err != nil {
		return netproto.ServerConfig{}, fmt.Errorf("saving server configuration: %w", err)
	}
	s.configMu.Lock()
	s.cfg.MaxClients = msg.MaxClients
	s.cfg.ClientTimeoutSeconds = msg.ClientTimeoutSeconds
	s.cfg.DefaultOpusBitrate = msg.OpusBitrate
	s.cfg.DefaultOpusFEC = msg.OpusFEC
	s.cfg.DefaultOpusDTX = msg.OpusDTX
	s.cfg.DefaultOpusStereo = msg.OpusStereo
	s.configMu.Unlock()
	auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	s.audit(auditCtx, actor, "server_config_set", "server", fmt.Sprintf("max_clients=%d timeout=%d opus=%d", msg.MaxClients, msg.ClientTimeoutSeconds, msg.OpusBitrate))
	// This is an output-only capability. Never echo a value supplied by a
	// client, and keep save acknowledgements consistent with configuration reads.
	msg.MediaLimitsManagement = s.supportsMediaLimitsManagement()
	return msg, nil
}
