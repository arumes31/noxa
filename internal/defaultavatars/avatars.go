// Package defaultavatars provides immutable, identity-stable profile portraits.
// Uploaded avatars take precedence; callers must authorize the lookup first.
package defaultavatars

import (
	"crypto/sha256"
	"embed"
	"encoding/binary"
	"fmt"
	"io/fs"
)

// Count and the numbered portraits form the v1 assignment contract. Do not
// reorder or expand this pool: doing so would change existing users' pictures.
const Count = 100

//go:embed portraits/*.webp
var portraits embed.FS

// Get returns a local 256px WebP selected by account identity, never nickname
// or device/session ID. It does not persist a profile or infer a user's gender.
func Get(uniqueID string) ([]byte, error) {
	if uniqueID == "" {
		return nil, fs.ErrNotExist
	}
	hash := sha256.Sum256([]byte(uniqueID))
	number := binary.BigEndian.Uint32(hash[:4])%Count + 1
	return portraits.ReadFile(fmt.Sprintf("portraits/%03d.webp", number))
}
