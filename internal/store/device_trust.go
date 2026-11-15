package store

import (
	"noxa/internal/devicetrust"
	"noxa/internal/netproto"
)

// DMTrusted returns the active devices of principal that clients accept for
// direct messages: those chained to the first device through approvals by
// active devices (devicetrust.Chain, the clients' rule). A device whose
// approver was revoked drops out. If the chain cannot be walked at all (no
// valid first device), every first or approved device is kept as before:
// clients then refuse the whole directory anyway.
func DMTrusted(principal string, devices []Device) []Device {
	list := make([]netproto.DMDevice, 0, len(devices))
	for _, d := range devices {
		if d.Active() {
			list = append(list, netproto.DMDevice{DeviceID: d.DeviceID, Kind: d.Kind, Ed25519Public: d.Ed25519Public, X25519Public: d.X25519Public,
				FirstDevice: d.FirstDevice, ApprovedBy: d.ApprovedBy, ApprovalSignature: d.ApprovalSignature})
		}
	}
	chain, err := devicetrust.Chain(principal, list)
	trusted := make(map[string]bool, len(chain))
	for _, d := range chain {
		trusted[d.ID] = true
	}
	out := make([]Device, 0, len(devices))
	for _, d := range devices {
		if d.Active() && (trusted[d.DeviceID] || err != nil && (d.FirstDevice || len(d.ApprovalSignature) > 0)) {
			out = append(out, d)
		}
	}
	return out
}
