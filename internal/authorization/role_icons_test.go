package authorization

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"
)

func iconPNG(t *testing.T, size int) string {
	t.Helper()
	var b bytes.Buffer
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.Set(x, y, color.NRGBA{uint8(x * 7), uint8(y * 7), 255, 255})
		}
	}
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return roleIconPrefix + base64.StdEncoding.EncodeToString(b.Bytes())
}
func TestRoleIconValidation(t *testing.T) {
	valid := iconPNG(t, 32)
	for _, tt := range []struct {
		name, value string
		valid       bool
	}{
		{"png", valid, true}, {"empty", "", true}, {"oversized dimensions", iconPNG(t, 33), false},
		{"url", "https://example.com/icon.png", false}, {"svg", "data:image/svg+xml;base64,PHN2Zz4=", false},
		{"bad base64", roleIconPrefix + "%%%%", false}, {"corrupt png", valid[:len(valid)-16], false},
		{"over byte limit", roleIconPrefix + base64.StdEncoding.EncodeToString(make([]byte, MaxRoleIconBytes+1)), false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := roleFixture()
			p.Roles[1].IconImage = &tt.value
			_, err := NewRoleEvaluator(p)
			if (err == nil) != tt.valid {
				t.Fatalf("valid=%t err=%v", tt.valid, err)
			}
		})
	}
}
func TestRoleIconUpdateCompatibilityAndIsolation(t *testing.T) {
	p := roleFixture()
	value := iconPNG(t, 16)
	p.Roles[1].IconImage = &value
	e, err := NewRoleEvaluator(p)
	if err != nil {
		t.Fatal(err)
	}
	*p.Roles[1].IconImage = "changed"
	copy := e.Policy()
	*copy.Roles[1].IconImage = "also changed"
	original := e.RoleImage(20)
	if !strings.HasPrefix(original, roleIconPrefix) {
		t.Fatal("mutable evaluator image")
	}
	p = e.Policy()
	role := p.Roles[1]
	role.IconImage = nil
	role.Name = "Renamed by old client"
	next, err := ApplyRoleChange(p, 1, RoleChange{Kind: RoleUpdate, ExpectedRevision: p.Revision, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	if RoleIconImage(next.Roles[1].IconImage) != original {
		t.Fatal("old client erased image")
	}
	empty := ""
	role = next.Roles[1]
	role.IconImage = &empty
	removed, err := ApplyRoleChange(next, 1, RoleChange{Kind: RoleUpdate, ExpectedRevision: next.Revision, Role: role})
	if err != nil {
		t.Fatal(err)
	}
	empty = "mutated input"
	if RoleIconImage(removed.Roles[1].IconImage) != "" {
		t.Fatal("request aliases result")
	}
	if _, err := ApplyRoleChange(next, 3, RoleChange{Kind: RoleUpdate, ExpectedRevision: next.Revision, Role: role}); err != ErrRoleForbidden {
		t.Fatalf("member changed image: %v", err)
	}
}
func TestRoleIconPolicyBudget(t *testing.T) {
	p := roleFixture()
	value := iconPNG(t, 32)
	// Valid PNG trailing padding remains within the per-image byte limit.
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, roleIconPrefix))
	raw = append(raw, make([]byte, MaxRoleIconBytes-len(raw))...)
	value = roleIconPrefix + base64.StdEncoding.EncodeToString(raw)
	for i := 0; i < 20; i++ {
		p.Roles = append(p.Roles, Role{ID: int64(100 + i), Name: "Image", Position: 4 + i, IconImage: &value})
	}
	if _, err := NewRoleEvaluator(p); err == nil {
		t.Fatal("accepted oversized image dictionary")
	}
}
