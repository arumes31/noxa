package server

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/netproto"
)

type assetFence struct {
	saved  []netproto.AssetMutationSaved
	errors []netproto.Error
	err    error
}

func readAssetFence(conn net.Conn, accepted chan<- struct{}) assetFence {
	var result assetFence
	for {
		f, err := netproto.ReadFrame(conn)
		if err != nil {
			result.err = err
			return result
		}
		switch netproto.MessageType(f.Type) {
		case netproto.MsgAssetMutationSaved:
			var saved netproto.AssetMutationSaved
			result.err = netproto.Decode(f, &saved)
			result.saved = append(result.saved, saved)
			accepted <- struct{}{}
		case netproto.MsgError:
			var e netproto.Error
			result.err = netproto.Decode(f, &e)
			result.errors = append(result.errors, e)
		case netproto.MsgPong:
			return result
		}
		if result.err != nil {
			return result
		}
	}
}

func TestAssetAcknowledgementFollowsStorage(t *testing.T) {
	for _, kind := range []netproto.MessageType{netproto.MsgAvatarSet, netproto.MsgServerIconSet, netproto.MsgServerBannerSet, netproto.MsgEmojiUpload, netproto.MsgEmojiDelete, netproto.MsgEmojiRename} {
		for _, outcome := range []string{"done", "failed", "denied", "invalid", "unrequested"} {
			t.Run(kind.String()+"/"+outcome, func(t *testing.T) {
				backend := serverRoleFixture()
				authority, err := authorization.NewAuthority(t.Context(), backend, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
				if err != nil {
					t.Fatal(err)
				}
				blockedRoot := filepath.Join(t.TempDir(), "blocked")
				if outcome == "failed" {
					if err := os.WriteFile(blockedRoot, []byte("not a directory"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				env := startTestEnvDeps(t, nil, func(c *config.Config) {
					if outcome == "failed" {
						c.FileRoot = blockedRoot
					}
				}, func(d *Deps) { d.Authority = authority })
				defer env.stop()
				uid := "user-uid"
				if outcome == "denied" {
					uid = "admin-uid"
				}
				conn, id := dialAuthed(t, env.addr, uid)
				defer func() { _ = conn.Close() }()
				dir, base := "", "server_icon"
				request := map[string]any{"ack_requested": outcome != "unrequested", "data_base64": b64(tinyPNG)}
				want := netproto.AssetMutationSaved{Operation: kind, ClientID: id}
				switch kind {
				case netproto.MsgAvatarSet:
					dir, base = "avatars", avatarAssetBase(uid)
				case netproto.MsgServerBannerSet:
					base = "server_banner"
				case netproto.MsgEmojiUpload, netproto.MsgEmojiDelete, netproto.MsgEmojiRename:
					dir, base = "emojis", "wave"
					want.Name = "wave"
					request["name"] = "wave"
					if kind == netproto.MsgEmojiRename {
						want.NewName = "hello"
						request["new_name"] = "hello"
					}
				}
				if outcome == "invalid" {
					request["name"] = "../invalid"
					request["data_base64"] = "invalid image"
				}
				storage := env.srv.assets()
				if outcome != "failed" && (kind == netproto.MsgEmojiDelete || kind == netproto.MsgEmojiRename) {
					if _, err := storage.writeImage(dir, base, ".png", tinyPNG); err != nil {
						t.Fatal(err)
					}
				}
				success := outcome == "done" || outcome == "unrequested"
				var unlock func()
				if success {
					unlock = storage.lockNamespace(dir)
				}
				var once sync.Once
				unblock := func() {
					once.Do(func() {
						if unlock != nil {
							unlock()
						}
					})
				}
				defer unblock()
				send(t, conn, kind, request)
				send(t, conn, netproto.MsgPing, netproto.Ping{})
				if err := conn.SetReadDeadline(time.Now().Add(8 * time.Second)); err != nil {
					t.Fatal(err)
				}
				accepted, done := make(chan struct{}, 4), make(chan assetFence, 1)
				go func() { done <- readAssetFence(conn, accepted) }()
				if success {
					waitFor(t, "asset storage waiter", func() bool {
						locks := &storage.lockSet().namespaces
						locks.mu.Lock()
						defer locks.mu.Unlock()
						return locks.entries[dir] != nil && locks.entries[dir].refs > 1
					})
					select {
					case <-accepted:
						t.Fatal("acknowledged before storage")
					case <-time.After(30 * time.Millisecond):
					}
				}
				unblock()
				result := <-done
				if result.err != nil {
					t.Fatal(result.err)
				}
				if outcome == "done" {
					if len(result.saved) != 1 || result.saved[0] != want || len(result.errors) != 0 {
						t.Fatalf("wrong result: %+v", result)
					}
				} else if len(result.saved) != 0 || (len(result.errors) == 0) != success {
					t.Fatalf("unexpected result: %+v", result)
				}
				if outcome == "failed" {
					data, err := os.ReadFile(blockedRoot)
					if err != nil || string(data) != "not a directory" {
						t.Fatalf("changed root: %q / %v", data, err)
					}
					return
				}
				if success && kind == netproto.MsgEmojiRename {
					if _, _, err := storage.readImage(dir, base); !errors.Is(err, fs.ErrNotExist) {
						t.Fatalf("old name remains: %v", err)
					}
					base = "hello"
				}
				data, _, err := storage.readImage(dir, base)
				wantImage := success && kind != netproto.MsgEmojiDelete || !success && (kind == netproto.MsgEmojiDelete || kind == netproto.MsgEmojiRename)
				if wantImage {
					if err != nil || !bytes.Equal(data, tinyPNG) {
						t.Fatalf("stored data mismatch: %v", err)
					}
				} else if !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("unexpected stored image: %v", err)
				}
			})
		}
	}
}
