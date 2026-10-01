//go:build integration

package grpcserver

import (
	"context"
	"testing"

	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"noxa/internal/auth"
	"noxa/internal/authorization"
	"noxa/internal/config"
	"noxa/internal/query"
	"noxa/internal/rules"
	"noxa/internal/server"
	"noxa/internal/state"
	noxav1 "noxa/v1"
)

type nativeMetadataGRPCBackend struct {
	*nativeRoleGRPCBackend
	query.RoleMetadataBackend
	query.RoleBanInspectionBackend
	query.RoleAuditBackend
	query.RoleComplaintBackend
	query.RoleRulesBackend
	query.RoleConfigBackend
	query.RoleFilterBackend
	query.RoleTextBackend
}

func TestRoleGRPCChannelReadUsesCurrentNativeProjection(t *testing.T) {
	db := grpcRoleTestStore(t)
	a := auth.New(db, zap.NewNop())
	const password = "grpc-read-password"
	create := func(name string) auth.IntegrationPrincipal {
		t.Helper()
		uid, err := a.RegisterUser(t.Context(), name, password)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=TRUE WHERE unique_id=$1", uid); err != nil {
			t.Fatal(err)
		}
		p, err := a.AuthenticateIntegration(t.Context(), uid, password, "127.0.0.1")
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	owner, member := create("read-owner"), create("read-member")
	if _, err := db.DB().ExecContext(t.Context(), "INSERT INTO channels (id,name,channel_type,parent_id) VALUES (1,'Hidden parent',2,NULL),(2,'Visible child',2,1),(3,'Hidden other',2,NULL)"); err != nil {
		t.Fatal(err)
	}
	policy, err := db.PrepareRolePolicy(t.Context(), owner.UserID())
	if err != nil {
		t.Fatal(err)
	}
	authority, err := authorization.NewAuthority(t.Context(), db, func(context.Context, *authorization.RoleEvaluator, *authorization.RoleEvaluator) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	change := authorization.RoleChange{Kind: authorization.ChannelAccessSet, ExpectedRevision: policy.Revision,
		Channel: authorization.ChannelPolicy{ChannelID: 2, ParentID: 1, Overrides: []authorization.RoleOverride{{RoleID: policy.EveryoneID, Capability: authorization.ViewChannel, Effect: authorization.Allow}}}}
	policy, err = authority.ChangeRolePolicy(t.Context(), owner.UserID(), change)
	if err != nil {
		t.Fatal(err)
	}
	sm := state.New(zap.NewNop())
	sm.AddChannel(&state.Channel{ChannelID: 1, Name: "Hidden parent", ChannelType: 2})
	sm.AddChannel(&state.Channel{ChannelID: 2, ParentID: 1, Name: "Visible child", Topic: "Visible topic", ChannelType: 2, MaxClients: 25, OpusBitrate: 64000})
	sm.AddChannel(&state.Channel{ChannelID: 3, Name: "Hidden other", ChannelType: 2})
	for _, client := range []struct {
		id      string
		channel int64
		status  string
	}{{"public", 2, "online"}, {"invisible", 2, "invisible"}, {"hidden", 1, "online"}} {
		sm.AddClient(&state.Client{ClientID: client.id, UniqueID: client.id, Nickname: client.id, Status: client.status})
		if err := sm.MoveClient(client.id, client.channel); err != nil {
			t.Fatal(err)
		}
	}
	sm.AddClient(&state.Client{ClientID: "unassigned", UniqueID: "unassigned", Nickname: "Unassigned", Status: "online"})
	ruleService := rules.New(db, db.DB())
	native := server.New(&config.Config{ServerName: "Integration server", MaxClients: 25, DefaultOpusBitrate: 64000, DefaultOpusFEC: true}, zap.NewNop(), &server.Deps{Auth: a, Authority: authority, Roles: db, State: sm, BanAdmin: db, Groups: db, Complaints: db, Rules: ruleService, Chat: db})
	b := &nativeMetadataGRPCBackend{nativeRoleGRPCBackend: &nativeRoleGRPCBackend{RoleIntegrationBackend: native, RoleManagementBackend: native}, RoleMetadataBackend: native, RoleBanInspectionBackend: native, RoleAuditBackend: native, RoleComplaintBackend: native, RoleRulesBackend: native, RoleConfigBackend: native}
	b.RoleFilterBackend = native
	b.RoleTextBackend = native
	client := noxav1.NewControlClient(dialGRPC(t, startGRPC(t, b, nil)))
	ctx := func() context.Context { return roleAuthCtx(t, member.UniqueID(), password) }
	response, err := client.ListChannels(ctx(), &noxav1.ListChannelsRequest{})
	if err != nil || len(response.GetChannels()) != 1 {
		t.Fatalf("filtered response: %v %v", response, err)
	}
	got := response.Channels[0]
	if got.Id != "2" || got.ParentId != "" || got.Name != "Visible child" || got.CurrentClients != 1 || got.MaxClients != 25 || !got.Permanent {
		t.Fatalf("hidden parent/presence exposed or fields lost: %v", got)
	}
	visible, err := client.ListClients(ctx(), &noxav1.ListClientsRequest{})
	if err != nil || len(visible.GetClients()) != 2 || visible.Clients[0].ClientId != "unassigned" || visible.Clients[1].ClientId != "public" || visible.Clients[1].ChannelId != 2 {
		t.Fatalf("discovery differed from native visible/unassigned sessions: %v %v", visible, err)
	}
	detail, err := client.GetChannelInfo(ctx(), &noxav1.GetChannelInfoRequest{ChannelId: 2})
	if err != nil || detail.GetParentId() != 0 || detail.GetCurrentClients() != 1 || detail.GetTopic() != "Visible topic" || detail.GetOpusBitrate() != 64000 {
		t.Fatalf("channel detail bypassed projection: %v %v", detail, err)
	}
	for _, id := range []int64{1, 3, 9999} {
		if _, err := client.GetChannelInfo(ctx(), &noxav1.GetChannelInfoRequest{ChannelId: id}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("hidden/missing channel %d: %v", id, err)
		}
	}
	info, err := client.GetServerInfo(ctx(), &noxav1.GetServerInfoRequest{})
	if err != nil || info.GetName() != "Integration server" || info.GetClientsOnline() != 2 || info.GetChannelsOnline() != 1 || info.GetMaxClients() != 25 {
		t.Fatalf("filtered metadata: %v %v", info, err)
	}
	if _, err := client.GetServerConfig(ctx(), &noxav1.GetServerConfigRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member read configuration: %v", err)
	}
	if _, err := client.ListBans(ctx(), &noxav1.ListBansRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member read bans: %v", err)
	}
	if _, err := client.GetClientInfo(ctx(), &noxav1.GetClientInfoRequest{ClientId: "missing"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("missing native session: %v", err)
	}
	ownerCtx := func() context.Context { return roleAuthCtx(t, owner.UniqueID(), password) }
	if _, err := client.ListAuditLog(ctx(), &noxav1.ListAuditLogRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member read audit: %v", err)
	}
	audit, err := client.ListAuditLog(ownerCtx(), &noxav1.ListAuditLogRequest{Limit: 1})
	if err != nil || len(audit.GetEntries()) != 1 || audit.Entries[0].Actor != owner.UniqueID() || audit.Entries[0].Restricted || !audit.Entries[0].Structured || len(audit.Capabilities) == 0 {
		t.Fatalf("native scoped audit through gRPC: %v %v", audit, err)
	}
	visible, err = client.ListClients(ownerCtx(), &noxav1.ListClientsRequest{})
	if err != nil || len(visible.GetClients()) != 4 {
		t.Fatalf("owner discovery: %v %v", visible, err)
	}
	if err := db.AddComplaint(t.Context(), member.UniqueID(), owner.UniqueID(), "Native complaint"); err != nil {
		t.Fatal(err)
	}
	clearComplaint := &noxav1.ClearComplaintsRequest{TargetUniqueId: owner.UniqueID(), FromUniqueId: member.UniqueID()}
	if _, err := client.ListComplaints(ctx(), &noxav1.ListComplaintsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member read complaints: %v", err)
	}
	if _, err := client.ClearComplaints(ctx(), clearComplaint); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member cleared complaints: %v", err)
	}
	complaints, err := client.ListComplaints(ownerCtx(), &noxav1.ListComplaintsRequest{Limit: 1})
	if err != nil || len(complaints.GetEntries()) != 1 || complaints.Entries[0].TargetNickname != "read-owner" || complaints.Entries[0].FromNickname != "read-member" || complaints.Entries[0].Reason != "Native complaint" {
		t.Fatalf("native complaints: %v %v", complaints, err)
	}
	for _, deleted := range []int64{1, 0} {
		result, err := client.ClearComplaints(ownerCtx(), clearComplaint)
		if err != nil || result.GetDeleted() != deleted {
			t.Fatalf("native clear: %v %v", result, err)
		}
	}
	complaints, err = client.ListComplaints(ownerCtx(), &noxav1.ListComplaintsRequest{})
	if err != nil || len(complaints.GetEntries()) != 0 {
		t.Fatalf("native clear did not persist: %v %v", complaints, err)
	}
	var complaintActor string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT actor_unique_id FROM audit_log WHERE action='complaint_clear' ORDER BY id DESC LIMIT 1").Scan(&complaintActor); err != nil || complaintActor != owner.UniqueID() {
		t.Fatalf("canonical complaint actor: %q %v", complaintActor, err)
	}
	settings, err := client.GetServerConfig(ownerCtx(), &noxav1.GetServerConfigRequest{})
	if err != nil || settings.GetMaxClients() != 25 || settings.GetOpusBitrate() != 64000 || !settings.GetOpusFec() {
		t.Fatalf("owner configuration: %v %v", settings, err)
	}
	saveConfig := &noxav1.SetServerConfigRequest{MaxClients: 50, ClientTimeoutSeconds: 180, OpusBitrate: 128000, OpusStereo: true}
	if _, err := client.SetServerConfig(ctx(), saveConfig); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member saved config: %v", err)
	}
	ack, err := client.SetServerConfig(ownerCtx(), saveConfig)
	if err != nil || ack.GetMaxClients() != 50 || ack.GetClientTimeoutSeconds() != 180 || ack.GetOpusBitrate() != 128000 || ack.GetOpusFec() || ack.GetOpusDtx() || !ack.GetOpusStereo() {
		t.Fatalf("native save ack: %v %v", ack, err)
	}
	settings, err = client.GetServerConfig(ownerCtx(), &noxav1.GetServerConfigRequest{})
	if err != nil || settings.GetMaxClients() != 50 || settings.GetClientTimeoutSeconds() != 180 || settings.GetOpusBitrate() != 128000 || settings.GetOpusFec() || settings.GetOpusDtx() || !settings.GetOpusStereo() {
		t.Fatalf("native runtime settings: %v %v", settings, err)
	}
	var reloaded config.Config
	if err := server.LoadPersistedServerConfig(t.Context(), &reloaded, db); err != nil || reloaded.MaxClients != 50 || reloaded.ClientTimeoutSeconds != 180 || reloaded.DefaultOpusBitrate != 128000 || !reloaded.DefaultOpusStereo {
		t.Fatalf("native persisted settings: %+v %v", reloaded, err)
	}
	var configActor string
	if err := db.DB().QueryRowContext(t.Context(), "SELECT actor_unique_id FROM audit_log WHERE action='server_config_set' ORDER BY id DESC LIMIT 1").Scan(&configActor); err != nil || configActor != owner.UniqueID() {
		t.Fatalf("canonical config audit: %q %v", configActor, err)
	}
	if err := ruleService.Set(t.Context(), "Native rules"); err != nil {
		t.Fatal(err)
	}
	if err := ruleService.Accept(t.Context(), member.UserID(), rules.Hash("Native rules")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetServerRules(ctx(), &noxav1.GetServerRulesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member inspected rules: %v", err)
	}
	ruleInfo, err := client.GetServerRules(ownerCtx(), &noxav1.GetServerRulesRequest{})
	if err != nil || ruleInfo.GetText() != "Native rules" || ruleInfo.GetHash() != rules.Hash("Native rules") || ruleInfo.GetAcceptedClients() != 1 {
		t.Fatalf("native rule inspection: %v %v", ruleInfo, err)
	}
	var ids []int64
	newRules := "Changed native rules"
	textRequest := &noxav1.SetServerTextRequest{Key: "server_rules", Value: &newRules}
	if _, err := client.SetServerText(ctx(), textRequest); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member saved text: %v", err)
	}
	textAck, err := client.SetServerText(ownerCtx(), textRequest)
	if err != nil || textAck.GetKey() != "server_rules" || textAck.GetContentHash() != rules.Hash(newRules) {
		t.Fatalf("native text save: %v %v", textAck, err)
	}
	ruleInfo, err = client.GetServerRules(ownerCtx(), &noxav1.GetServerRulesRequest{})
	if err != nil || ruleInfo.GetText() != newRules || ruleInfo.GetHash() != textAck.ContentHash || ruleInfo.GetAcceptedClients() != 0 {
		t.Fatalf("changed rule acceptance: %v %v", ruleInfo, err)
	}
	filterWords := " Bad, , Worse "
	filterPatch := &noxav1.SetChatFiltersRequest{WordFilter: &filterWords}
	if _, err := client.GetChatFilters(ctx(), &noxav1.GetChatFiltersRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member read filters: %v", err)
	}
	if _, err := client.SetChatFilters(ctx(), filterPatch); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("ordinary member saved filters: %v", err)
	}
	filters, err := client.GetChatFilters(ownerCtx(), &noxav1.GetChatFiltersRequest{})
	if err != nil || !filters.GetFromConfig() {
		t.Fatalf("native default filters: %v %v", filters, err)
	}
	filterAck, err := client.SetChatFilters(ownerCtx(), filterPatch)
	if err != nil || filterAck.GetWordFilter() != "Bad,Worse" || filterAck.GetFromConfig() {
		t.Fatalf("native normalized filter save: %v %v", filterAck, err)
	}
	filters, err = client.GetChatFilters(ownerCtx(), &noxav1.GetChatFiltersRequest{})
	if err != nil || filters.GetWordFilter() != "Bad,Worse" || filters.GetFromConfig() {
		t.Fatalf("persisted filters: %v %v", filters, err)
	}
	for _, value := range []string{"older-ban", "newer-ban"} {
		var id int64
		if err := db.DB().QueryRowContext(t.Context(), "INSERT INTO bans (ban_type,value,reason,banned_by,expires_at) VALUES (1,$1,'Reason',$2,to_timestamp(1700000600)) RETURNING id", value, owner.UserID()).Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	page, err := client.ListBans(ownerCtx(), &noxav1.ListBansRequest{Limit: 1})
	if err != nil || len(page.GetBans()) != 1 || page.Bans[0].Id != ids[1] || page.GetNextBeforeId() != ids[1] || page.Bans[0].BannedBy != owner.UniqueID() || page.Bans[0].ExpiresAt != 1700000600 {
		t.Fatalf("ban recovery page and Unix-second expiry: %v %v", page, err)
	}
	page, err = client.ListBans(ownerCtx(), &noxav1.ListBansRequest{BeforeId: page.NextBeforeId, Limit: 1})
	if err != nil || len(page.GetBans()) != 1 || page.Bans[0].Id != ids[0] || page.GetNextBeforeId() != 0 {
		t.Fatalf("ban recovery cursor: %v %v", page, err)
	}
	for _, root := range []string{"1", "9999"} {
		response, err := client.ListChannels(ctx(), &noxav1.ListChannelsRequest{RootChannelId: root})
		if err != nil || len(response.GetChannels()) != 0 {
			t.Fatalf("hidden/missing root %s: %v %v", root, response, err)
		}
	}
	response, err = client.ListChannels(roleAuthCtx(t, owner.UniqueID(), password), &noxav1.ListChannelsRequest{})
	if err != nil || len(response.GetChannels()) != 3 {
		t.Fatalf("owner read: %v %v", response, err)
	}
	change.ExpectedRevision = policy.Revision
	change.Channel.Overrides[0].Effect = authorization.Deny
	if _, err := authority.ChangeRolePolicy(t.Context(), owner.UserID(), change); err != nil {
		t.Fatal(err)
	}
	response, err = client.ListChannels(ctx(), &noxav1.ListChannelsRequest{})
	if err != nil || len(response.GetChannels()) != 0 {
		t.Fatalf("same connection retained revoked channels: %v %v", response, err)
	}
	visible, err = client.ListClients(ctx(), &noxav1.ListClientsRequest{})
	if err != nil || len(visible.GetClients()) != 1 || visible.Clients[0].ClientId != "unassigned" {
		t.Fatalf("revoked discovery: %v %v", visible, err)
	}
	if _, err := client.GetChannelInfo(ctx(), &noxav1.GetChannelInfoRequest{ChannelId: 2}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("revoked channel detail: %v", err)
	}
	info, err = client.GetServerInfo(ctx(), &noxav1.GetServerInfoRequest{})
	if err != nil || info.GetClientsOnline() != 1 || info.GetChannelsOnline() != 0 {
		t.Fatalf("same connection retained hidden counts: %v %v", info, err)
	}
	if _, err := db.DB().ExecContext(t.Context(), "UPDATE users SET integration_enabled=FALSE WHERE id=$1", member.UserID()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListChannels(ctx(), &noxav1.ListChannelsRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("disabled identity read channels: %v", err)
	}
	for name, read := range map[string]func() error{
		"text save":        func() error { _, err := client.SetServerText(ctx(), textRequest); return err },
		"filters":          func() error { _, err := client.GetChatFilters(ctx(), &noxav1.GetChatFiltersRequest{}); return err },
		"filter save":      func() error { _, err := client.SetChatFilters(ctx(), filterPatch); return err },
		"config save":      func() error { _, err := client.SetServerConfig(ctx(), saveConfig); return err },
		"rules":            func() error { _, err := client.GetServerRules(ctx(), &noxav1.GetServerRulesRequest{}); return err },
		"complaints":       func() error { _, err := client.ListComplaints(ctx(), &noxav1.ListComplaintsRequest{}); return err },
		"clear complaints": func() error { _, err := client.ClearComplaints(ctx(), clearComplaint); return err },
		"audit":            func() error { _, err := client.ListAuditLog(ctx(), &noxav1.ListAuditLogRequest{}); return err },
		"clients":          func() error { _, err := client.ListClients(ctx(), &noxav1.ListClientsRequest{}); return err },
		"channel detail": func() error {
			_, err := client.GetChannelInfo(ctx(), &noxav1.GetChannelInfoRequest{ChannelId: 2})
			return err
		},
		"server info": func() error { _, err := client.GetServerInfo(ctx(), &noxav1.GetServerInfoRequest{}); return err },
		"client info": func() error {
			_, err := client.GetClientInfo(ctx(), &noxav1.GetClientInfoRequest{ClientId: "missing"})
			return err
		},
		"config": func() error { _, err := client.GetServerConfig(ctx(), &noxav1.GetServerConfigRequest{}); return err },
		"bans":   func() error { _, err := client.ListBans(ctx(), &noxav1.ListBansRequest{}); return err },
	} {
		if err := read(); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("disabled identity read %s: %v", name, err)
		}
	}
}
