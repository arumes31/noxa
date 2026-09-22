package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/broadcast"
	"noxa/internal/channels"
	"noxa/internal/chatcrypto"
	"noxa/internal/config"
	"noxa/internal/eventbus"
	"noxa/internal/filetransfer"
	"noxa/internal/grpcserver"
	"noxa/internal/health"
	"noxa/internal/logging"
	"noxa/internal/metrics"
	"noxa/internal/netproto"
	"noxa/internal/query"
	"noxa/internal/recorder"
	"noxa/internal/redisx"
	"noxa/internal/rules"
	"noxa/internal/safecast"
	"noxa/internal/server"
	"noxa/internal/state"
	"noxa/internal/store"
	"noxa/internal/tlscert"
	"noxa/internal/turn"
	"noxa/internal/version"
	"noxa/internal/webrtc"
)

func main() {
	var err error
	if len(os.Args) > 1 && os.Args[1] == "rewrap-chat-keys" {
		err = rewrapChatKeys()
	} else {
		err = run()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "noxa: %v\n", err)
		os.Exit(1)
	}
}

// hasFlag reports whether the argument list carries name.
func hasFlag(name string) bool {
	for _, a := range os.Args[1:] {
		if a == name {
			return true
		}
	}
	return false
}

// syncLogger flushes buffered log entries. Zap commonly gets EINVAL when
// syncing a console stream, which is not a durability failure.
func syncLogger(logger *zap.Logger) {
	if err := logger.Sync(); err != nil && !errors.Is(err, syscall.EINVAL) {
		fmt.Fprintf(os.Stderr, "noxa: syncing logger: %v\n", err)
	}
}

func markChannelIcons(fileRoot string, manager *state.Manager) (int, error) {
	ids, err := server.DiscoverChannelIconIDs(fileRoot)
	if err != nil {
		return 0, err
	}
	marked := 0
	for _, id := range ids {
		if manager.SetChannelHasIcon(id, true) {
			marked++
		}
	}
	return marked, nil
}

func recorderConfig(cfg config.RecordingConfig) recorder.Config {
	return recorder.Config{
		Enabled:         cfg.Enabled,
		Dir:             cfg.Dir,
		FFmpegPath:      cfg.FFmpegPath,
		Format:          cfg.Format,
		VideoArgs:       append([]string(nil), cfg.VideoArgs...),
		AudioArgs:       append([]string(nil), cfg.AudioArgs...),
		MaxConcurrent:   cfg.MaxConcurrent,
		WindowsACLReady: cfg.WindowsACLReady,
	}
}

type serviceExit struct {
	name string
	err  error
}

// activeRoleBackend prevents prepared or interrupted setup state from becoming
// a serving authority. Mutations still use the store's transactional writer.
type activeRoleBackend struct{ *store.Store }

func (b activeRoleBackend) RolePolicy(ctx context.Context) (authorization.RolePolicy, error) {
	return b.ActiveRolePolicy(ctx)
}

// startService reports every service exit, including an unexpected nil error.
// The caller provides a channel large enough for every launched service so
// shutdown cannot strand a reporter after the first exit wins the select.
func startService(exits chan<- serviceExit, name string, start func() error) {
	go func() {
		exits <- serviceExit{name: name, err: start()}
	}()
}

