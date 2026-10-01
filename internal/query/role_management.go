package query

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

// RoleManagementBackend consumes authenticated identity separately from the
// change's target member. The native store owns revision/hierarchy/audit checks.
type RoleManagementBackend interface {
	WithIntegrationRoleState(context.Context, auth.IntegrationPrincipal, int64, func(context.Context, netproto.RoleState) error) error
	ChangeIntegrationRoles(context.Context, auth.IntegrationPrincipal, authorization.RoleChange) (netproto.RoleChangeResult, error)
}

const roleHelpText = "authorization_model=roles-v1\n" +
	"login <unique_id_or_nickname> <password> authorization_model=roles-v1\nlogout\nhelp\nversion\nquit\n" +
	"SSH: set NOXA_AUTHORIZATION_MODEL=roles-v1 on each session channel before shell or exec.\n" +
	"clientlist\nchannellist\nchannelinfo cid=<channel_id>\n" +
	"rolelist [cid=<channel_id>]\nrolechange data=<escaped_JSON_with_expected_revision>\n" +
	"rolemembers data=<escaped_JSON_with_expected_revision>\naccesscheck data=<escaped_JSON_with_expected_revision>\n" +
	"channelquery data=<escaped_JSON>\nchannelchange data=<escaped_JSON_with_expected_revision>\n" +
	"membervoice data=<escaped_JSON_with_client_id_and_channel_id>\n" +
	"membermove data=<escaped_JSON_with_client_id_and_destination_channel_id>\n" +
	"memberdisconnect data=<escaped_JSON_with_client_id_and_current_channel_id>\n" +
	"memberkick data=<escaped_JSON_with_client_id>\nmemberban data=<escaped_JSON_with_client_id_and_duration_seconds>\n" +
	"banquery data=<escaped_JSON_with_optional_before_id_and_limit>\n" +
	"auditquery data=<escaped_JSON_with_optional_before_id_and_limit>\n" +
	"complaintquery data=<escaped_JSON_with_optional_after_id_and_limit>\ncomplaintclear data=<escaped_JSON_with_target_unique_id>\n" +
	"serverinfo\nclientinfo clid=<client_id>\nserverconfig\n" +
	"medialimits\nmedialimitsset data=<escaped_complete_media_limits>\n" +
	"rulesquery\n" +
	"serverconfigset data=<escaped_complete_server_configuration>\n" +
	"chatfilterquery\nchatfilterset data=<escaped_partial_chat_filters>\n" +
	"servertextset data=<escaped_JSON_with_key_and_value>\n" +
	"customquery data=<escaped_JSON_with_unique_id_and_optional_after_key_and_limit>\n" +
	"customchange data=<escaped_JSON_with_unique_id_key_and_value_or_delete>\n" +
	"Other protected commands are unavailable with roles-v1 authorization.\n"

func (s *Server) executeRoleCommand(ctx context.Context, sess *session, backend RoleIntegrationBackend, cmd command) bool {
	if cmd.name == "medialimits" || cmd.name == "medialimitsset" {
		return s.executeRoleMediaLimits(ctx, sess, backend, cmd)
	}
	if cmd.name == "customquery" || cmd.name == "customchange" {
		return s.executeRoleCustomMetadata(ctx, sess, backend, cmd)
	}
	if cmd.name == "servertextset" {
		return s.executeRoleText(ctx, sess, backend, cmd)
	}
	if cmd.name == "chatfilterquery" || cmd.name == "chatfilterset" {
		return s.executeRoleFilters(ctx, sess, backend, cmd)
	}
	if cmd.name == "serverconfigset" {
		return s.executeRoleConfig(ctx, sess, backend, cmd)
	}
	if cmd.name == "rulesquery" {
		return s.executeRoleRules(ctx, sess, backend, cmd)
	}
	if cmd.name == "complaintquery" || cmd.name == "complaintclear" {
		return s.executeRoleComplaints(ctx, sess, backend, cmd)
	}
	if cmd.name == "auditquery" {
		return s.executeRoleAudit(ctx, sess, backend, cmd)
	}
	if cmd.name == "serverinfo" || cmd.name == "clientinfo" || cmd.name == "serverconfig" {
		return s.executeRoleMetadata(ctx, sess, backend, cmd)
	}
	if cmd.name == "banquery" {
		return s.executeRoleBanQuery(ctx, sess, backend, cmd)
	}
	if cmd.name == "memberkick" || cmd.name == "memberban" {
		return s.executeRoleMemberRemoval(ctx, sess, backend, cmd)
	}
	if cmd.name == "memberdisconnect" {
		return s.executeRoleMemberDisconnect(ctx, sess, backend, cmd)
	}
	if cmd.name == "membermove" {
		return s.executeRoleMemberMove(ctx, sess, backend, cmd)
	}
	if cmd.name == "membervoice" {
		return s.executeRoleVoiceModeration(ctx, sess, backend, cmd)
	}
	if cmd.name == "channelquery" || cmd.name == "channelchange" {
		return s.executeRoleChannelCommand(ctx, sess, backend, cmd)
	}
	if cmd.name == "rolemembers" || cmd.name == "accesscheck" {
		return s.executeRoleInspection(ctx, sess, backend, cmd)
	}
	if cmd.name != "rolelist" && cmd.name != "rolechange" {
		return s.executeRoleRead(ctx, sess, backend, cmd)
	}
	b, ok := backend.(RoleManagementBackend)
	if !ok {
		return s.write(sess, errorLine(errInsufficientPermissions, "role management unavailable"))
	}
	var channelID int64
	var change authorization.RoleChange
	if cmd.name == "rolelist" {
		if value := cmd.args["cid"]; value != "" {
			var err error
			channelID, err = strconv.ParseInt(value, 10, 64)
			if err != nil || channelID < 0 {
				return s.write(sess, errorLine(errInvalidParameter, "usage: rolelist [cid=<channel_id>]"))
			}
		}
	} else {
		if err := decodeRoleRequest(cmd.args["data"], &change); err != nil || change.ExpectedRevision <= 0 {
			return s.write(sess, errorLine(errInvalidParameter, "rolechange requires data=<escaped JSON> with expected_revision"))
		}
	}
	duration := 10 * time.Second
	if cmd.name == "rolechange" {
		duration = 30 * time.Second
	}
	return s.executeIntegrationJSON(ctx, sess, duration, func(ctx context.Context, deliver func(context.Context, any) error) error {
		if cmd.name == "rolelist" {
			return b.WithIntegrationRoleState(ctx, sess.principal, channelID, func(ctx context.Context, state netproto.RoleState) error { return deliver(ctx, state) })
		}
		result, err := b.ChangeIntegrationRoles(ctx, sess.principal, change)
		if err != nil {
			return err
		}
		return deliverIntegrationCommit(ctx, deliver, result)
	})
}

