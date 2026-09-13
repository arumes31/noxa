//go:build integration

package main

import (
	"bytes"
	"debug/pe"
	"encoding/binary"
	"testing"
)

// Every variant gets its own valid signed manifest in the real updater harness.
// Mutations start from a runnable Windows probe, never from the installed app.
func invalidUpdatePlatforms(t *testing.T, valid []byte) map[string][]byte {
	t.Helper()
	header := int(binary.LittleEndian.Uint32(valid[0x3c:]))
	mutate := func(change func([]byte)) []byte {
		b := bytes.Clone(valid)
		change(b)
		return b
	}
	parsed, err := pe.NewFile(bytes.NewReader(valid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = parsed.Close() }()
	truncated := mutate(func(b []byte) {
		// Remove COFF symbol references so parsing can reach the truncated
		// section: debug/pe itself does not read or validate all section bytes.
		clear(b[header+12 : header+20])
	})
	first := parsed.Sections[0]
	truncated = truncated[:int(first.Offset)+int(first.Size)-1]
	return map[string][]byte{
		"signed Linux ELF": buildUpdateProbeFor(t, "linux", "amd64"),
		"signed wrong architecture": mutate(func(b []byte) {
			binary.LittleEndian.PutUint16(b[header+4:], pe.IMAGE_FILE_MACHINE_ARM64)
		}),
		"signed DLL": mutate(func(b []byte) {
			flags := binary.LittleEndian.Uint16(b[header+22:])
			binary.LittleEndian.PutUint16(b[header+22:], flags|pe.IMAGE_FILE_DLL)
		}),
		"signed non executable": mutate(func(b []byte) {
			flags := binary.LittleEndian.Uint16(b[header+22:])
			binary.LittleEndian.PutUint16(b[header+22:], flags&^pe.IMAGE_FILE_EXECUTABLE_IMAGE)
		}),
		"signed malformed PE": mutate(func(b []byte) { b[header] = 0 }),
		"signed PE32 header": mutate(func(b []byte) {
			binary.LittleEndian.PutUint16(b[header+24:], 0x10b)
		}),
		"signed truncated headers": bytes.Clone(valid[:80]),
		"signed truncated section": truncated,
		"signed invalid entry point": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+16:], 0xffffffff)
		}),
		"signed unsupported subsystem": mutate(func(b []byte) {
			binary.LittleEndian.PutUint16(b[header+24+68:], pe.IMAGE_SUBSYSTEM_NATIVE)
		}),
		"signed zero file alignment": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+36:], 0)
		}),
		"signed zero section alignment": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+32:], 0)
		}),
		"signed undersized headers": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+60:], 1)
		}),
		"signed non power of two file alignment": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+36:], 768)
		}),
		"signed file alignment exceeds section alignment": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+36:], 8192)
		}),
		"signed unequal subpage alignments": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+32:], 1024)
		}),
		"signed unaligned headers": mutate(func(b []byte) {
			size := binary.LittleEndian.Uint32(b[header+24+60:])
			binary.LittleEndian.PutUint32(b[header+24+60:], size-1)
		}),
		"signed aligned but undersized headers": mutate(func(b []byte) {
			binary.LittleEndian.PutUint32(b[header+24+60:], 512)
		}),
	}
}
