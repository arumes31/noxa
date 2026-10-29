// Test-only JSON pipe host for real native/browser interoperability.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"noxa/internal/e2ee/ratchet"
	"os"
)

func main() {
	ctx := context.Background()
	e, err := ratchet.New(ctx)
	if err != nil {
		panic(err)
	}
	defer e.Close(ctx)
	s := bufio.NewScanner(os.Stdin)
	s.Buffer(make([]byte, 4096), ratchet.MaxInput)
	enc := json.NewEncoder(os.Stdout)
	for s.Scan() {
		var r ratchet.Request
		if json.Unmarshal(s.Bytes(), &r) != nil {
			_ = enc.Encode(map[string]string{"error": "invalid request"})
			continue
		}
		out, err := e.Apply(ctx, r)
		clear(r.Key)
		if err != nil {
			_ = enc.Encode(map[string]string{"error": err.Error()})
		} else {
			_ = enc.Encode(out)
		}
	}
	if s.Err() != nil {
		os.Exit(1)
	}
}