// registerPprofEndpoints adds the explicit runtime diagnostic handlers to the
// health server's private mux. It intentionally never uses the default mux:
// enabling pprof does not expose it on unrelated HTTP listeners.
func registerPprofEndpoints(healthServer *health.Server, enabled bool) {
	if !enabled {
		return
	}
	healthServer.HandleLocalGET("/debug/pprof/", http.HandlerFunc(pprof.Index))
	healthServer.HandleLocalGET("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
	healthServer.HandleLocalGET("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
	healthServer.HandleLocalGET("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
	healthServer.HandleLocalGET("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
}

func unexpectedServiceExit(exit serviceExit) error {
	if exit.err == nil {
		return fmt.Errorf("%s exited unexpectedly", exit.name)
	}
	return fmt.Errorf("%s exited unexpectedly: %w", exit.name, exit.err)
}

func joinShutdownError(current error, service string, err error) error {
	if err == nil || errors.Is(err, net.ErrClosed) {
		return current
	}
	return errors.Join(current, fmt.Errorf("shutting down %s: %w", service, err))
}

// rewrapChatKeys re-wraps every stored scope key generation under the newest
// KEK. It is the ONLY thing that ever rewrites wrapped_key: the scope keys
// themselves are unchanged, so every stored message still opens (91).
func rewrapChatKeys() (retErr error) {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	logger, err := logging.New(cfg.DevMode, cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}
	defer syncLogger(logger)
	lease, err := store.AcquireRoleProcessLease(context.Background(), cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("acquiring offline role process lease: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lease.Close()) }()

	dbStore, err := store.New(cfg.DatabaseURL, logger,
		cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, cfg.DBConnMaxLifetime)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() {
		if err := dbStore.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("closing store: %w", err))
		}
	}()
	if err := dbStore.CheckFreshInstall(context.Background()); err != nil {
		return err
	}

	ring, err := chatcrypto.LoadKEKRing(cfg.ChatMasterKeyFile, os.Getenv("NOXA_CHAT_MASTER_KEY"), false)
	if err != nil {
		return fmt.Errorf("loading chat master key: %w", err)
	}
	newest := ring.NewestID()

	type pending struct {
		scope int64
		keyID int64
		key   [32]byte
	}
	ctx := context.Background()
	todo, err := func() (out []pending, retErr error) {
		rows, err := dbStore.DB().QueryContext(ctx,
			`SELECT scope_id, key_id, wrapped_key, kek_id FROM chat_scope_keys WHERE kek_id <> $1`, int32(newest))
		if err != nil {
			return nil, fmt.Errorf("listing scope keys: %w", err)
		}
		defer func() {
			if err := rows.Close(); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("closing scope key rows: %w", err))
			}
		}()
		for rows.Next() {
			var (
				p       pending
				wrapped []byte
				kekID   int64
			)
			if err := rows.Scan(&p.scope, &p.keyID, &wrapped, &kekID); err != nil {
				return nil, fmt.Errorf("scanning scope key: %w", err)
			}
			storedKEKID, err := safecast.Int64ToUint16(kekID)
			if err != nil {
				return nil, fmt.Errorf("scope %d generation %d has invalid kek id %d: %w", p.scope, p.keyID, kekID, err)
			}
			key, err := ring.Unwrap(storedKEKID, wrapped)
			if err != nil {
				return nil, fmt.Errorf("unwrapping scope %d generation %d: %w", p.scope, p.keyID, err)
			}
			p.key = key
			out = append(out, p)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("listing scope keys: %w", err)
		}
		return out, nil
	}()
	if err != nil {
		return err
	}

	for _, p := range todo {
		kekID, wrapped, err := ring.Wrap(p.key)
		if err != nil {
			return fmt.Errorf("wrapping scope %d generation %d: %w", p.scope, p.keyID, err)
		}
		if _, err := dbStore.DB().ExecContext(ctx,
			`UPDATE chat_scope_keys SET wrapped_key = $1, kek_id = $2 WHERE scope_id = $3 AND key_id = $4`,
			wrapped, int32(kekID), p.scope, p.keyID); err != nil {
			return fmt.Errorf("rewrapping scope %d generation %d: %w", p.scope, p.keyID, err)
		}
	}
	logger.Info("chat scope keys rewrapped",
		zap.Int("generations", len(todo)),
		zap.Uint16("kek_id", newest),
		zap.String("fingerprint", ring.Fingerprint()),
	)
	return nil
}

// resetChatKeys abandons all chat history: it drops every scope key
// generation and tombstones every message. It exists for the operator who
// lost the master key, for whom the alternative is a server that never
// starts again. It always asks first.
func resetChatKeys(ctx context.Context, dbStore *store.Store, logger *zap.Logger) error {
	fmt.Fprintln(os.Stderr, "--reset-chat-keys DESTROYS all chat history: every scope key generation")
	fmt.Fprintln(os.Stderr, "is dropped and every stored message is tombstoned. This cannot be undone.")
	fmt.Fprint(os.Stderr, "Type yes to continue: ")
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		return fmt.Errorf("reading the --reset-chat-keys confirmation: %w", err)
	}
	if strings.TrimSpace(strings.ToLower(line)) != "yes" {
		return errors.New("--reset-chat-keys not confirmed")
	}

	tx, err := dbStore.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning chat key reset: %w", err)
	}
	// Tombstone first: a message row referencing a generation that no longer
	// exists would decrypt to nothing, undetectably and forever.
	stmts := []string{
		`UPDATE chat_messages SET body = '', body_enc = '', key_id = 0, deleted_at = COALESCE(deleted_at, NOW())`,
		`UPDATE server_settings SET value = '', key_id = 0 WHERE key IN ('motd', 'announcement')`,
		`DELETE FROM chat_scope_keys`,
		`DELETE FROM chat_scope_seq`,
	}
	for _, q := range stmts {
		if _, err := tx.ExecContext(ctx, q); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("resetting chat keys: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing the chat key reset: %w", err)
	}
	logger.Warn("chat keys reset: all chat history has been tombstoned and every scope key generation dropped")
	return nil
}

