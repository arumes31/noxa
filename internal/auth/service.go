package auth

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"filippo.io/edwards25519"
	"github.com/lib/pq"
	"go.uber.org/zap"

	"noxa/internal/store"
)

// ErrUserExists is returned by RegisterUser when a user with the same unique ID
// or nickname already exists in the database.
var ErrUserExists = errors.New("user already exists")

// ErrPasswordTooShort is returned for new registrations below the minimum
// password length. Existing short password hashes remain login-compatible.
var ErrPasswordTooShort = errors.New("password must be at least 12 bytes")

const minRegistrationPasswordBytes = 12

// ErrUserNotFound is returned by the Authenticate* methods when no user with the
// given unique ID exists in the database.
var ErrUserNotFound = errors.New("user not found")

// ErrPublicKeyInUse is returned by BindPublicKey when the identity key is
// already bound to, or derives the unique ID of, a different account.
var ErrPublicKeyInUse = errors.New("public key belongs to another account")

// AuthService provides user registration and authentication (password and
// challenge-response) backed by the PostgreSQL store. Passwords are verified
// on every call: there is deliberately NO positive-result cache — a cache
// keyed only by unique ID would let any password through after one
// successful login (see the BUG-1 regression).
type AuthService struct {
	store            *store.Store
	logger           *zap.Logger
	verifyPassword   func(password, encodedHash string) error
	verifyChallenge  func(string, []byte, []byte) error
	lookupIdentifier func(context.Context, string) (passwordCredential, error)
	loginLimiter     *LoginFailureLimiter // set before serving; nil disables alias budgets
}

// SetLoginLimiter installs the process-wide login limiter so a failed password
// also counts against the account budget of the account's other identifier.
func (a *AuthService) SetLoginLimiter(limiter *LoginFailureLimiter) {
	a.loginLimiter = limiter
}

type passwordCredential struct {
	user User
	hash string
}

// New constructs an AuthService backed by the given store and logger.
func New(s *store.Store, logger *zap.Logger) *AuthService {
	return &AuthService{store: s, logger: logger, verifyPassword: VerifyPassword}
}

// RecordLastIP encrypts the address at column level before persistence.
func (a *AuthService) RecordLastIP(ctx context.Context, userID int64, ip string) error {
	return a.store.SetUserPII(ctx, userID, nil, &ip)
}

// RegisterUser generates a new Ed25519 identity key pair, derives the unique
// ID, hashes the password with Argon2id, and inserts a new row into the users
// table. It returns the unique ID of the newly created user. If a user with the
// same unique ID or nickname already exists, ErrUserExists is returned.
func (a *AuthService) RegisterUser(ctx context.Context, nickname, password string) (string, error) {
	if err := validateRegistrationPassword(password); err != nil {
		return "", err
	}

	pubPEM, _, err := GenerateIdentityKeyPair()
	if err != nil {
		return "", fmt.Errorf("generating identity key pair: %w", err)
	}

	uniqueID, err := UniqueIDFromPublicKey(pubPEM)
	if err != nil {
		return "", fmt.Errorf("deriving unique id: %w", err)
	}

	hash, err := HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}

	_, err = a.store.RegisterUserWithDefaultRole(ctx, uniqueID, nickname, hash, pubPEM)
	if err != nil {
		if isUniqueViolation(err) {
			return "", ErrUserExists
		}
		return "", fmt.Errorf("inserting user: %w", err)
	}

	if a.logger != nil {
		a.logger.Info("user registered",
			zap.String("unique_id", uniqueID),
			zap.String("nickname", nickname),
		)
	}
	return uniqueID, nil
}

func validateRegistrationPassword(password string) error {
	if len(password) < minRegistrationPasswordBytes {
		return ErrPasswordTooShort
	}
	if len(password) > MaxPasswordBytes {
		return ErrPasswordTooLong
	}
	return nil
}

