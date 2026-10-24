package store

import (
	"context"
	"database/sql"
	"errors"
	"noxa/internal/netproto"
)

// attachConversationSenderChains preserves proof of who sent persisted group
// messages even after sign-out or idle expiry revokes their device. This proof
// travels only with authorized history; the live encryption directory continues
// to exclude revoked devices.
func attachConversationSenderChains(ctx context.Context, tx *sql.Tx, result *netproto.ConversationResult) error {
	chains := map[string][]netproto.DMDevice{}
	for i := range result.Messages {
		message := &result.Messages[i]
		if message.Body == "" || message.SenderDeviceID == "" {
			continue
		}
		cacheKey := netproto.DeviceEnvelopeKey(message.FromUniqueID, message.SenderDeviceID)
		chain, cached := chains[cacheKey]
		if !cached {
			seen := map[string]bool{}
			id := message.SenderDeviceID
			for id != "" && !seen[id] && len(chain) < netproto.MaxConversationSenderChain {
				seen[id] = true
				d, err := scanDevice(tx.QueryRowContext(ctx, `SELECT `+deviceColumns+` FROM user_devices WHERE principal=$1 AND device_id=$2`, message.FromUniqueID, id))
				if errors.Is(err, sql.ErrNoRows) {
					break
				}
				if err != nil {
					return err
				}
				chain = append(chain, netproto.DMDevice{DeviceID: d.DeviceID, Kind: d.Kind, Ed25519Public: d.Ed25519Public,
					X25519Public: d.X25519Public, FirstDevice: d.FirstDevice, ApprovedBy: d.ApprovedBy, ApprovalSignature: d.ApprovalSignature})
				if d.FirstDevice {
					break
				}
				id = d.ApprovedBy
			}
			chains[cacheKey] = chain
		}
		message.SenderDeviceChain = chain
	}
	return nil
}
