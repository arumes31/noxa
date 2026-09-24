package server

import (
	"context"
	"errors"
	"fmt"
	"time"

	"noxa/internal/e2ee"
	"noxa/internal/netproto"
	"noxa/internal/store"
)

const maxPublishedOneTimePreKeys = 100

func (s *TCPServer) handlePreKeyPublish(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.publishSessionPreKeys(ctx, client, f)
	})
}

func (s *TCPServer) publishSessionPreKeys(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.PreKeyPublish
	if err := netproto.Decode(f, &msg); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "malformed prekey publish")
	}
	if client.userID() <= 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "registered account required for asynchronous prekeys")
	}
	if s.deps == nil || s.deps.PreKeys == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "prekey store unavailable")
	}
	bundle := e2ee.PreKeyBundle{
		IdentityDH: msg.IdentityDH, SigningPublic: msg.SigningPublic,
		SignedPreKeyID: msg.SignedPreKeyID, SignedPreKey: msg.SignedPreKey, Signature: msg.Signature,
	}
	if msg.SignedPreKeyID == 0 || !e2ee.VerifyPreKeyBundle(bundle) || len(msg.OneTimePreKeys) > maxPublishedOneTimePreKeys {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid signed prekey bundle")
	}
	seen := make(map[uint32]bool, len(msg.OneTimePreKeys))
	oneTime := make([]store.PreKey, 0, len(msg.OneTimePreKeys))
	for _, key := range msg.OneTimePreKeys {
		if key.KeyID == 0 || len(key.PublicKey) != 32 || seen[key.KeyID] {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "invalid or duplicate one-time prekey")
		}
		seen[key.KeyID] = true
		oneTime = append(oneTime, store.PreKey{KeyID: key.KeyID, PublicKey: key.PublicKey, OneTime: true})
	}
	previousIdentity, err := s.deps.PreKeys.PreKeyIdentity(ctx, client.userID())
	if err != nil && !errors.Is(err, store.ErrNoPreKeyBundle) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "loading stored prekey identity failed")
	}
	if len(previousIdentity) > 0 && !e2ee.EqualFingerprint(previousIdentity, msg.IdentityDH) {
		s.audit(ctx, client.UniqueID, "e2ee_identity_changed", client.UniqueID, "signed prekey identity changed")
	}
	if err := s.deps.PreKeys.PublishPreKeyBundle(ctx, client.userID(), store.PreKeyBundle{
		IdentityDH: msg.IdentityDH, SigningPublic: msg.SigningPublic,
		SignedPreKeyID: msg.SignedPreKeyID, SignedPreKey: msg.SignedPreKey, Signature: msg.Signature,
	}, oneTime); err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "publishing prekeys failed")
	}
	s.audit(ctx, client.UniqueID, "e2ee_prekeys_publish", client.UniqueID, fmt.Sprintf("one_time=%d", len(oneTime)))
	return nil
}

func (s *TCPServer) handlePreKeyQuery(ctx context.Context, client *Client, f *netproto.Frame) error {
	return s.rolePolicyRead(ctx, client, func(ctx context.Context) error {
		return s.querySessionPreKeys(ctx, client, f)
	})
}

func (s *TCPServer) querySessionPreKeys(ctx context.Context, client *Client, f *netproto.Frame) error {
	var msg netproto.PreKeyQuery
	if err := netproto.Decode(f, &msg); err != nil || msg.UniqueID == "" {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "target unique ID is required")
	}
	if client.userID() <= 0 {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodePermissionDenied, "registered account required for asynchronous prekeys")
	}
	if s.chatRate != nil && !s.chatRate.allow(client.UniqueID+":prekey", time.Now()) {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeMalformed, "prekey query rate limit exceeded")
	}
	if s.deps == nil || s.deps.PreKeys == nil || s.deps.Auth == nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "prekey directory unavailable")
	}
	target, err := s.deps.Auth.LookupUser(ctx, msg.UniqueID)
	if err != nil {
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "prekey bundle not found")
	}
	bundle, err := s.deps.PreKeys.ConsumePreKeyBundle(ctx, target.ID)
	if err != nil {
		if errors.Is(err, store.ErrNoPreKeyBundle) {
			return s.sendErrorFor(client, requestOrigin(ctx), errCodeNotFound, "prekey bundle not found")
		}
		return s.sendErrorFor(client, requestOrigin(ctx), errCodeUnavailable, "loading prekey bundle failed")
	}
	response := netproto.PreKeyBundle{
		UniqueID: msg.UniqueID, IdentityDH: bundle.IdentityDH, SigningPublic: bundle.SigningPublic,
		SignedPreKeyID: bundle.SignedPreKeyID, SignedPreKey: bundle.SignedPreKey, Signature: bundle.Signature,
	}
	if bundle.OneTimePreKey != nil {
		response.OneTimeKeyID = bundle.OneTimePreKey.KeyID
		response.OneTimePreKey = bundle.OneTimePreKey.PublicKey
	}
	return s.writeMessage(client, netproto.MsgPreKeyBundle, response)
}
