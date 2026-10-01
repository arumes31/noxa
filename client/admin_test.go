package main

import (
	"testing"

	"noxa/internal/netproto"
)

func TestComplaintBindings(t *testing.T) {
	frames := make(chan *netproto.Frame, 4)
	app, _ := newPipedApp(t, func(frame *netproto.Frame) (netproto.MessageType, any, bool) {
		frames <- frame
		switch netproto.MessageType(frame.Type) {
		case netproto.MsgComplaintList, netproto.MsgComplaintClear:
			return netproto.MsgComplaints, netproto.Complaints{
				Entries: []netproto.ComplaintEntry{{TargetUniqueID: "target", FromUniqueID: "sender", Reason: "spam"}},
			}, true
		default:
			return 0, nil, false
		}
	})

	complaints, err := app.ComplaintList()
	if err != nil || len(complaints.Entries) != 1 || complaints.Entries[0].Reason != "spam" {
		t.Fatalf("ComplaintList = %+v, %v", complaints, err)
	}
	nextFrame(t, frames, netproto.MsgComplaintList)

	complaints, err = app.ComplaintClear("target", "sender")
	if err != nil || len(complaints.Entries) != 1 {
		t.Fatalf("ComplaintClear = %+v, %v", complaints, err)
	}
	var clear netproto.ComplaintClear
	if err := netproto.Decode(nextFrame(t, frames, netproto.MsgComplaintClear), &clear); err != nil ||
		clear.TargetUniqueID != "target" || clear.FromUniqueID != "sender" {
		t.Fatalf("ComplaintClear payload = %+v, %v", clear, err)
	}

	app.cmLoad().disconnect()
	if _, err := app.ComplaintList(); err == nil {
		t.Fatal("ComplaintList succeeded after disconnect")
	}
}
