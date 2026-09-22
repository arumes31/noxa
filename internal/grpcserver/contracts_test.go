package grpcserver

import (
	"reflect"
	"sort"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"

	"noxa/internal/eventbus"
	"noxa/internal/query"
	noxav1 "noxa/v1"
)

func TestGeneratedServiceContracts(t *testing.T) {
	for _, test := range []struct {
		name string
		file protoreflect.FileDescriptor
	}{
		{name: "Chat", file: noxav1.File_chat_proto},
		{name: "Signaling", file: noxav1.File_signaling_proto},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := test.file.Services().ByName(protoreflect.Name(test.name))
			if service == nil {
				t.Fatalf("%s service descriptor is missing", test.name)
			}
			options, ok := service.Options().(*descriptorpb.ServiceOptions)
			if !ok || !options.GetDeprecated() {
				t.Fatalf("%s deprecated option = %v, want true", test.name, options)
			}
		})
	}

	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	server := New("127.0.0.1:12338", &stubBackend{}, bus, zap.NewNop(), nil)
	services := server.grpc.GetServiceInfo()
	got := make([]string, 0, len(services))
	for name := range services {
		got = append(got, name)
	}
	sort.Strings(got)
	want := []string{"voicx.v1.Control", "voicx.v1.Events"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered gRPC services = %v, want %v", got, want)
	}
}

func TestGeneratedRolesAuthenticationContract(t *testing.T) {
	request := (&noxav1.AuthenticateRequest{}).ProtoReflect().Descriptor()
	response := (&noxav1.AuthenticateResponse{}).ProtoReflect().Descriptor()
	if request.Fields().Len() != 2 || response.Fields().Len() != 1 {
		t.Fatalf("authentication fields = %d request, %d response; want 2 and 1", request.Fields().Len(), response.Fields().Len())
	}
	for _, name := range []protoreflect.Name{"username", "password"} {
		field := request.Fields().ByName(name)
		if field == nil || field.Kind() != protoreflect.StringKind {
			t.Fatalf("AuthenticateRequest field %s = %v", name, field)
		}
	}
	userID := response.Fields().ByName("user_id")
	if userID == nil || userID.Number() != 1 || userID.Kind() != protoreflect.StringKind {
		t.Fatalf("AuthenticateResponse user_id descriptor = %v, want string field 1", userID)
	}
}

func TestUserBannedEventChannelFieldContract(t *testing.T) {
	descriptor := (&noxav1.UserBannedEvent{}).ProtoReflect().Descriptor()
	field := descriptor.Fields().ByName("channel_id")
	if field == nil || field.Number() != 5 || field.Kind() != protoreflect.StringKind {
		t.Fatalf("UserBannedEvent channel_id descriptor = %v, want string field 5", field)
	}
}

func TestListChannelsSkipsOnlyMalformedRows(t *testing.T) {
	channels := []query.ChannelInfo{
		{ChannelID: 1, Name: "valid", MaxClients: 3, ClientCount: 1},
		{ChannelID: 2, Name: "bad max", MaxClients: -1},
		{ChannelID: 3, Name: "bad count", ClientCount: -1},
	}
	service := &controlService{logger: zap.NewNop()}
	response := service.channelListResponse(channels, 0)
	if len(response.GetChannels()) != 1 || response.GetChannels()[0].GetId() != "1" {
		t.Fatalf("ListChannels retained %+v, want only valid channel 1", response.GetChannels())
	}
}

func TestAllFileTransferRPCsAreClosedWithoutRoles(t *testing.T) {
	bus := eventbus.New(zap.NewNop())
	defer bus.Close()
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, &stubBackend{}, bus)))
	ctx := authCtx(t, "admin-uid", "pw")
	for _, test := range []struct {
		name string
		call func() error
	}{
		{name: "start", call: func() error {
			_, err := client.StartFileTransfer(ctx, &noxav1.StartFileTransferRequest{})
			return err
		}},
		{name: "status", call: func() error {
			_, err := client.GetFileTransferStatus(ctx, &noxav1.GetFileTransferStatusRequest{})
			return err
		}},
		{name: "cancel", call: func() error {
			_, err := client.CancelFileTransfer(ctx, &noxav1.CancelFileTransferRequest{})
			return err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); status.Code(err) != codes.FailedPrecondition {
				t.Fatalf("%s file transfer RPC = %v, want FailedPrecondition", test.name, err)
			}
		})
	}
}
