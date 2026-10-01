// commands.go implements the ServerQuery command set: parsing, the auth
// gate, and one handler per command.
package query

import (
	"bufio"
	"context"
	"io"
	"strings"
	"sync"
	"time"

	"noxa/internal/auth"
	"noxa/internal/netproto"
)

// session holds per-connection query state. It is transport-agnostic (224):
// the raw TCP port and the SSH transport differ only in the reader/writer and
// in whether read deadlines are available.
type session struct {
	r *bufio.Reader
	w io.Writer
	// setReadDeadline arms the idle timeout; nil when the transport has no
	// deadline of its own.
	setReadDeadline    func(time.Time) error
	setWriteDeadline   func(time.Time) error
	closeTransport     func() error
	remoteIP           string
	authed             bool
	username           string
	principal          auth.IntegrationPrincipal
	authorizationModel string
	// SSH must negotiate on this channel before Query-style reauthentication.
	sshModelRequired bool
	sshModelAccepted bool
	// SSH channels share a network deadline; serialize protected responses.
	roleWriteMu *sync.Mutex
}

// command is a parsed command line: name, key=value args (unescaped), and
// positional arguments (used by login).
type command struct {
	name       string
	args       map[string]string
	positional []string
}

// parseCommand splits a command line into name, key=value pairs, and
// positional tokens. Values with spaces must arrive escaped (\s).
//
// login is the only command with positional arguments, and its unique ID
// argument is base64 (which may END in '='); splitting it as key=value would
// eat the ID, so login's arguments are always parsed positionally.
func parseCommand(line string) command {
	fields := strings.Fields(line)
	cmd := command{args: make(map[string]string)}
	if len(fields) == 0 {
		return cmd
	}
	cmd.name = strings.ToLower(fields[0])

	if cmd.name == "login" {
		for _, f := range fields[1:] {
			cmd.positional = append(cmd.positional, unescape(f))
		}
		return cmd
	}

	for _, f := range fields[1:] {
		if k, v, ok := strings.Cut(f, "="); ok {
			cmd.args[unescape(k)] = unescape(v)
		} else {
			cmd.positional = append(cmd.positional, unescape(f))
		}
	}
	return cmd
}

// noAuthCommands may be used without login.
var noAuthCommands = map[string]bool{
	"login":   true,
	"help":    true,
	"quit":    true,
	"version": true,
}

// execute parses and runs one command line, writing the response. It returns
// false when the connection should close (quit, or a write failure).
func (s *Server) execute(ctx context.Context, sess *session, line string) bool {
	cmd := parseCommand(line)
	if cmd.name == "" {
		return true
	}

	if !sess.authed && !noAuthCommands[cmd.name] {
		return s.write(sess, errorLine(errInsufficientPermissions, "not logged in"))
	}
	switch cmd.name {
	case "login":
		return s.cmdLogin(ctx, sess, cmd)
	case "logout":
		sess.authed = false
		sess.username = ""
		sess.principal = auth.IntegrationPrincipal{}
		sess.authorizationModel = ""
		return s.write(sess, errorLine(errOK, "ok"))
	case "quit":
		s.write(sess, errorLine(errOK, "ok"))
		return false
	case "help":
		return s.write(sess, roleHelpText+errorLine(errOK, "ok"))
	case "version":
		return s.write(sess, "version="+Version+"\n"+errorLine(errOK, "ok"))
	}
	b := s.roleBackend()
	if b == nil {
		return s.write(sess, errorLine(errServerError, "roles-v1 integration backend unavailable"))
	}
	if sess.authorizationModel != netproto.AuthorizationModelRolesV1 {
		if sess.sshModelRequired {
			return s.write(sess, errorLine(errInsufficientPermissions, sshAuthorizationModelUpgrade))
		}
		return s.write(sess, errorLine(errInsufficientPermissions, authorizationModelUpgrade))
	}
	return s.executeRoleCommand(ctx, sess, b, cmd)
}

// write sends lines to the client. It returns false when the write fails.
func (s *Server) write(sess *session, text string) bool {
	_, err := io.WriteString(sess.w, text)
	return err == nil
}

// cmdLogin authenticates the session. Brute-force protection is scoped by
// remote source and normalized principal, so one failed identity cannot lock
// out an unrelated administrator using the same address.
func (s *Server) cmdLogin(ctx context.Context, sess *session, cmd command) bool {
	roleBackend := s.roleBackend()
	// Every attempt replaces the previous identity and negotiated model.
	sess.authed, sess.username, sess.principal = false, "", auth.IntegrationPrincipal{}
	sess.authorizationModel = ""
	if roleBackend == nil {
		return s.write(sess, errorLine(errServerError, "roles-v1 integration backend unavailable"))
	}
	if sess.sshModelRequired && !sess.sshModelAccepted {
		return s.write(sess, errorLine(errInsufficientPermissions, sshAuthorizationModelUpgrade))
	}
	if len(cmd.positional) != 3 || cmd.positional[2] != "authorization_model="+netproto.AuthorizationModelRolesV1 {
		return s.write(sess, errorLine(errInsufficientPermissions, authorizationModelUpgrade))
	}
	uniqueID, password := cmd.positional[0], cmd.positional[1]
	sourceScope := auth.LoginFailureScope("query:"+sess.remoteIP, "")
	principalScope := auth.LoginFailureScope("", uniqueID)
	attempt, allowed := s.ReserveLoginAttempt(sourceScope, principalScope)
	if !allowed {
		s.RecordAuthFailure("query", "locked_out")
		return s.write(sess, errorLine(errLoginFailed, "too many failed logins, try again later"))
	}
	defer attempt.Cancel()
	return s.roleLogin(ctx, sess, roleBackend, uniqueID, password, attempt, principalScope)
}

// LoginAllowed reports whether the shared ServerQuery brute-force limiter
// currently permits another attempt from scope.
func (s *Server) LoginAllowed(scope string) bool {
	return s.loginFailureLimiter().Allowed(scope)
}

// ReserveLoginAttempt atomically reserves one bounded password-verification
// slot for scope. Callers must finish the returned attempt.
func (s *Server) ReserveLoginAttempt(scopes ...string) (*auth.LoginAttempt, bool) {
	return s.loginFailureLimiter().Reserve(scopes...)
}

// RecordLoginFailure feeds a failed attempt into the shared limiter.
func (s *Server) RecordLoginFailure(scope string) {
	s.loginFailureLimiter().RecordFailure(scope)
}

// ClearLoginFailures clears the shared limiter after a successful login.
func (s *Server) ClearLoginFailures(scope string) {
	s.loginFailureLimiter().Clear(scope)
}

// RecordAuthFailure increments the bounded auth-failure metric for a stable
// transport and reason pair.
func (s *Server) RecordAuthFailure(transport, reason string) {
	s.metricsSink().IncAuthFailure(transport, reason)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