// AuthenticateIdentityChallenge verifies the Ed25519 signature over the
// challenge with the user's proven public key and returns that exact key,
// avoiding a second lookup that could race an account key replacement. A
// signature mismatch returns false with a nil error. Every call verifies.
func (a *AuthService) AuthenticateIdentityChallenge(ctx context.Context, uniqueID string, challenge, signature []byte) (string, bool, error) {
	var pubPEM string
	// An unproven binding never authenticates; it verifies like an unbound key.
	const q = `SELECT CASE WHEN public_key_proven THEN COALESCE(public_key, '') ELSE '' END FROM users WHERE unique_id = $1`
	err := a.store.DB().QueryRowContext(ctx, q, uniqueID).Scan(&pubPEM)
	missing := errors.Is(err, sql.ErrNoRows)
	if err != nil && !missing {
		return "", false, fmt.Errorf("querying user public key: %w", err)
	}
	// Missing, unbound and malformed stored keys must perform the same signature
	// verification work as a wrong signature for a usable account key. A dummy
	// key is never an identity credential, even if its signature verifies.
	publicKey, keyErr := LoadPublicKey(pubPEM)
	if keyErr == nil {
		// Length-valid encodings can still fail before Ed25519's scalar work.
		_, keyErr = new(edwards25519.Point).SetBytes(publicKey)
	}
	usable := !missing && keyErr == nil
	verificationKey := pubPEM
	if !usable {
		verificationKey = dummyChallengePublicKey
	}
	verify := a.verifyChallenge
	if verify == nil {
		verify = VerifyLoginChallenge
	}
	verificationErr := verify(verificationKey, challenge, signature)
	if missing {
		return "", false, ErrUserNotFound
	}
	if !usable || verificationErr != nil {
		return "", false, nil
	}
	return pubPEM, true, nil
}

// Fixed Ed25519 test public key (RFC 8032, test 1). Its known private key is
// harmless: verification with this key is only performed on a denied path.
const dummyChallengePublicKey = `-----BEGIN PUBLIC KEY-----
11qYAYKxCrfVS/7TyWQHOg7hcvPapiMlrwIaaPcHURo=
-----END PUBLIC KEY-----
`

// User holds the identity fields the server needs after a successful
// authentication.
type User struct {
	ID          int64
	UniqueID    string
	Nickname    string
	DisplayName string // display label for an enrolled identity; never a login identifier
	IsBot       bool   // identity metadata; never grants authority or bypasses limits
}

// LookupUser returns the user row for the given unique ID. It returns
// ErrUserNotFound when no such user exists.
func (a *AuthService) LookupUser(ctx context.Context, uniqueID string) (*User, error) {
	var u User
	const q = `SELECT id, unique_id, COALESCE(nickname, ''), is_bot, COALESCE(identity_display_name, '') FROM users WHERE unique_id = $1`
	err := a.store.DB().QueryRowContext(ctx, q, uniqueID).
		Scan(&u.ID, &u.UniqueID, &u.Nickname, &u.IsBot, &u.DisplayName)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("querying user: %w", err)
	}
	return &u, nil
}

// Ban describes an active ban row from the bans table. ExpiresAt is the zero
// time for permanent bans.
type Ban struct {
	ID        int64
	Type      int // 0=IP, 1=unique_id, 2=nickname
	Value     string
	Reason    string
	ExpiresAt time.Time
}

// LookupActiveBan returns the first active, server-wide ban matching the
// given unique ID (ban_type 1) or IP address (ban_type 0). Expired bans
// (expires_at in the past) and channel-scoped bans are ignored; a NULL
// expires_at means the ban is permanent. It returns (nil, nil) when no ban
// matches.
func (a *AuthService) LookupActiveBan(ctx context.Context, uniqueID, ip string) (*Ban, error) {
	const q = `SELECT id, ban_type, value, COALESCE(reason, ''), expires_at
	          FROM bans
	          WHERE channel_id IS NULL
	            AND ((ban_type = 1 AND value = $1) OR ban_type = 0)
	            AND (expires_at IS NULL OR expires_at > NOW())
	          ORDER BY id`
	rows, err := a.store.DB().QueryContext(ctx, q, uniqueID)
	if err != nil {
		return nil, fmt.Errorf("querying active ban: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var b Ban
		var expires sql.NullTime
		if err := rows.Scan(&b.ID, &b.Type, &b.Value, &b.Reason, &expires); err != nil {
			return nil, err
		}
		if b.Type == 0 && !sameBanIP(b.Value, ip) {
			continue
		}
		if expires.Valid {
			b.ExpiresAt = expires.Time
		}
		return &b, nil
	}
	return nil, rows.Err()
}

// AuthenticateIdentifier authenticates either a unique ID or nickname with
// unique-ID precedence. It performs exactly one real or dummy Argon2
// verification for each external password attempt.
func (a *AuthService) AuthenticateIdentifier(ctx context.Context, identifier, password string) (*User, error) {
	lookup := a.lookupIdentifier
	if lookup == nil {
		lookup = a.lookupCredentialByIdentifier
	}
	credential, err := lookup(ctx, identifier)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			a.verifyUnknownPassword(password)
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("querying user credential: %w", err)
	}
	if !a.verifyCredential(password, credential.hash) {
		aliases := []string{AccountPasswordScope(credential.user.UniqueID)}
		if credential.user.Nickname != "" {
			aliases = append(aliases, AccountPasswordScope(credential.user.Nickname))
		}
		a.loginLimiter.RecordAliasFailure(AccountPasswordScope(identifier), aliases...)
		return nil, nil
	}
	return &credential.user, nil
}

