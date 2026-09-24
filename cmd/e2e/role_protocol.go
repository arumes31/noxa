package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

func e2eAuthorizationModel(models []string) (string, error) {
	if len(models) == 0 {
		return "", nil
	}
	if len(models) != 1 || (models[0] != "" && models[0] != netproto.AuthorizationModelRolesV1) {
		return "", errors.New("unsupported E2E authorization model")
	}
	return models[0], nil
}

func e2eAdvertisedModels(model string) []string {
	if model == "" {
		return nil
	}
	return []string{model}
}

func e2eCheckAuthorizationModel(response netproto.AuthResponse, model string) error {
	if response.AuthorizationModel != model {
		return errors.New("server authorization model differs from the selected E2E profile; upgrade or select the matching profile")
	}
	return nil
}

// Roles publish filtered snapshots instead of unscoped membership events.
// Capture key updates on the same stream while waiting for the exact member.
func waitForRoleMembership(conn net.Conn, clientID string, channelID int64) error {
	if err := conn.SetReadDeadline(time.Now().Add(readTimeout)); err != nil {
		return err
	}
	defer clearE2EReadDeadline(conn)
	for {
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			return err
		}
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgError:
			return errors.New("server rejected role membership operation")
		case netproto.MsgPing:
			if err := writeMsg(conn, netproto.MsgPong, netproto.Pong{}); err != nil {
				return err
			}
		case netproto.MsgChannelKey:
			captureChannelKey(conn, frame)
		case netproto.MsgSnapshot:
			var snapshot broadcast.TreeSnapshot
			if err := netproto.Decode(frame, &snapshot); err != nil {
				return err
			}
			if roleSnapshotContains(snapshot, clientID, channelID) {
				return nil
			}
		}
	}
}

func roleSnapshotContains(snapshot broadcast.TreeSnapshot, clientID string, channelID int64) bool {
	if channelID == 0 {
		for _, member := range snapshot.UnassignedClients {
			if member != nil && member.ClientID == clientID && member.ChannelID == 0 {
				return true
			}
		}
		return false
	}
	var visit func([]*broadcast.ChannelNode) bool
	visit = func(nodes []*broadcast.ChannelNode) bool {
		for _, node := range nodes {
			if node == nil {
				continue
			}
			if node.ChannelID == channelID {
				for _, member := range node.Clients {
					if member != nil && member.ClientID == clientID && member.ChannelID == channelID {
						return true
					}
				}
			}
			if visit(node.Children) {
				return true
			}
		}
		return false
	}
	return visit(snapshot.RootChannels)
}

var e2eQueryEscaper = strings.NewReplacer(`\`, `\\`, ` `, `\s`, `|`, `\p`, `/`, `\/`, "\n", `\n`, "\r", `\r`, "\t", `\t`)

func escapeE2EQuery(value string) string { return e2eQueryEscaper.Replace(value) }

func unescapeE2EQuery(value string) (string, error) {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' {
			out.WriteByte(value[i])
			continue
		}
		i++
		if i == len(value) {
			return "", errors.New("truncated Query escape")
		}
		switch value[i] {
		case 's':
			out.WriteByte(' ')
		case 'p':
			out.WriteByte('|')
		case 'n':
			out.WriteByte('\n')
		case 'r':
			out.WriteByte('\r')
		case 't':
			out.WriteByte('\t')
		case '\\', '/':
			out.WriteByte(value[i])
		default:
			return "", errors.New("invalid Query escape")
		}
	}
	return out.String(), nil
}

func e2eQueryCommand(name string, request any) (string, error) {
	command := name
	if request != nil {
		payload, err := json.Marshal(request)
		if err != nil {
			return "", err
		}
		command += " data=" + escapeE2EQuery(string(payload))
	}
	return command, nil
}

func e2eQueryJSON[T any](q *querySession, name string, request any) (T, error) {
	var result T
	command, err := e2eQueryCommand(name, request)
	if err != nil {
		return result, err
	}
	lines, err := q.cmd(command)
	if err != nil {
		return result, fmt.Errorf("%s: %w", name, err)
	}
	if len(lines) != 2 || lines[1] != "error id=0 msg=ok" || !strings.HasPrefix(lines[0], "data=") {
		return result, fmt.Errorf("%s did not return one successful JSON result", name)
	}
	value, err := unescapeE2EQuery(strings.TrimPrefix(lines[0], "data="))
	if err != nil {
		return result, err
	}
	if err := json.Unmarshal([]byte(value), &result); err != nil {
		return result, err
	}
	return result, nil
}
