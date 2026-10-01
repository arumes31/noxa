package main

import (
	"noxa/internal/query"
	"reflect"
	"testing"
)

func TestQueryBackendExposesOnlyRoleIntegrationMethods(t *testing.T) {
	var _ query.Backend = (*queryBackend)(nil)
	for _, name := range []string{"CreateChannel", "DeleteChannel", "ListChannels", "KickClient", "BanClient", "Authenticate"} {
		if _, ok := reflect.TypeFor[*queryBackend]().MethodByName(name); ok {
			t.Errorf("query backend still exposes retired method %s", name)
		}
	}
}
