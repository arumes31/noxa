package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"testing"
	"testing/synctest"
	"time"
)

func TestQueryRunRejectsFailedOrEmptyWorkloads(t *testing.T) {
	for _, tc := range []struct {
		name      string
		response  string
		rate      int
		wantError bool
	}{
		{"successful commands", "error id=0 msg=ok", 100, false},
		{"rejected commands", "error id=256 msg=denied", 100, true},
		{"no completed command", "error id=0 msg=ok", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client, peer := net.Pipe()
				defer func() { _ = peer.Close() }()
				go func() {
					reader := bufio.NewReader(peer)
					for {
						if _, err := reader.ReadString('\n'); err != nil {
							return
						}
						if _, err := fmt.Fprintln(peer, tc.response); err != nil {
							return
						}
					}
				}()
				err := runWithDeps(context.Background(), options{connections: 1, rate: tc.rate, duration: 105 * time.Millisecond, command: "clientlist"}, queryloadDeps{
					dialAndLogin: func(options) (net.Conn, *bufio.Reader, error) { return client, bufio.NewReader(client), nil },
					newTicker:    func(d time.Duration) queryloadTicker { return stdQueryloadTicker{time.NewTicker(d)} },
					now:          time.Now,
					report:       func(options, *result) {},
				})
				if (err != nil) != tc.wantError {
					t.Fatalf("run error = %v, want error %v", err, tc.wantError)
				}
			})
		})
	}
}
