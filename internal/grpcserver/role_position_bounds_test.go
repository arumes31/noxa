package grpcserver

import (
	"math"
	"strconv"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/authorization"
	"noxa/internal/netproto"
)

func TestRoleStateToProtoPreservesPositionBounds(t *testing.T) {
	for _, position := range []int{0, 1, math.MaxInt32} {
		t.Run(strconv.Itoa(position), func(t *testing.T) {
			response, err := roleStateToProto(netproto.RoleState{Policy: authorization.RolePolicy{
				Roles: []authorization.Role{{ID: 1, Position: position}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			roles := response.GetPolicy().GetRoles()
			if len(roles) != 1 || int64(roles[0].GetPosition()) != int64(position) {
				t.Fatalf("roles = %v, want one role with position %d", roles, position)
			}
		})
	}
}

func TestRoleStateToProtoRejectsInvalidPosition(t *testing.T) {
	positions := []int{-1, math.MinInt32}
	if strconv.IntSize == 64 {
		for _, position := range []int64{math.MinInt32 - 1, math.MaxInt32 + 1} {
			positions = append(positions, int(position))
		}
	}
	for _, position := range positions {
		t.Run(strconv.Itoa(position), func(t *testing.T) {
			response, err := roleStateToProto(netproto.RoleState{Policy: authorization.RolePolicy{
				Roles: []authorization.Role{{ID: 1, Position: 1}, {ID: 2, Position: position}},
			}})
			if status.Code(err) != codes.Internal || response != nil {
				t.Fatalf("role state with position %d = %v, %v; want no response and Internal", position, response, err)
			}
		})
	}
}