func run() (retErr error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	logger, err := logging.New(cfg.DevMode, cfg.LogLevel)
	if err != nil {
		return fmt.Errorf("initializing logger: %w", err)
	}
	defer func() { syncLogger(logger) }()

	// (223) tee log lines into the in-memory ring buffer for `logview`.
	logger = logger.WithOptions(logging.Tee())

	logger.Info("noxa server starting",
		zap.String("version", version.String()),
		zap.String("server_name", cfg.ServerName),
		zap.Bool("dev_mode", cfg.DevMode),
		zap.String("log_level", cfg.LogLevel),
		zap.String("tcp_addr", cfg.TCPAddr),
		zap.String("udp_addr", cfg.UDPAddr),
		zap.String("grpc_addr", cfg.GRPCAddr),
		zap.String("database_url", cfg.RedactedDatabaseURL()),
		zap.String("redis_addr", cfg.RedactedRedisAddr()),
		zap.Int("max_clients", cfg.MaxClients),
	)
	logger.Info("config summary", zap.String("config", cfg.Summary()))
	for _, warning := range cfg.Warnings() {
		logger.Warn("unsafe configuration", zap.String("warning", warning))
	}
	lease, err := store.AcquireRoleProcessLease(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("acquiring role process lease: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, lease.Close()) }()

	// Initialize this version's database before opening any listeners.
	dbStore, err := store.New(cfg.DatabaseURL, logger,
		cfg.DBMaxOpenConns, cfg.DBMaxIdleConns, cfg.DBConnMaxLifetime)
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer func() {
		if err := dbStore.Close(); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("closing store: %w", err))
		}
	}()

	migrationCtx, cancelMigration := context.WithTimeout(ctx, 5*time.Minute)
	err = dbStore.EnsureFreshInstall(migrationCtx)
	if err == nil {
		err = dbStore.MigrateContext(migrationCtx)
	}
	cancelMigration()
	if err != nil {
		return fmt.Errorf("running migrations: %w", err)
	}
	piiCipher, err := store.LoadOrCreatePIICipher(cfg.PIIKeyFile)
	if err != nil {
		return fmt.Errorf("loading PII encryption key: %w", err)
	}
	dbStore.SetPIICipher(piiCipher)
	logger.Info("fresh-version schema ready")

	// (91) chat key material. --reset-chat-keys runs first so an operator who
	// lost the master key can start over instead of being locked out forever;
	// it leaves zero generations behind, which is exactly the state that lets
	// LoadKEKRing create a fresh ring below.
	if hasFlag("--reset-chat-keys") {
		if err := resetChatKeys(context.Background(), dbStore, logger); err != nil {
			return err
		}
	}
	scopeKeyCount, err := dbStore.CountScopeKeys(context.Background())
	if err != nil {
		return fmt.Errorf("counting chat scope keys: %w", err)
	}
	chatKEK, err := chatcrypto.LoadKEKRing(cfg.ChatMasterKeyFile,
		os.Getenv("NOXA_CHAT_MASTER_KEY"), scopeKeyCount == 0)
	if err != nil {
		return fmt.Errorf("loading chat master key: %w", err)
	}
	logger.Warn("chat master key loaded — BACK THIS UP WITH THE DATABASE: losing it destroys all channel and global chat history irreversibly",
		zap.String("file", cfg.ChatMasterKeyFile),
		zap.String("fingerprint", chatKEK.Fingerprint()),
		zap.Int64("scope_key_generations", scopeKeyCount),
	)

	var tcpServer *server.TCPServer
	roleAuthority, err := authorization.NewAuthority(ctx, activeRoleBackend{dbStore},
		func(ctx context.Context, before, after *authorization.RoleEvaluator) error {
			if tcpServer == nil {
				return authorization.ErrAuthorizationUnavailable
			}
			return tcpServer.ReconcileRolePolicy(ctx, before, after)
		})
	if err != nil {
		return fmt.Errorf("loading active roles-v1 authorization (run role-setup activation first): %w", err)
	}

	// Initialize the authentication service. It wraps the store and provides
	// password (Argon2id) and challenge-response (Ed25519) authentication.
	authSvc := auth.New(dbStore, logger)
	logger.Info("auth service ready")

	// Initialize the in-memory state manager. It tracks connected clients,
	// active channels, channel membership, and current speaking states.
	stateManager := state.New(logger)
	initStats := stateManager.Stats()
	logger.Info("state manager initialized",
		zap.Int("clients", initStats.ClientCount),
		zap.Int("channels", initStats.ChannelCount),
		zap.Int("speaking", initStats.SpeakingCount),
	)

	// Initialize the channel manager. It coordinates channel lifecycle across
	// the database and the in-memory state manager, including automatic cleanup
	// of empty temporary channels.
	channelMgr := channels.New(dbStore, stateManager, logger)
	// Enter role mode before loading persisted channels so legacy cleanup timers
	// and resource writers can never run during startup.
	channelMgr.EnableRoleMode(nil)
	// (165) operator-configurable temporary channel lifetime; <= 0 keeps the default.
	channelMgr.SetCleanupDelay(time.Duration(cfg.ChannelTempLifetimeSeconds) * time.Second)
	defer channelMgr.Close()
	cleanupDelay := channels.DefaultCleanupDelay
	if cfg.ChannelTempLifetimeSeconds > 0 {
		cleanupDelay = time.Duration(cfg.ChannelTempLifetimeSeconds) * time.Second
	}
	logger.Info("channel manager ready",
		zap.Duration("cleanup_delay", cleanupDelay),
	)

	// Load persisted channels into the in-memory state so the channel tree is
	// complete before any client connects and requests a snapshot.
	loadedChannels, err := channelMgr.LoadIntoState(context.Background())
	if err != nil {
		return fmt.Errorf("loading channels into state: %w", err)
	}
	logger.Info("persisted channels loaded", zap.Int("count", loadedChannels))

	// Echo test channel (15): ensure the loopback channel exists; publishers
	// in it hear their own audio routed back. Empty name disables it.
	var echoChannelID int64
	if cfg.EchoChannelName != "" {
		for _, ch := range stateManager.ChannelTreeOrdered() {
			if ch.Name == cfg.EchoChannelName {
				echoChannelID = ch.ChannelID
				break
			}
		}
		if echoChannelID == 0 {
			logger.Warn("configured echo channel is missing; loopback stays disabled until the channel is created through roles-v1",
				zap.String("name", cfg.EchoChannelName))
		}
	}

	// Construct bounded metrics before any service starts. Component counters
	// are registered as scrape-time callbacks below as their dependencies are
	// constructed.
	m := metrics.New()
	m.RegisterDBPool(dbStore.DB())
	m.RegisterStateStats(func() (int, int) {
		stats := stateManager.Stats()
		return stats.ClientCount, stats.ChannelCount
	})

	// Initialize the broadcaster. It builds snapshots of the channel tree and
	// active users and delivers them to registered clients via per-client
	// outbound channels.
	broadcaster := broadcast.New(logger, stateManager, broadcast.Observers{
		ObserveSnapshotDuration: m.ObserveBroadcastSnapshot,
		ObserveClientBacklog:    m.ObserveBroadcastClientBacklog,
	})
	defer broadcaster.Close()
	logger.Info("broadcaster ready")

	// (231/232) tap the server-wide event fan-out into the event bus, so bots
	// on the WebSocket and gRPC streams observe exactly what connected clients
	// observe. The bus is lossy by design: a bot that stops reading is dropped,
	// never allowed to slow this call down.
	events := eventbus.New(logger)
	defer events.Close()
	broadcaster.SetEventTap(events.Publish)

	logger.Info("roles-v1 authorization ready")

	// Initialize the Pion WebRTC engine and the voice facade (engine + SFU
	// router) that the TCP control server drives via signaling messages.
	// Load saved defaults before constructing consumers of startup settings.
	if err := server.LoadPersistedServerConfig(context.Background(), cfg, dbStore); err != nil {
		return fmt.Errorf("loading persisted server configuration: %w", err)
	}
	videoBounds := webrtc.VideoBounds{Width: cfg.VideoMaxWidth, Height: cfg.VideoMaxHeight}
	engine, err := webrtc.NewWithVideoBounds(logger, cfg.WebRTC.ICEServers, cfg.WebRTC.EnableAV1, webrtc.NetworkConfig{
		UDPAddr: cfg.WebRTC.UDPAddr, ExternalIPs: cfg.WebRTC.ExternalIPs,
	}, videoBounds)
	if err != nil {
		return fmt.Errorf("initializing webrtc engine: %w", err)
	}
	defer func() {
		if err := engine.Close(); err != nil {
			logger.Warn("WebRTC engine shutdown error", zap.Error(err))
		}
	}()
	voiceRouter, err := webrtc.NewRouterWithVideoBounds(logger, videoBounds)
	if err != nil {
		return fmt.Errorf("configuring video dimension ceiling: %w", err)
	}
	if err := voiceRouter.SetVideoBitrateLimit(cfg.VideoMaxBitrate); err != nil {
		return fmt.Errorf("configuring video bitrate ceiling: %w", err)
	}
	voice := webrtc.NewVoice(engine, voiceRouter, logger)
	m.RegisterWebRTCPeerCount(voice.PeerCount)
	voice.SetEchoChannel(echoChannelID)
	// Per-channel Opus audio configuration (21-25): SDP fmtp rewriting and
	// music-channel talk-gate bypass read the channel's stored settings.
	voice.SetChannelAudioLookup(func(channelID int64) webrtc.ChannelAudio {
		ch, ok := stateManager.GetChannel(channelID)
		if !ok {
			return webrtc.ChannelAudio{}
		}
		return webrtc.ChannelAudio{
			Bitrate: ch.OpusBitrate,
			FEC:     ch.OpusFEC,
			DTX:     ch.OpusDTX,
			Stereo:  ch.OpusStereo,
		}
	})
	logger.Info("voice pipeline ready")

	// Initialize the recorder. It manages ffmpeg subprocesses that record
	// channel streams; it is inert unless recording.enabled is set.
	rec := recorder.NewChannelRecorder(recorderConfig(cfg.Recording), logger, recorder.Observers{
		OnError: m.IncRecordingError,
	})
	m.RegisterRecorderSessionCount(rec.SessionCount)
	defer func() {
		if err := rec.Close(); err != nil {
			logger.Warn("recorder shutdown error", zap.Error(err))
		}
	}()

	// Initialize the optional Redis client. A startup ping failure is degraded
	// rather than fatal, but the client is retained so later readiness probes
	// can observe go-redis reconnecting without a process restart.
	var redisClient *redisx.Client
	if cfg.RedisEnabled && cfg.RedisAddr != "" {
		redisClient, err = redisx.New(redisx.Options{
			Addr:          cfg.RedisAddr,
			Password:      cfg.RedisPassword,
			DialTimeout:   cfg.RedisDialTimeout,
			ReadTimeout:   cfg.RedisReadTimeout,
			WriteTimeout:  cfg.RedisWriteTimeout,
			TLSEnabled:    cfg.RedisTLSEnabled,
			TLSServerName: cfg.RedisTLSServerName,
			TLSCAFile:     cfg.RedisTLSCAFile,
		}, logger)
		if err != nil {
			return fmt.Errorf("configuring Redis client: %w", err)
		}
		defer func() {
			if err := redisClient.Close(); err != nil {
				logger.Warn("redis close error", zap.Error(err))
			}
		}()
		pingCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		pingErr := redisClient.Ping(pingCtx)
		cancel()
		if pingErr != nil {
			logger.Warn("redis unavailable, continuing in degraded mode",
				zap.String("addr", cfg.RedactedRedisAddr()),
				zap.Error(pingErr),
			)
		} else {
			logger.Info("redis connected", zap.String("addr", cfg.RedactedRedisAddr()))
		}
	} else {
		logger.Info("redis disabled")
	}

	// Construct the UDP listener and register its scrape-time queue callback
	// before the metrics endpoint starts serving.
	udpServer := server.NewUDP(cfg, logger)
	udpServer.Metrics = m
	m.RegisterUDPInboundQueueDepth(udpServer.InboundQueueDepth)

	// TLS material is minted ONCE here and handed to both listeners: the data
	// port must present the same certificate as the control channel so the
	// client re-uses the pin it already holds, and two independent
	// tlscert.Ensure calls on a fresh install would race to create two certs.
	var (
		tlsCert tls.Certificate
		tlsFP   string
	)
	if cfg.TLSEnabled {
		tlsCert, tlsFP, err = tlscert.Ensure(cfg.TLSDir, cfg.TLSCertFile, cfg.TLSKeyFile,
			[]string{"localhost", cfg.ServerName})
		if err != nil {
			return fmt.Errorf("preparing TLS material: %w", err)
		}
		notAfter, expiryErr := tlscert.NotAfter(&tlsCert)
		if expiryErr != nil {
			return fmt.Errorf("reading TLS certificate expiry: %w", expiryErr)
		}
		expiresSoon, expiryErr := tlscert.ExpiresBy(&tlsCert, time.Now().Add(30*24*time.Hour))
		if expiryErr != nil {
			return fmt.Errorf("evaluating TLS certificate expiry: %w", expiryErr)
		}
		if expiresSoon {
			logger.Warn("TLS certificate expires within 30 days or is already expired; rotate it deliberately to preserve client TOFU pins",
				zap.Time("not_after", notAfter),
				zap.String("fingerprint", tlsFP),
			)
		}
	}
	fileTLS := cfg.TLSEnabled && cfg.FileTLSEnabled
	if cfg.FileTLSEnabled && !cfg.TLSEnabled {
		logger.Warn("file_tls_enabled is set but tls_enabled is false: there is no certificate to present, file transfers stay PLAINTEXT")
	}

	// Construct the file-transfer server before the control server (which
	// references it for token issuance). The control channel issues transfer
	// tokens (permission-checked); the file port trusts only the token.
	ftServer := filetransfer.New(filetransfer.Config{
		Addr:            cfg.FileAddr,
		RootDir:         cfg.FileRoot,
		MaxKBps:         cfg.FileMaxKBps,
		MaxConnections:  cfg.FileMaxConnections,
		QuietHoursStart: cfg.FileQuietHoursStart,
		QuietHoursEnd:   cfg.FileQuietHoursEnd,
		ChannelQuotaMB:  cfg.FileChannelQuotaMB,
		UserQuotaMB:     cfg.FileUserQuotaMB,
		MaxSizeMB:       cfg.FileMaxSizeMB,
		TLSEnabled:      fileTLS,
		Cert:            tlsCert,
		Fingerprint:     tlsFP,
	}, dbStore, logger)
	ftServer.OnTransferComplete = m.IncFileTransfer
	storageProbe := newCachedProbe(storageProbeTTL, time.Now, ftServer.CheckRoot)
	if err := storageProbe.Check(context.Background()); err != nil {
		logger.Warn("file storage root not writable, file transfers will fail",
			zap.String("root", cfg.FileRoot),
			zap.Error(err),
		)
	}

	// Health/readiness is constructed only after every dependency it checks is
	// available. The handler's single three-second request context is shared
	// by the mandatory Postgres/storage checks and optional Redis check.
	components := []readinessComponent{
		{
			name:     "postgres",
			required: true,
			check: func(ctx context.Context) error {
				return retryOnce(ctx, readinessRetryDelay, dbStore.Ping)
			},
		},
	}
	components = append(components, readinessComponent{
		name:     "storage",
		required: true,
		check:    storageProbe.Check,
	})
	if redisClient != nil {
		components = append(components, readinessComponent{
			name:     "redis",
			required: false,
			check:    redisClient.Ping,
		})
	}
	readiness := newReadinessChecker(components, m.ObserveReadiness)
	voiceRouter.SetForwardObserver(m.IncRTPForwarded)
	var servingReady atomic.Bool
	healthServer := health.New(cfg.HealthAddr, logger, func(ctx context.Context) error {
		if !servingReady.Load() {
			return errors.New("server startup is not complete")
		}
		return readiness(ctx)
	})
	if cfg.MetricsAllowRemote {
		healthServer.HandleGET("/metrics", m.Handler())
	} else {
		healthServer.HandleLocalGET("/metrics", m.Handler())
	}
	registerPprofEndpoints(healthServer, cfg.PprofEnabled)
	healthServer.Handle("/api/v1/schema/version", health.SchemaVersionHandler(logger, dbStore.SchemaVersion))
	// Download links (267): the control channel mints expiring tokens; the
	// health HTTP server serves them (LAN-friendly, no extra auth).
	healthServer.Handle("/dl/", ftServer.Links())

	// Global server password (plaintext in config, hashed once at startup
	// with Argon2id). Empty means an open server.
	var serverPasswordHash string
	if cfg.ServerPassword != "" {
		hash, err := auth.HashPassword(cfg.ServerPassword)
		if err != nil {
			return fmt.Errorf("hashing server password: %w", err)
		}
		serverPasswordHash = hash
		logger.Info("server password enabled")
	}
	// One limiter owns every expensive credential verification in this process.
	// Transports use separate source scopes while sharing this KDF gate.
	loginLimiter := auth.NewLoginFailureLimiter(auth.LoginFailureLimiterConfig{})
	for _, warning := range server.AssetStorageSecurityWarnings() {
		logger.Error("ASSET STORAGE SECURITY LIMITATION", zap.String("warning", warning))
	}

	// Flag only confined, supported, regular channel icons so snapshots do not
	// trust arbitrary directory entries at startup.
	if _, err := markChannelIcons(cfg.FileRoot, stateManager); err != nil {
		logger.Warn("channel icon discovery failed", zap.Error(err))
	}

	// (215) one rules service for both readers: ServerQuery edits the wording
	// and the control server hands it out, and a second instance would only
	// be a second place for the two to disagree.
	rulesSvc := rules.New(dbStore, dbStore.DB())

	// Start the TCP control listener, wired to the auth, state, channels,
	// broadcast, roles, and voice backends.
	tcpServer = server.New(cfg, logger, &server.Deps{
		Auth:               authSvc,
		State:              stateManager,
		Channels:           channelMgr,
		Broadcast:          broadcaster,
		Authority:          roleAuthority,
		Bans:               dbStore,
		Spool:              dbStore,
		PreKeys:            dbStore,
		Voice:              voice,
		Recorder:           rec,
		FileTransfer:       ftServer,
		Complaints:         dbStore,
		CustomMetadata:     dbStore,
		Chat:               dbStore,
		Groups:             dbStore,
		Roles:              dbStore,
		BanAdmin:           dbStore,
		Metrics:            m,
		LoginLimiter:       loginLimiter,
		Rules:              rulesSvc,
		ScopeKeys:          dbStore,
		ChatKEK:            chatKEK,
		ServerPasswordHash: serverPasswordHash,
		ICEServers:         iceServersProvider(cfg, logger),
	})
	channelMgr.EnableRoleMode(roleAuthority)
	// Remove orphaned file data before serving. Role-mode channel timers remain
	// disabled until the Authority is attached and its initial Reload completes.
	// No listener is accepting file work yet.
	liveChannels := stateManager.ListChannels()
	liveChannelIDs := make([]int64, 0, len(liveChannels))
	for _, channel := range liveChannels {
		liveChannelIDs = append(liveChannelIDs, channel.ChannelID)
	}
	reconciledChannels, err := ftServer.ReconcileChannelData(context.Background(), liveChannelIDs)
	if err != nil {
		return fmt.Errorf("reconciling orphaned channel files: %w", err)
	}
	if reconciledChannels > 0 {
		logger.Info("orphaned channel file directories removed", zap.Int("count", reconciledChannels))
	}
	if cfg.TLSEnabled {
		tcpServer.UseTLSMaterial(tlsCert, tlsFP)
	}

	// The global generation is minted eagerly: it is a fixed known scope, and
	// minting it here removes the only legitimate lazy mint from a hot path.
	// Both of these are fatal — a half-encrypted table behind a NOT VALID
	// constraint is the silent-plaintext state 91 forbids.
	if err := tcpServer.EnsureGlobalScopeKey(context.Background()); err != nil {
		return fmt.Errorf("ensuring the global chat key: %w", err)
	}
	if err := tcpServer.EncryptLegacyChatHistory(context.Background(), cfg.ChatLegacyHistory); err != nil {
		return fmt.Errorf("encrypting legacy chat history: %w", err)
	}
	if err := roleAuthority.Reload(ctx); err != nil {
		return fmt.Errorf("reconciling roles-v1 authorization before serving: %w", err)
	}

	// ServerQuery accepts explicitly enabled roles-v1 integration accounts.
	qBackend := &queryBackend{tcp: tcpServer}
	queryServer := query.New(cfg.QueryAddr, logger, qBackend)
	queryServer.SetLoginLimiter(loginLimiter)
	queryServer.SetMetrics(m)
	// (231) the event stream for bots, on the health listener next to
	// /metrics. Integration roles govern the filtered stream.
	healthServer.Handle("/events", eventbus.HandlerWithRoleBackend(events, qBackend, logger, loginLimiter, m))
	registerEventBusMetrics(m.Registry(), events, logger)

	// (232) the gRPC API on the reserved port shares the roles-v1 backend.
	grpcServer := grpcserver.New(cfg.GRPCAddr, qBackend, events, logger, queryServer)
	grpcServer.ShutdownTimeout = cfg.ShutdownTimeout
	// gRPC has a dedicated run context. The shared shutdown context below owns
	// its graceful stop deterministically, rather than letting Start's
	// cancellation watcher race to manufacture a second deadline.
	grpcRunCtx, cancelGRPCRun := context.WithCancel(context.Background())
	defer cancelGRPCRun()

	// (224) the same command set over SSH, opt-in.
	var sshQuery *query.SSHServer
	if cfg.QuerySSHEnabled {
		sshQuery = query.NewSSH(cfg.QuerySSHAddr, cfg.QuerySSHHostKey, queryServer)
	}

	// Routes and listener objects are complete before any socket is opened.
	// The spare slot ensures every reporter can finish after one exit triggers
	// shutdown (eight services at most, including optional SSH and lease failure).
	serviceExits := make(chan serviceExit, 9)
	if err := lease.Check(ctx); err != nil {
		return fmt.Errorf("checking role process lease before listening: %w", err)
	}
	go func() {
		if err := monitorRoleProcessLease(ctx, lease); err != nil {
			serviceExits <- serviceExit{name: "role process lease", err: err}
		}
	}()
	startService(serviceExits, "health HTTP server", healthServer.Start)
	startService(serviceExits, "TCP control server", func() error { return tcpServer.Start(ctx) })
	startService(serviceExits, "ServerQuery server", func() error { return queryServer.Start(ctx) })
	startService(serviceExits, "gRPC server", func() error { return grpcServer.Start(grpcRunCtx) })
	if sshQuery != nil {
		startService(serviceExits, "ServerQuery SSH server", func() error { return sshQuery.Start(ctx) })
	}
	startService(serviceExits, "file-transfer server", func() error { return ftServer.Start(ctx) })
	startService(serviceExits, "UDP media server", func() error { return udpServer.Start(ctx) })
	servingReady.Store(true)
	logger.Info("noxa server running, waiting for shutdown signal")
	var runErr error
	select {
	case <-ctx.Done():
		logger.Info("noxa server shutting down")
	case exit := <-serviceExits:
		runErr = unexpectedServiceExit(exit)
	}
	servingReady.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancel()
	stop()

	grpcShutdown := make(chan error, 1)
	go func() {
		grpcShutdown <- grpcServer.Shutdown(shutdownCtx)
	}()
	if err := healthServer.Shutdown(shutdownCtx); err != nil {
		runErr = joinShutdownError(runErr, "health HTTP server", err)
	}

	runErr = joinShutdownError(runErr, "TCP control server", tcpServer.Shutdown(shutdownCtx))
	runErr = joinShutdownError(runErr, "ServerQuery server", queryServer.Close())
	if sshQuery != nil {
		runErr = joinShutdownError(runErr, "ServerQuery SSH server", sshQuery.Close())
	}
	runErr = joinShutdownError(runErr, "gRPC server", <-grpcShutdown)
	runErr = joinShutdownError(runErr, "file-transfer server", ftServer.Close())
	runErr = joinShutdownError(runErr, "UDP media server", udpServer.Shutdown())
	stats := udpServer.Stats()
	logger.Info("udp server stats",
		zap.Uint64("packets_received", stats.PacketsReceived),
		zap.Uint64("packets_dropped", stats.PacketsDropped),
		zap.Uint64("packets_processed", stats.PacketsProcessed),
		zap.Uint64("packets_rate_limited", stats.PacketsRateLimited),
	)
	return runErr
}

