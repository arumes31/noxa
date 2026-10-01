package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"noxa/internal/store"
)

func TestRunInspectsExactOwnerAndProjectsJSON(t *testing.T) {
	var output, diagnostics bytes.Buffer
	called := false
	inspect := func(_ context.Context, dsn, uid string, timeout time.Duration) (store.RoleSetupReport, error) {
		called = true
		if dsn != "secret-dsn" || uid != "exact-owner" || timeout != 12*time.Second {
			t.Fatal("incorrect inspection inputs")
		}
		return store.RoleSetupReport{State: "prepared_inactive", Owner: store.RoleSetupOwner{
			RoleSetupIdentity: store.RoleSetupIdentity{UniqueID: uid}}}, nil
	}
	code := run(t.Context(), []string{"-owner-uid", "exact-owner", "-query-timeout", "12s"}, func(string) string { return "secret-dsn" }, &output, &diagnostics, inspect, nil)
	if code != 0 || !called || !strings.Contains(output.String(), `"state": "prepared_inactive"`) || diagnostics.Len() != 0 || strings.Contains(output.String(), "secret-dsn") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &output, &diagnostics)
	}
}

func TestRunRejectsInvalidRequestsBeforeDatabaseAccess(t *testing.T) {
	for _, args := range [][]string{nil, {"-owner-uid", " "}, {"-owner-uid", "u", "-query-timeout", "0s"}, {"-owner-uid", "u", "-prepare"}, {"-owner-uid", "u", "unexpected"}} {
		var out, diagnostics bytes.Buffer
		inspect := func(context.Context, string, string, time.Duration) (store.RoleSetupReport, error) {
			t.Fatal("invalid request reached database")
			return store.RoleSetupReport{}, nil
		}
		if code := run(t.Context(), args, func(string) string { return "dsn" }, &out, &diagnostics, inspect, nil); code != 1 || out.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%s", args, code, &out)
		}
	}
}

func TestRunDoesNotPrintDriverSecrets(t *testing.T) {
	for _, cause := range []error{errors.New("driver includes secret-password and secret-dsn"), store.ErrRoleSetupOwnerNotFound,
		errors.Join(store.ErrRoleSetupSchemaUnavailable, errors.New("secret-key")), context.DeadlineExceeded} {
		var out, diagnostics bytes.Buffer
		inspect := func(context.Context, string, string, time.Duration) (store.RoleSetupReport, error) {
			return store.RoleSetupReport{}, cause
		}
		code := run(t.Context(), []string{"-owner-uid", "owner"}, func(string) string { return "secret-dsn" }, &out, &diagnostics, inspect, nil)
		if code != 1 || out.Len() != 0 || diagnostics.Len() == 0 || strings.Contains(diagnostics.String(), "secret-") {
			t.Fatalf("unsafe error projection: code=%d stdout=%s stderr=%s", code, &out, &diagnostics)
		}
	}
}

func TestRunRequiresExplicitDatabase(t *testing.T) {
	var output, diagnostics bytes.Buffer
	if code := run(t.Context(), []string{"-owner-uid", "owner"}, func(string) string { return "" }, &output, &diagnostics, nil, nil); code != 1 {
		t.Fatal("missing database accepted")
	}
}

type failedOutput struct{}

func (failedOutput) Write([]byte) (int, error) { return 0, errors.New("output unavailable") }

func TestRunReportsOutputFailure(t *testing.T) {
	for _, args := range [][]string{{"-help"}, {"-owner-uid", "owner"}} {
		var diagnostics bytes.Buffer
		inspect := func(context.Context, string, string, time.Duration) (store.RoleSetupReport, error) {
			return store.RoleSetupReport{State: "unprepared"}, nil
		}
		if code := run(t.Context(), args, func(string) string { return "dsn" }, failedOutput{}, &diagnostics, inspect, nil); code != 1 {
			t.Fatalf("output failure returned success for %v", args)
		}
	}
}

func TestRunActivatesOnlyWithExactConfirmationAndDoesNotExposeSecrets(t *testing.T) {
	for _, args := range [][]string{
		{"-owner-uid", "owner", "-activate"},
		{"-owner-uid", "owner", "-activate", "-confirm", "yes"},
		{"-owner-uid", "owner", "-confirm", "ACTIVATE-ROLES-V1"},
	} {
		var out, diagnostics bytes.Buffer
		activate := func(context.Context, string, string, string, string, time.Duration) (store.RoleSetupReport, error) {
			t.Fatal("invalid activation reached database")
			return store.RoleSetupReport{}, nil
		}
		if code := run(t.Context(), args, func(string) string { return "secret-dsn" }, &out, &diagnostics, nil, activate); code != 1 || out.Len() != 0 {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, &out, &diagnostics)
		}
	}
	var out, diagnostics bytes.Buffer
	activate := func(_ context.Context, dsn, uid, keyFile, envKey string, timeout time.Duration) (store.RoleSetupReport, error) {
		if dsn != "secret-dsn" || uid != "owner" || keyFile != "keys/ring" || envKey != "secret-kek" || timeout != 15*time.Second {
			t.Fatalf("activation inputs: %q %q %q %q %s", dsn, uid, keyFile, envKey, timeout)
		}
		return store.RoleSetupReport{State: "active", Active: true}, nil
	}
	env := func(name string) string {
		if name == "NOXA_CHAT_MASTER_KEY" {
			return "secret-kek"
		}
		return "secret-dsn"
	}
	code := run(t.Context(), []string{"-owner-uid", "owner", "-activate", "-confirm", "ACTIVATE-ROLES-V1",
		"-chat-master-key-file", "keys/ring", "-query-timeout", "15s"}, env, &out, &diagnostics, nil, activate)
	if code != 0 || diagnostics.Len() != 0 || !strings.Contains(out.String(), `"state": "active"`) || strings.Contains(out.String(), "secret-") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &out, &diagnostics)
	}
}
