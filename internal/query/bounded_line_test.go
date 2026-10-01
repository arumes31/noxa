package query

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

type countedLineReader struct {
	r io.Reader
	n int
}

func (r *countedLineReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.n += n
	return n, err
}

func TestQueryRejectsOversizedLineBeforeTerminator(t *testing.T) {
	for _, suffix := range []string{"", "\nversion\n"} {
		t.Run("suffix="+suffix, func(t *testing.T) {
			s := New("", nil, &roleQueryBackend{})
			s.MaxLineLength = 32
			input := &countedLineReader{r: strings.NewReader(strings.Repeat("x", 64*1024) + suffix)}
			var output bytes.Buffer
			s.runSession(context.Background(), &session{r: bufio.NewReaderSize(input, 16), w: &output})
			if input.n > 64 {
				t.Fatalf("read %d bytes before rejecting a 32-byte command limit", input.n)
			}
			if !strings.Contains(output.String(), "line\\stoo\\slong") {
				t.Fatalf("missing line-length rejection: %q", output.String())
			}
			if strings.Contains(output.String(), "version=") {
				t.Fatal("oversized command stream was executed after rejection")
			}
		})
	}
}

func TestQueryLineLimitBoundaryAndEOF(t *testing.T) {
	for _, input := range []string{"help\n", "help\r\n", "help"} {
		t.Run(input, func(t *testing.T) {
			s := New("", nil, &roleQueryBackend{})
			s.MaxLineLength = 4
			var output bytes.Buffer
			s.runSession(context.Background(), &session{r: bufio.NewReader(strings.NewReader(input)), w: &output})
			if strings.Contains(output.String(), "line\\stoo\\slong") {
				t.Fatalf("boundary command rejected: %q", output.String())
			}
			if input == "help" && output.Len() != 0 {
				t.Fatal("unterminated command executed at EOF")
			}
			if input != "help" && output.Len() == 0 {
				t.Fatal("complete command was not executed")
			}
		})
	}
}