func (a *AuthService) lookupCredentialByIdentifier(ctx context.Context, identifier string) (passwordCredential, error) {
	var credential passwordCredential
	const q = `SELECT id, unique_id, COALESCE(nickname, ''), is_bot, COALESCE(password_hash, '')
	          FROM users
	          WHERE unique_id = $1 OR nickname = $1
	          ORDER BY CASE WHEN unique_id = $1 THEN 0 ELSE 1 END
	          LIMIT 1`
	err := a.store.DB().QueryRowContext(ctx, q, identifier).Scan(
		&credential.user.ID,
		&credential.user.UniqueID,
		&credential.user.Nickname,
		&credential.user.IsBot,
		&credential.hash,
	)
	return credential, err
}

func (a *AuthService) verifyCredential(password, hash string) bool {
	if hash == "" {
		a.verifyUnknownPassword(password)
		return false
	}
	return a.passwordVerifier()(password, hash) == nil
}

func (a *AuthService) verifyUnknownPassword(password string) {
	// A match is deliberately not accepted: the fixed hash only equalizes the
	// Argon2 work performed for unknown accounts and wrong passwords.
	_ = a.passwordVerifier()(password, dummyPasswordHash)
}

func (a *AuthService) passwordVerifier() func(password, encodedHash string) error {
	if a.verifyPassword == nil {
		return VerifyPassword
	}
	return a.verifyPassword
}

