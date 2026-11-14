//go:build windows

package main

import (
	"context"
	"log"
	"os"

	"github.com/wailsapp/go-webview2/pkg/edge"
)

func updateBrowserArguments() string { return edge.UpdateBrowserArguments() }

// allowUpdateForeground lets the replacement bring its window to the front
// when it takes over from this (foreground) process.
func allowUpdateForeground(pid int) {
	_, _, _ = user32.NewProc("AllowSetForegroundWindow").Call(uintptr(pid)) // #nosec G115 -- positive OS process ID.
}

// holdUpdateParent opens the old process while it is certainly alive, so the
// returned wait cannot follow a reused process ID after it exits.
func holdUpdateParent() func(context.Context) error {
	parent, err := openUpdateProcess(updateProcessIdentity{PID: uint32(os.Getppid())}) // #nosec G115 -- OS process IDs fit DWORD.
	if err != nil {
		return func(context.Context) error { return err }
	}
	return func(ctx context.Context) error {
		defer parent.close()
		return parent.wait(ctx)
	}
}

// prepareIncomingHandover ends a handover process whose browser options
// differ from the old process's: WebView2 cannot share that browser and would
// fail visibly. The old process then restarts as before.
func prepareIncomingHandover() {
	h := incomingHandover
	if h == nil {
		return
	}
	h.parentWait = holdUpdateParent()
	edge.SetHandoverBrowserCheck(func(arguments string) {
		if arguments == h.config.BrowserArgs {
			return
		}
		log.Print("update handover: browser options changed; handing the session back for a restart")
		if err := h.signal(handoverAbandoned); err != nil {
			log.Printf("update handover: abandon signal: %v", err)
		}
		os.Exit(0)
	})
}