// A known commit needs its own reply window after a reconciliation timeout.
func deliverIntegrationCommit(ctx context.Context, deliver func(context.Context, any) error, result any) error {
	replyCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	return deliver(replyCtx, result)
}

func (s *Server) executeIntegrationJSON(ctx context.Context, sess *session, duration time.Duration, operation func(context.Context, func(context.Context, any) error) error) bool {
	if sess.setWriteDeadline == nil || sess.closeTransport == nil {
		return false
	}
	if sess.roleWriteMu != nil {
		sess.roleWriteMu.Lock()
		defer sess.roleWriteMu.Unlock()
	}
	ctx, cancel := context.WithTimeout(ctx, duration)
	defer cancel()
	writing := false
	deliver := func(ctx context.Context, result any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if len(data) > maxIntegrationResponseBytes {
			return errIntegrationResponseTooLarge
		}
		response := "data=" + escape(string(data)) + "\n" + errorLine(errOK, "ok")
		if len(response) > maxIntegrationResponseBytes {
			return errIntegrationResponseTooLarge
		}
		writing = true
		return writeIntegrationResponse(ctx, sess, response)
	}
	err := operation(ctx, deliver)
	if err == nil {
		return true
	}
	if writing {
		return false
	}
	return s.integrationError(sess, err)
}

func decodeRoleRequest(data string, target any) error {
	decoder := json.NewDecoder(strings.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return authorization.ErrRoleInvalid
	}
	return nil
}

func writeIntegrationResponse(ctx context.Context, sess *session, text string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return context.DeadlineExceeded
	}
	if err := sess.setWriteDeadline(deadline); err != nil {
		return err
	}
	defer func() { _ = sess.setWriteDeadline(time.Time{}) }()
	// SSH can wait for channel credit before reaching a socket write.
	stop := context.AfterFunc(ctx, func() { _ = sess.closeTransport() })
	defer stop()
	_, err := io.WriteString(sess.w, text)
	return err
}

func (s *Server) integrationError(sess *session, err error) bool {
	switch {
	case errors.Is(err, auth.ErrIntegrationDenied):
		sess.authed, sess.username, sess.principal = false, "", auth.IntegrationPrincipal{}
		return s.write(sess, errorLine(errInsufficientPermissions, "integration access denied; log in again"))
	case errors.Is(err, authorization.ErrRoleForbidden):
		return s.write(sess, errorLine(errInsufficientPermissions, "role operation forbidden"))
	case errors.Is(err, authorization.ErrRoleConflict):
		return s.write(sess, errorLine(errRoleConflict, "permissions changed; refresh before saving"))
	case errors.Is(err, authorization.ErrRoleInvalid):
		return s.write(sess, errorLine(errInvalidParameter, "invalid role operation"))
	default:
		s.logger.Warn("query integration operation failed", zap.Error(err))
		return s.write(sess, errorLine(errServerError, "integration authorization unavailable"))
	}
}
