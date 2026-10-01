// Package main exposes complaint moderation to the desktop client.
package main

import (
	"time"

	"noxa/internal/netproto"
)

// --- complaints (173) ---------------------------------------------------------

// ComplaintList returns every filed complaint. Gated server-side by the same
// check as the ban list, so a denial arrives as MsgError (code 4).
func (a *App) ComplaintList() (netproto.Complaints, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.Complaints{}, err
	}
	return m.complaintList()
}

// ComplaintListForTab rejects operations from a different server's dialog.
func (a *App) ComplaintListForTab(tabID string) (netproto.Complaints, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.Complaints{}, err
	}
	return m.complaintList()
}

func (m *connManager) complaintList() (netproto.Complaints, error) {
	f, err := m.request(netproto.MsgComplaintList, netproto.MsgComplaints,
		netproto.ComplaintList{}, 5*time.Second)
	if err != nil {
		return netproto.Complaints{}, err
	}
	var resp netproto.Complaints
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.Complaints{}, err
	}
	return resp, nil
}

// ComplaintClear resolves complaints against a target and returns the
// refreshed list. An empty fromUniqueID clears every complaint against the
// target.
func (a *App) ComplaintClear(targetUniqueID, fromUniqueID string) (netproto.Complaints, error) {
	m, err := a.requireCM()
	if err != nil {
		return netproto.Complaints{}, err
	}
	return m.complaintClear(targetUniqueID, fromUniqueID)
}

// ComplaintClearForTab rejects operations from a different server's dialog.
func (a *App) ComplaintClearForTab(tabID string, targetUniqueID, fromUniqueID string) (netproto.Complaints, error) {
	m, err := a.requireTabCM(tabID)
	if err != nil {
		return netproto.Complaints{}, err
	}
	return m.complaintClear(targetUniqueID, fromUniqueID)
}

func (m *connManager) complaintClear(targetUniqueID, fromUniqueID string) (netproto.Complaints, error) {
	f, err := m.request(netproto.MsgComplaintClear, netproto.MsgComplaints,
		netproto.ComplaintClear{TargetUniqueID: targetUniqueID, FromUniqueID: fromUniqueID}, 5*time.Second)
	if err != nil {
		return netproto.Complaints{}, err
	}
	var resp netproto.Complaints
	if err := decodeJSON(f, &resp); err != nil {
		return netproto.Complaints{}, err
	}
	return resp, nil
}
