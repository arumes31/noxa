package grpcserver

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	noxav1 "noxa/v1"
)

// These field numbers and kinds were published before the roles-v1 migration.
// Retiring a field must not let a new field reinterpret an older client's bytes.
func TestAuthenticateHistoricalWireFields(t *testing.T) {
	for _, test := range []struct {
		message proto.Message
		name    protoreflect.Name
		number  protoreflect.FieldNumber
		kind    protoreflect.Kind
	}{
		{&noxav1.AuthenticateRequest{}, "username", 1, protoreflect.StringKind},
		{&noxav1.AuthenticateRequest{}, "password", 2, protoreflect.StringKind},
		{&noxav1.AuthenticateRequest{}, "token", 3, protoreflect.StringKind},
		{&noxav1.AuthenticateRequest{}, "client_version", 4, protoreflect.StringKind},
		{&noxav1.AuthenticateRequest{}, "metadata", 5, protoreflect.MessageKind},
		{&noxav1.AuthenticateResponse{}, "success", 1, protoreflect.BoolKind},
		{&noxav1.AuthenticateResponse{}, "session_token", 2, protoreflect.StringKind},
		{&noxav1.AuthenticateResponse{}, "user_id", 3, protoreflect.StringKind},
		{&noxav1.AuthenticateResponse{}, "display_name", 4, protoreflect.StringKind},
		{&noxav1.AuthenticateResponse{}, "expires_at", 5, protoreflect.Int64Kind},
		{&noxav1.AuthenticateResponse{}, "error", 6, protoreflect.StringKind},
	} {
		descriptor := test.message.ProtoReflect().Descriptor()
		t.Run(string(descriptor.Name())+"/"+string(test.name), func(t *testing.T) {
			field := descriptor.Fields().ByName(test.name)
			if field == nil || field.Number() != test.number || field.Kind() != test.kind {
				t.Fatalf("historical field %s = %v, want %s field %d", test.name, field, test.kind, test.number)
			}
			if test.name == "metadata" && (!field.IsMap() || field.MapKey().Kind() != protoreflect.StringKind || field.MapValue().Kind() != protoreflect.StringKind) {
				t.Fatalf("metadata descriptor = %v, want map<string, string>", field)
			}
		})
	}
}

func TestAuthenticateUserIDUsesHistoricalWireTag(t *testing.T) {
	// Field 3, wire type 2, length 3, followed by the user ID "uid".
	wire := []byte{0x1a, 0x03, 'u', 'i', 'd'}
	var response noxav1.AuthenticateResponse
	if err := proto.Unmarshal(wire, &response); err != nil {
		t.Fatal(err)
	}
	if response.GetUserId() != "uid" {
		t.Errorf("decoded historical response user ID = %q, want uid", response.GetUserId())
	}
	encoded, err := proto.Marshal(&noxav1.AuthenticateResponse{UserId: "uid"})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, wire) {
		t.Errorf("encoded user ID = %x, want historical field 3 bytes %x", encoded, wire)
	}
}

func TestRetiredControlRPCDescriptorsRemainDeprecated(t *testing.T) {
	service := noxav1.File_control_proto.Services().ByName("Control")
	for _, name := range []protoreflect.Name{"CreateChannel", "DeleteChannel", "QueryPermissions"} {
		t.Run(string(name), func(t *testing.T) {
			method := service.Methods().ByName(name)
			if method == nil {
				t.Fatalf("retired %s descriptor must remain for source compatibility", name)
			}
			options, ok := method.Options().(*descriptorpb.MethodOptions)
			if !ok || !options.GetDeprecated() {
				t.Fatalf("retired %s deprecated option = %v, want true", name, options)
			}
		})
	}
}
