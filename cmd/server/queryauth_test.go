package main

import (
	"reflect"
	"testing"

	"noxa/internal/query"
)

func TestQueryBackendHasNoLegacyPasswordAuthentication(t *testing.T) {
	var _ query.Backend = (*queryBackend)(nil)
	if _, ok := reflect.TypeFor[*queryBackend]().MethodByName("Authenticate"); ok {
		t.Fatal("query backend still exposes legacy password authentication")
	}
}
