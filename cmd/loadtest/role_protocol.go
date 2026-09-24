package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/nacl/box"
	"golang.org/x/crypto/nacl/secretbox"
	"noxa/internal/broadcast"
	"noxa/internal/netproto"
)

var errAuthorizationModel = errors.New("authorization model mismatch; select the server model with -authorization-model")

var errControlRejected = errors.New("server rejected control operation")

func (c *loadChat) observe(frame *netproto.Frame) error {
	if c == nil || netproto.MessageType(frame.Type) != netproto.MsgChannelKey {
		return nil
	}
	var key netproto.ChannelKey
	if err := netproto.Decode(frame, &key); err != nil {
		return err
	}
	return c.install(key)
}

func validateAuthorizationModel(model string) error {
	if model != "" && model != netproto.AuthorizationModelRolesV1 {
		return errors.New("-authorization-model must be roles-v1")
	}
	return nil
}

func selectedAuthorizationModel(opts options) string {
	return netproto.AuthorizationModelRolesV1
}

// loadChat owns a session key pair and a bounded current/previous global key
// cache. The reader installs rotations while the traffic writer seals messages.
type loadChat struct {
	public, private   *[32]byte
	mu                sync.Mutex
	current, previous uint32
	keys              map[uint32][32]byte
	prefix            string
	sequence          uint64 // only the traffic writer advances this
}

func newLoadChat() (*loadChat, error) {
	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	return &loadChat{public: public, private: private, keys: make(map[uint32][32]byte), prefix: "loadtest-" + base64.RawURLEncoding.EncodeToString(nonce[:]) + "-"}, nil
}

func (c *loadChat) install(key netproto.ChannelKey) error {
	if key.ChannelID != 0 {
		return nil
	}
	sealed, err := base64.StdEncoding.DecodeString(key.SealedKey)
	if err != nil || key.KeyID == 0 {
		return errors.New("invalid global key")
	}
	plain, ok := box.OpenAnonymous(nil, sealed, c.public, c.private)
	if !ok || len(plain) != 32 {
		return errors.New("cannot open global key")
	}
	var value [32]byte
	copy(value[:], plain)
	c.mu.Lock()
	defer c.mu.Unlock()
	if key.KeyID < c.current {
		return nil
	}
	if key.KeyID > c.current {
		delete(c.keys, c.previous)
		c.previous, c.current = c.current, key.KeyID
	}
	c.keys[key.KeyID] = value
	return nil
}

func (c *loadChat) message() (netproto.ChatSend, error) {
	c.mu.Lock()
	id, key := c.current, c.keys[c.current]
	c.mu.Unlock()
	if id == 0 {
		return netproto.ChatSend{}, errors.New("global chat key unavailable")
	}
	var nonce [24]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return netproto.ChatSend{}, err
	}
	c.sequence++
	idText := c.prefix + strconv.FormatUint(c.sequence, 10)
	return netproto.ChatSend{
		Text: base64.StdEncoding.EncodeToString(append(nonce[:], secretbox.Seal(nil, []byte("loadtest ping "+idText), &nonce, &key)...)),
		Enc:  true, KeyID: id, ClientMsgID: idText,
	}, nil
}

func (c *loadChat) receive(frame *netproto.Frame, clientID string) (chat, confirmed bool, err error) {
	var event struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	}
	if err := netproto.Decode(frame, &event); err != nil {
		return false, false, err
	}
	if event.Type != "chat" {
		return false, false, nil
	}
	var message netproto.ChatBroadcast
	if err := json.Unmarshal(event.Data, &message); err != nil {
		return false, false, err
	}
	if message.ChannelID != "" || message.Direct || message.E2E {
		return false, false, nil
	}
	if !message.Enc {
		return false, false, errors.New("unencrypted global chat")
	}
	c.mu.Lock()
	key, found := c.keys[message.KeyID]
	c.mu.Unlock()
	sealed, err := base64.StdEncoding.DecodeString(message.Text)
	if err != nil || !found || len(sealed) < 24+secretbox.Overhead {
		return false, false, errors.New("invalid global chat ciphertext")
	}
	var nonce [24]byte
	copy(nonce[:], sealed[:24])
	plain, ok := secretbox.Open(nil, sealed[24:], &nonce, &key)
	if !ok {
		return false, false, errors.New("cannot open global chat")
	}
	if !strings.HasPrefix(message.ClientMsgID, "loadtest-") || string(plain) != "loadtest ping "+message.ClientMsgID {
		return false, false, nil
	}
	return true, message.FromClientID == clientID && message.ClientMsgID == c.prefix+"1", nil
}

// Role mode confirms membership from the server's filtered tree before starting
// media. A successful socket write alone does not prove Connect was allowed.
func awaitRoleJoin(conn net.Conn, chat *loadChat, clientID string, channelID int64) error {
	if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		return err
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()
	for {
		frame, err := netproto.ReadFrame(conn)
		if err != nil {
			return err
		}
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgError:
			return errors.New("channel join rejected")
		case netproto.MsgPing:
			if err := writeMsg(conn, netproto.MsgPong, netproto.Pong{}); err != nil {
				return err
			}
		case netproto.MsgChannelKey:
			var key netproto.ChannelKey
			if err := netproto.Decode(frame, &key); err != nil {
				return err
			}
			if err := chat.install(key); err != nil {
				return err
			}
		case netproto.MsgSnapshot:
			var snapshot broadcast.TreeSnapshot
			if err := netproto.Decode(frame, &snapshot); err != nil {
				return err
			}
			var joined func([]*broadcast.ChannelNode) bool
			joined = func(nodes []*broadcast.ChannelNode) bool {
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
					if joined(node.Children) {
						return true
					}
				}
				return false
			}
			if joined(snapshot.RootChannels) {
				return nil
			}
		}
	}
}
