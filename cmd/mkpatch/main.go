// Command mkpatch writes a binary delta update that turns one client
// executable into another (see internal/updatepatch). The release pipeline
// uses it so CI and the client tests share one patch format.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"os"

	"noxa/internal/updatepatch"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "mkpatch: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("mkpatch", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	oldPath := flags.String("old", "", "previous executable")
	newPath := flags.String("new", "", "new executable")
	outPath := flags.String("out", "", "patch output path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *oldPath == "" || *newPath == "" || *outPath == "" || flags.NArg() != 0 {
		return fmt.Errorf("usage: mkpatch -old OLD -new NEW -out PATCH")
	}
	oldFile, err := os.ReadFile(*oldPath)
	if err != nil {
		return err
	}
	newFile, err := os.ReadFile(*newPath)
	if err != nil {
		return err
	}
	patch, err := updatepatch.Create(oldFile, newFile)
	if err != nil {
		return err
	}
	// Self-check: never publish a patch that does not reproduce the new file.
	got, err := updatepatch.Apply(oldFile, bytes.NewReader(patch), int64(len(newFile)))
	if err != nil {
		return fmt.Errorf("verify patch: %w", err)
	}
	if !bytes.Equal(got, newFile) {
		return fmt.Errorf("verify patch: output differs from %s", *newPath)
	}
	return os.WriteFile(*outPath, patch, 0o644)
}