func monitorRoleProcessLease(ctx context.Context, lease *store.RoleProcessLease) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			checkCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := lease.Check(checkCtx)
			cancel()
			if err != nil && ctx.Err() == nil {
				return err
			}
		}
	}
}

// iceServersProvider builds the Deps.ICEServers callback: clients receive the
// configured STUN servers plus, when turn.secret is set, a TURN entry with
// time-limited REST API credentials minted per client unique ID (445/446).
// It returns nil when there is nothing to deliver (client then uses its own
// defaults).
func iceServersProvider(cfg *config.Config, logger *zap.Logger) func(string) []netproto.ICEServer {
	var stun []string
	for _, u := range cfg.WebRTC.ICEServers {
		if strings.HasPrefix(u, "stun:") {
			stun = append(stun, u)
		}
	}
	turnEnabled := cfg.TURN.Secret != "" && len(cfg.TURN.URIs) > 0
	if !turnEnabled && len(stun) == 0 {
		return nil
	}
	if cfg.TURN.Secret != "" && !turnEnabled {
		logger.Warn("turn.secret set but turn.uris is empty; TURN disabled")
	}
	return func(uniqueID string) []netproto.ICEServer {
		var out []netproto.ICEServer
		if len(stun) > 0 {
			out = append(out, netproto.ICEServer{URLs: stun})
		}
		if turnEnabled {
			username, credential := turn.Credentials(cfg.TURN.Secret, uniqueID, cfg.TURN.CredentialsTTL, time.Now())
			out = append(out, netproto.ICEServer{
				URLs:       cfg.TURN.URIs,
				Username:   username,
				Credential: credential,
			})
		}
		return out
	}
}

