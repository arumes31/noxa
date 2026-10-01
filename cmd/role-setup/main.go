// role-setup inspects or explicitly activates roles-v1 for one exact owner.
package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.uber.org/zap"

	"noxa/internal/chatcrypto"
	"noxa/internal/store"
)

type inspectFunc func(context.Context, string, string, time.Duration) (store.RoleSetupReport, error)
type activateFunc func(context.Context, string, string, string, string, time.Duration) (store.RoleSetupReport, error)

func main() {
	os.Exit(runMain())
}

func runMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr, inspectDatabase, activateDatabase)
}

func run(ctx context.Context, args []string, env func(string) string, out, diagnostics io.Writer, inspect inspectFunc, activate activateFunc) int {
	flags := flag.NewFlagSet("role-setup", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	ownerUID := flags.String("owner-uid", "", "exact existing owner unique ID (required; never a nickname)")
	timeout := flags.Duration("query-timeout", 10*time.Second, "maximum inspection query duration after connecting")
	activateRoles := flags.Bool("activate", false, "prepare and activate roles-v1 while the server is stopped")
	confirmation := flags.String("confirm", "", "required activation phrase")
	keyFile := flags.String("chat-master-key-file", "./data/keys/chat_master.key", "chat KEK ring used to rotate scope keys")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err := fmt.Fprintln(out, "Usage: role-setup -owner-uid <unique-id> [-query-timeout 10s]\n"+
				"       role-setup -owner-uid <unique-id> -activate -confirm ACTIVATE-ROLES-V1 [-chat-master-key-file path]\n"+
				"Database: NOXA_DATABASE_URL. Inspection is read-only. Activation requires the server to be stopped,\n"+
				"rotates current scope keys and cannot be repeated. The schema migration removes legacy authority data.")
			if err != nil {
				return 1
			}
			return 0
		}
		_, _ = fmt.Fprintln(diagnostics, "role-setup: invalid arguments; use -help")
		return 1
	}
	if flags.NArg() != 0 || strings.TrimSpace(*ownerUID) == "" || *timeout <= 0 ||
		(*activateRoles && (*confirmation != "ACTIVATE-ROLES-V1" || strings.TrimSpace(*keyFile) == "")) ||
		(!*activateRoles && *confirmation != "") {
		_, _ = fmt.Fprintln(diagnostics, "role-setup: an exact -owner-uid and positive -query-timeout are required; no positional arguments")
		return 1
	}
	dsn := env("NOXA_DATABASE_URL")
	if strings.TrimSpace(dsn) == "" {
		_, _ = fmt.Fprintln(diagnostics, "role-setup: set NOXA_DATABASE_URL to the database to inspect")
		return 1
	}
	var report store.RoleSetupReport
	var err error
	if *activateRoles {
		report, err = activate(ctx, dsn, *ownerUID, *keyFile, env("NOXA_CHAT_MASTER_KEY"), *timeout)
	} else {
		report, err = inspect(ctx, dsn, *ownerUID, *timeout)
	}
	if err != nil {
		// Driver errors can contain DSNs, SQL values or credential material. Keep
		// diagnostics deliberately narrow; the report never includes raw errors.
		message := "database inspection failed; verify connectivity, schema and read permissions"
		switch {
		case errors.Is(err, store.ErrRoleSetupOwnerNotFound):
			message = "selected owner unique ID was not found; nickname fallback is not supported"
		case errors.Is(err, store.ErrRoleSetupSchemaUnavailable):
			message = "fresh-version schema is unavailable; initialize the empty database first"
		case errors.Is(err, store.ErrFreshDatabaseRequired):
			message = "roles-v1 requires a fresh PostgreSQL database"
		case errors.Is(err, store.ErrRoleSetupAlreadyActive):
			message = "roles-v1 is already active; repeated activation is refused"
		case errors.Is(err, store.ErrRoleSetupNotReady):
			message = "prepared policy is not a clean closed baseline; inspect and repair it before activation"
		case errors.Is(err, store.ErrRoleProcessBusy):
			message = "another roles-v1 server or operator command is using this database; stop it before activation"
		case errors.Is(err, context.DeadlineExceeded):
			message = "inspection query deadline exceeded"
		case errors.Is(err, context.Canceled):
			message = "inspection canceled"
		}
		_, _ = fmt.Fprintln(diagnostics, "role-setup: "+message)
		return 1
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(report); err != nil {
		_, _ = fmt.Fprintln(diagnostics, "role-setup: writing report failed")
		return 1
	}
	return 0
}

func inspectDatabase(ctx context.Context, dsn, ownerUID string, timeout time.Duration) (_ store.RoleSetupReport, retErr error) {
	// New bounds its initial connection check to five seconds. Query timeout is
	// separate and starts after opening; there is intentionally no Migrate call.
	db, err := store.New(dsn, zap.NewNop(), 1, 1, time.Minute)
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	queryCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := db.CheckFreshInstall(queryCtx); err != nil {
		return store.RoleSetupReport{}, err
	}
	return db.InspectRoleSetup(queryCtx, ownerUID)
}

func activateDatabase(ctx context.Context, dsn, ownerUID, keyFile, envKey string, timeout time.Duration) (_ store.RoleSetupReport, retErr error) {
	lease, err := store.AcquireRoleProcessLease(ctx, dsn)
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	defer func() { retErr = errors.Join(retErr, lease.Close()) }()
	db, err := store.New(dsn, zap.NewNop(), 1, 1, time.Minute)
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	defer func() { retErr = errors.Join(retErr, db.Close()) }()
	activationCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := lease.Check(activationCtx); err != nil {
		return store.RoleSetupReport{}, err
	}
	if err := db.CheckFreshInstall(activationCtx); err != nil {
		return store.RoleSetupReport{}, err
	}
	before, err := db.InspectRoleSetup(activationCtx, ownerUID)
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	if before.Active {
		return store.RoleSetupReport{}, store.ErrRoleSetupAlreadyActive
	}
	keyCount, err := db.CountScopeKeys(activationCtx)
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	ring, err := chatcrypto.LoadKEKRing(keyFile, envKey, keyCount == 0)
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	if !before.Configured {
		if before.State != "unprepared" {
			return store.RoleSetupReport{}, store.ErrRoleSetupNotReady
		}
		if _, err := db.PrepareRolePolicy(activationCtx, before.Owner.UserID); err != nil {
			return store.RoleSetupReport{}, err
		}
	}
	_, err = db.ActivatePreparedRolePolicy(activationCtx, ownerUID, func() (uint16, []byte, error) {
		var key [32]byte
		if _, err := rand.Read(key[:]); err != nil {
			return 0, nil, err
		}
		return ring.Wrap(key)
	})
	if err != nil {
		return store.RoleSetupReport{}, err
	}
	if err := lease.Check(activationCtx); err != nil {
		return store.RoleSetupReport{}, err
	}
	return db.InspectRoleSetup(activationCtx, ownerUID)
}
