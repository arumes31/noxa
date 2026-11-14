// Package updatepatch creates and applies binary delta updates for the desktop
// client. A patch is a single zstd frame of the new executable compressed with
// the old executable as a raw dictionary ("patch-from"), so unchanged regions
// become long back-references. Patches are never trusted on their own: the
// updater compares the reconstructed file with the signed manifest hash.
package updatepatch

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math/bits"

	"github.com/klauspost/compress/zstd"
)

// MaxFileSize bounds the old and new executables a patch may relate.
const MaxFileSize = 512 << 20

// dictID is fixed so a patch only decodes with an explicit raw dictionary.
const dictID = 1

// windowFor returns a window that lets matches reach the whole dictionary.
func windowFor(oldSize, newSize int) int {
	window := 1 << bits.Len(uint(oldSize+newSize))
	return max(min(window, zstd.MaxWindowSize), zstd.MinWindowSize)
}

// Create returns a patch that turns oldFile into newFile.
func Create(oldFile, newFile []byte) ([]byte, error) {
	if len(oldFile) == 0 || len(oldFile) > MaxFileSize || len(newFile) > MaxFileSize {
		return nil, fmt.Errorf("patch inputs must be between 1 and %d bytes", MaxFileSize)
	}
	enc, err := zstd.NewWriter(nil,
		zstd.WithEncoderDictRaw(dictID, oldFile),
		zstd.WithWindowSize(windowFor(len(oldFile), len(newFile))),
		zstd.WithEncoderLevel(zstd.SpeedBestCompression),
		zstd.WithEncoderConcurrency(1),
		zstd.WithEncoderCRC(true),
	)
	if err != nil {
		return nil, fmt.Errorf("create patch encoder: %w", err)
	}
	defer func() { _ = enc.Close() }()
	return enc.EncodeAll(newFile, nil), nil
}

// Apply reconstructs the new file from oldFile and patch, refusing output
// larger than maxSize.
func Apply(oldFile []byte, patch io.Reader, maxSize int64) ([]byte, error) {
	if len(oldFile) == 0 || len(oldFile) > MaxFileSize {
		return nil, fmt.Errorf("patch base must be between 1 and %d bytes", MaxFileSize)
	}
	maxSize = min(maxSize, MaxFileSize)
	dec, err := zstd.NewReader(patch,
		zstd.WithDecoderDictRaw(dictID, oldFile),
		zstd.WithDecoderMaxWindow(uint64(windowFor(len(oldFile), int(maxSize)))),
		zstd.WithDecoderMaxMemory(uint64(maxSize)),
		zstd.WithDecoderConcurrency(1),
	)
	if err != nil {
		return nil, fmt.Errorf("create patch decoder: %w", err)
	}
	defer dec.Close()
	var out bytes.Buffer
	n, err := io.Copy(&out, io.LimitReader(dec, maxSize+1))
	if err != nil {
		return nil, fmt.Errorf("apply patch: %w", err)
	}
	if n > maxSize {
		return nil, errors.New("patched file exceeds size limit")
	}
	return out.Bytes(), nil
}
