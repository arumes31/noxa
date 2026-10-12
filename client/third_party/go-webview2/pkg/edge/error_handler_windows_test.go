//go:build windows

package edge

import (
	"bytes"
	"errors"
	"log"
	"strings"
	"testing"
)

func TestGlobalErrorHandlerUsesLogSink(t *testing.T) {
	// GUI builds have no usable stdout. Error details must reach the same
	// configured logger as the stack, and this test must not run in parallel.
	writer, flags, prefix := log.Writer(), log.Flags(), log.Prefix()
	t.Cleanup(func() {
		log.SetOutput(writer)
		log.SetFlags(flags)
		log.SetPrefix(prefix)
	})
	var output bytes.Buffer
	log.SetOutput(&output)
	log.SetFlags(0)
	log.SetPrefix("")

	globalErrorHandler(nil)
	if output.Len() != 0 {
		t.Fatal("nil error produced a log entry")
	}
	message := "error creating controller with 80070005: Access is denied."
	globalErrorHandler(errors.New(message))
	for _, want := range []string{"[WebView2 Error] " + message, "Stack trace:", "TestGlobalErrorHandlerUsesLogSink"} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("log sink missing %q: %s", want, output.String())
		}
	}
}