// registerEventBusMetrics publishes the event-bus counters on /metrics (231):
// the drop policy is only defensible if an operator can see it firing.
func registerEventBusMetrics(reg *prometheus.Registry, bus *eventbus.Bus, logger *zap.Logger) {
	collectors := []prometheus.Collector{
		prometheus.NewGaugeFunc(prometheus.GaugeOpts{
			Name: "noxa_eventbus_subscribers",
			Help: "Current number of event-bus subscribers (bots).",
		}, func() float64 { return float64(bus.Stats().Subscribers) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "noxa_eventbus_published_total",
			Help: "Events published to the event bus.",
		}, func() float64 { return float64(bus.Stats().Published) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "noxa_eventbus_dropped_total",
			Help: "Events dropped because a subscriber was not draining its buffer.",
		}, func() float64 { return float64(bus.Stats().Dropped) }),
		prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: "noxa_eventbus_evicted_total",
			Help: "Subscribers evicted for persistently failing to drain.",
		}, func() float64 { return float64(bus.Stats().Evicted) }),
	}
	for _, c := range collectors {
		if err := reg.Register(c); err != nil {
			logger.Warn("registering event-bus metric failed", zap.Error(err))
		}
	}
}

// queryBackend exposes the server's role-scoped integration operations.
type queryBackend struct {
	tcp *server.TCPServer
}