// LookupUserByPublicKey returns the user whose stored public key matches, or
// ErrUserNotFound. Used to resolve accounts whose identity key was bound via
// a nickname login (the account's unique ID stays canonical).
// A key held by more than one account is refused rather than resolved to
// whichever row the database returns first.
func (a *AuthService) LookupUserByPublicKey(ctx context.Context, publicKey string) (*User, error) {
	// Keys are stored canonically; the exact spelling still matches rows
	// written before canonicalization.
	canonical := publicKey
	if c, err := CanonicalPublicKey(publicKey); err == nil {
		canonical = c
	}
	const q = `SELECT id, unique_id, COALESCE(nickname, ''), is_bot, COALESCE(identity_display_name, '') FROM users WHERE (public_key = $1 OR public_key = $2) AND public_key_proven LIMIT 2`
	rows, err := a.store.DB().QueryContext(ctx, q, canonical, publicKey)
	if err != nil {
		return nil, fmt.Errorf("querying user by public key: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var found []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.UniqueID, &u.Nickname, &u.IsBot, &u.DisplayName); err != nil {
			return nil, fmt.Errorf("scanning user by public key: %w", err)
		}
		found = append(found, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("querying user by public key: %w", err)
	}
	switch len(found) {
	case 0:
		return nil, ErrUserNotFound
	case 1:
		return &found[0], nil
	default:
		return nil, errors.New("public key is bound to more than one account")
	}
}

// BindPublicKey records a client's identity public key on an account (after
// a password login that also proved the key), enabling future
// challenge-response logins with that key. A key already bound to another
// account, one whose derived unique ID is another account's or a linked
// guest's principal, or another principal's device key is refused with
// ErrPublicKeyInUse so a login cannot claim someone else's identity key.
//
// proven says the login signed for the key. An unproven binding (an older
// client's password login) serves that account's sessions but never routes
// a key login; a proven binding upgrades it, and replaces another account's
// unproven claim of the same key.
func (a *AuthService) BindPublicKey(ctx context.Context, userID int64, publicKey string, proven bool) error {
	derivedUniqueID, err := UniqueIDFromPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("binding public key: %w", err)
	}
	// Store one canonical spelling, so an equivalent PEM encoding of a key
	// bound elsewhere cannot be bound a second time (the submitted spelling is
	// also checked, covering rows written before canonicalization).
	canonical, err := CanonicalPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("binding public key: %w", err)
	}
	tx, err := a.store.DB().BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("binding public key: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if proven {
		if _, err := tx.ExecContext(ctx, `UPDATE users SET public_key = NULL, public_key_proven = TRUE
			WHERE id <> $1 AND NOT public_key_proven AND (public_key = $2 OR public_key = $3)`, userID, canonical, publicKey); err != nil {
			return fmt.Errorf("binding public key: %w", err)
		}
	}
	// An unproven bind never displaces a proven key: an older client's
	// password login must not break the newer client's key login. The
	// conflict checks still run, so the session may use its key.
	const q = `UPDATE users SET public_key = CASE
				WHEN NOT $5::boolean AND public_key_proven AND public_key IS NOT NULL AND public_key NOT IN ($2, $4) THEN public_key
				ELSE $2 END,
			public_key_proven = $5::boolean OR COALESCE(public_key_proven AND public_key IS NOT NULL, FALSE)
		WHERE id = $1 AND NOT EXISTS (
			SELECT 1 FROM users other
			WHERE other.id <> $1 AND (other.public_key = $2 OR other.public_key = $4 OR other.unique_id = $3)
		) AND NOT EXISTS (
			-- another principal's device key, or a linked guest derived from this key
			SELECT 1 FROM user_devices d
			WHERE d.key_proven AND d.user_id IS DISTINCT FROM $1 AND (d.principal = $3 OR d.ed25519_public = $2 OR d.ed25519_public = $4)
		)`
	result, err := tx.ExecContext(ctx, q, userID, canonical, derivedUniqueID, publicKey, proven)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrPublicKeyInUse
		}
		return fmt.Errorf("binding public key: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("binding public key: %w", err)
	}
	if affected == 0 {
		return ErrPublicKeyInUse
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("binding public key: %w", err)
	}
	return nil
}

// SetE2EPublicKey stores a user's X25519 public key for E2EE (wave 4b).
func (a *AuthService) SetE2EPublicKey(ctx context.Context, userID int64, publicKey string) error {
	const q = `UPDATE users SET e2e_public_key = $2 WHERE id = $1`
	if _, err := a.store.DB().ExecContext(ctx, q, userID, publicKey); err != nil {
		return fmt.Errorf("storing e2e public key: %w", err)
	}
	return nil
}

// GetE2EPublicKey returns a user's X25519 public key ("" when never
// published, auth.ErrUserNotFound for unknown users).
func (a *AuthService) GetE2EPublicKey(ctx context.Context, uniqueID string) (string, error) {
	const q = `SELECT COALESCE(e2e_public_key, '') FROM users WHERE unique_id = $1`
	var key string
	err := a.store.DB().QueryRowContext(ctx, q, uniqueID).Scan(&key)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrUserNotFound
		}
		return "", fmt.Errorf("loading e2e public key: %w", err)
	}
	return key, nil
}

// GenerateChallenge returns 32 cryptographically random bytes for use in
// challenge-response authentication.
func GenerateChallenge() ([]byte, error) {
	c := make([]byte, LoginChallengeSize)
	for {
		if _, err := rand.Read(c); err != nil {
			return nil, fmt.Errorf("generating challenge: %w", err)
		}
		// Updated clients refuse "noxa-"-prefixed challenges (domain safety).
		if !bytes.HasPrefix(c, []byte("noxa-")) {
			return c, nil
		}
	}
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation produced by the lib/pq driver.
func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		code := string(pqErr.Code)
		return code == "23505"
	}
	return false
}
