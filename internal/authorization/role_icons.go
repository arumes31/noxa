package authorization

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image/png"
	"strings"
)

const MaxRoleIconBytes = 8 * 1024
const MaxRoleIconDimension = 32

// Bound the shared snapshot image dictionary and management response below the frame limit.
const MaxRoleIconImagesEncoded = 128 * 1024
const roleIconPrefix = "data:image/png;base64,"

func RoleIconImage(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
func cloneRoleIcon(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func validateRoleIcon(value *string) error {
	raw := RoleIconImage(value)
	if raw == "" {
		return nil
	}
	if len(raw) > len(roleIconPrefix)+base64.StdEncoding.EncodedLen(MaxRoleIconBytes) || !strings.HasPrefix(raw, roleIconPrefix) {
		return fmt.Errorf("role icon must be a PNG up to 8 KiB and 32 x 32 pixels")
	}
	encoded := strings.TrimPrefix(raw, roleIconPrefix)
	data, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(data) > MaxRoleIconBytes || base64.StdEncoding.EncodeToString(data) != encoded {
		return fmt.Errorf("invalid role icon encoding")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > MaxRoleIconDimension || cfg.Height > MaxRoleIconDimension {
		return fmt.Errorf("role icon must be at most 32 x 32 pixels")
	}
	if _, err = png.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("invalid role icon PNG")
	}
	return nil
}

// RoleImage exposes only one public cosmetic, without copying authority data.
func (e *RoleEvaluator) RoleImage(roleID int64) string {
	return RoleIconImage(e.roles[roleID].IconImage)
}
