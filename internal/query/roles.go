package query

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

// RoleIntegrationBackend is the common roles-v1 authentication and protected
// snapshot contract shared by every integration transport.
type RoleIntegrationBackend interface {
	RoleIntegrationsEnabled() bool
	AuthenticateIntegration(context.Context, string, string, string) (auth.IntegrationPrincipal, error)
	WithIntegrationSnapshot(context.Context, auth.IntegrationPrincipal, func(context.Context, *broadcast.TreeSnapshot) error) error
}

const maxIntegrationResponseBytes = 8 << 20

const authorizationModelUpgrade = "roles-v1 authorization required; upgrade your integration and declare authorization_model=roles-v1"

const sshAuthorizationModelUpgrade = "roles-v1 authorization required; upgrade your integration and set NOXA_AUTHORIZATION_MODEL=roles-v1 before opening the SSH shell or command"

var errIntegrationResponseTooLarge = errors.New("integration response exceeds limit")

func (s *Server) roleBackend() RoleIntegrationBackend {
	b, ok := s.backend.(RoleIntegrationBackend)
	if !ok || !b.RoleIntegrationsEnabled() {
		return nil
	}
	return b
}

func (s *Server) roleLogin(ctx context.Context, sess *session, b RoleIntegrationBackend, identifier, password string, attempt *auth.LoginAttempt, principalScope string) bool {
	sess.authed, sess.username, sess.principal = false, "", auth.IntegrationPrincipal{}
	p, err := b.AuthenticateIntegration(ctx, identifier, password, sess.remoteIP)
	if errors.Is(err, auth.ErrIntegrationDenied) {
		attempt.Fail()
		s.RecordAuthFailure("query", "invalid_credentials")
		return s.write(sess, errorLine(errLoginFailed, "invalid loginname or password"))
	}
	if err != nil {
		s.logger.Warn("query integration login error", zap.Error(err))
		return s.write(sess, errorLine(errServerError, "integration authentication unavailable"))
	}
	attempt.Succeed(principalScope)
	sess.authed, sess.username, sess.principal = true, p.UniqueID(), p
	sess.authorizationModel = netproto.AuthorizationModelRolesV1
	return s.write(sess, "authorization_model="+sess.authorizationModel+"\n"+errorLine(errOK, "ok"))
}

func (s *Server) executeRoleRead(ctx context.Context, sess *session, b RoleIntegrationBackend, cmd command) bool {
	if cmd.name != "clientlist" && cmd.name != "channellist" && cmd.name != "channelinfo" {
		return s.write(sess, errorLine(errInsufficientPermissions, "command unavailable with roles-v1 authorization"))
	}
	var channelID int64
	if cmd.name == "channelinfo" {
		var err error
		channelID, err = strconv.ParseInt(cmd.args["cid"], 10, 64)
		if err != nil || channelID <= 0 {
			return s.write(sess, errorLine(errInvalidParameter, "usage: channelinfo cid=<id>"))
		}
	}
	// A bounded write is essential: policy revocation waits for this response.
	if sess.setWriteDeadline == nil || sess.closeTransport == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if sess.roleWriteMu != nil {
		sess.roleWriteMu.Lock()
		defer sess.roleWriteMu.Unlock()
	}
	writing := false
	err := b.WithIntegrationSnapshot(ctx, sess.principal, func(ctx context.Context, snapshot *broadcast.TreeSnapshot) error {
		var out strings.Builder
		check := func() error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if out.Len() > maxIntegrationResponseBytes {
				return errIntegrationResponseTooLarge
			}
			return nil
		}
		writeClient := func(c *broadcast.ClientInfo) error {
			if err := check(); err != nil {
				return err
			}
			fmt.Fprintf(&out, "clid=%s client_unique_identifier=%s client_nickname=%s cid=%d\n",
				escape(c.ClientID), escape(c.UniqueID), escape(c.Nickname), c.ChannelID)
			return nil
		}
		if cmd.name == "clientlist" {
			for _, c := range snapshot.UnassignedClients {
				if err := writeClient(c); err != nil {
					return err
				}
			}
		}
		found := false
		var visit func([]*broadcast.ChannelNode) error
		visit = func(nodes []*broadcast.ChannelNode) error {
			for _, ch := range nodes {
				if err := check(); err != nil {
					return err
				}
				switch cmd.name {
				case "clientlist":
					for _, c := range ch.Clients {
						if err := writeClient(c); err != nil {
							return err
						}
					}
				case "channellist":
					fmt.Fprintf(&out, "cid=%d pid=%d channel_name=%s channel_type=%d total_clients=%d\n",
						ch.ChannelID, ch.ParentID, escape(ch.Name), ch.ChannelType, ch.ClientCount)
				case "channelinfo":
					if ch.ChannelID == channelID {
						found = true
						fmt.Fprintf(&out, "cid=%d pid=%d channel_name=%s channel_topic=%s channel_type=%d channel_maxclients=%d total_clients=%d opus_bitrate=%d opus_fec=%d opus_dtx=%d opus_stereo=%d slow_mode_seconds=%d\n",
							ch.ChannelID, ch.ParentID, escape(ch.Name), escape(ch.Topic), ch.ChannelType, ch.MaxClients, ch.ClientCount,
							ch.OpusBitrate, boolInt(ch.OpusFEC), boolInt(ch.OpusDTX), boolInt(ch.OpusStereo), ch.SlowModeSeconds)
					}
				}
				if err := visit(ch.Children); err != nil {
					return err
				}
			}
			return check()
		}
		if err := visit(snapshot.RootChannels); err != nil {
			return err
		}
		if cmd.name == "channelinfo" && !found {
			out.WriteString(errorLine(errInvalidParameter, "channel not found"))
		} else {
			out.WriteString(errorLine(errOK, "ok"))
		}
		if err := check(); err != nil {
			return err
		}
		writing = true
		return writeIntegrationResponse(ctx, sess, out.String())
	})
	if err == nil {
		return true
	}
	if writing {
		return false // never append a second result after a partial write
	}
	return s.integrationError(sess, err)
}
