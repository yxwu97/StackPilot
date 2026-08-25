package security

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/argon2"
)

const (
	localTokenID          = "local"
	defaultArgonMemory    = 64 * 1024
	defaultArgonTime      = 3
	defaultArgonThreads   = 2
	defaultArgonKeyLength = 32
	defaultBootstrapTTL   = time.Minute
	defaultRenewalTTL     = 30 * time.Minute
	defaultAbsoluteTTL    = 8 * time.Hour
	defaultCSRFGraceTTL   = 5 * time.Minute
	maxBootstraps         = 128
	maxSessions           = 256
)

var (
	ErrAuthenticationFailed = errors.New("local authentication failed")
	ErrBootstrapInvalid     = errors.New("authentication bootstrap is invalid")
	ErrSessionInvalid       = errors.New("browser session is invalid")
	ErrCSRFInvalid          = errors.New("browser CSRF value is invalid")
	ErrAuthCapacity         = errors.New("authentication session capacity reached")
	ErrSecureTokenMissing   = errors.New("secure local token is missing")
)

// TokenRecord is the minimum hash metadata used by local authentication.
type TokenRecord struct {
	ID        string
	Hash      string
	CreatedAt time.Time
}

// TokenRepository persists hashes and safe usage metadata.
type TokenRepository interface {
	Active(context.Context) (TokenRecord, bool, error)
	Create(context.Context, TokenRecord) error
	MarkUsed(context.Context, string, time.Time) error
	PendingRotation(context.Context) (TokenRecord, bool, error)
	PrepareRotation(context.Context, TokenRecord) error
	CommitRotation(context.Context, string, TokenRecord, time.Time) error
	ClearRotation(context.Context) error
}

// TokenStore persists plaintext only through an OS-protected representation.
type TokenStore interface {
	Load() ([]byte, bool, error)
	Save([]byte) error
}

// AuthConfig wires local-token persistence and bounded ephemeral browser credentials.
type AuthConfig struct {
	Repository   TokenRepository
	Store        TokenStore
	Clock        func() time.Time
	ArgonMemory  uint32
	ArgonTime    uint32
	ArgonThreads uint8
	BootstrapTTL time.Duration
	RenewalTTL   time.Duration
	AbsoluteTTL  time.Duration
	CSRFGraceTTL time.Duration
}

// AuthManager verifies the local token and owns in-memory browser credentials.
type AuthManager struct {
	repository   TokenRepository
	store        TokenStore
	clock        func() time.Time
	params       argonParams
	bootstrapTTL time.Duration
	renewalTTL   time.Duration
	absoluteTTL  time.Duration
	csrfGraceTTL time.Duration
	mutex        sync.Mutex
	rotationLock sync.Mutex
	bootstraps   map[[32]byte]time.Time
	sessions     map[[32]byte]browserSession
}

type argonParams struct {
	memory  uint32
	time    uint32
	threads uint8
	keyLen  uint32
}

type browserSession struct {
	csrf                  [32]byte
	previousCSRF          [32]byte
	previousCSRFExpiresAt time.Time
	renewalExpiresAt      time.Time
	absoluteExpiresAt     time.Time
}

// BrowserCredentials carries ephemeral session values and their authoritative remaining lifetime.
type BrowserCredentials struct {
	Session   string
	CSRF      string
	ExpiresAt time.Time
	Remaining time.Duration
}

// RefreshSession atomically renews a browser session and rotates its CSRF value.
func (manager *AuthManager) RefreshSession(sessionValue string) (BrowserCredentials, error) {
	now := manager.clock().UTC()
	sessionDigest := sha256.Sum256([]byte(sessionValue))
	csrfValue, err := randomEncoded(32)
	if err != nil {
		return BrowserCredentials{}, err
	}
	defer zeroBytes(csrfValue)
	csrfDigest := sha256.Sum256(csrfValue)
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.pruneLocked(now)
	session, found := manager.sessions[sessionDigest]
	if !found || sessionExpired(session, now) {
		return BrowserCredentials{}, ErrSessionInvalid
	}
	session.renewalExpiresAt = minimumTime(now.Add(manager.renewalTTL), session.absoluteExpiresAt)
	session.previousCSRF = session.csrf
	session.previousCSRFExpiresAt = minimumTime(now.Add(manager.csrfGraceTTL), session.renewalExpiresAt)
	session.csrf = csrfDigest
	manager.sessions[sessionDigest] = session
	return sessionCredentials(sessionValue, csrfValue, session, now), nil
}

// NewAuthManager validates configuration; Initialize must run before serving requests.
func NewAuthManager(config AuthConfig) (*AuthManager, error) {
	if config.Repository == nil || config.Store == nil {
		return nil, fmt.Errorf("authentication repository and secure store are required")
	}
	applyAuthDefaults(&config)
	if config.ArgonMemory < 8*1024 || config.ArgonTime == 0 || config.ArgonThreads == 0 || config.BootstrapTTL <= 0 ||
		config.RenewalTTL <= 0 || config.AbsoluteTTL <= 0 || config.CSRFGraceTTL <= 0 || config.RenewalTTL > config.AbsoluteTTL {
		return nil, fmt.Errorf("authentication bounds are invalid")
	}
	return &AuthManager{
		repository: config.Repository, store: config.Store, clock: config.Clock,
		params:       argonParams{memory: config.ArgonMemory, time: config.ArgonTime, threads: config.ArgonThreads, keyLen: defaultArgonKeyLength},
		bootstrapTTL: config.BootstrapTTL, renewalTTL: config.RenewalTTL,
		absoluteTTL: config.AbsoluteTTL, csrfGraceTTL: config.CSRFGraceTTL,
		bootstraps: make(map[[32]byte]time.Time), sessions: make(map[[32]byte]browserSession),
	}, nil
}

func applyAuthDefaults(config *AuthConfig) {
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.ArgonMemory == 0 {
		config.ArgonMemory = defaultArgonMemory
	}
	if config.ArgonTime == 0 {
		config.ArgonTime = defaultArgonTime
	}
	if config.ArgonThreads == 0 {
		config.ArgonThreads = defaultArgonThreads
	}
	if config.BootstrapTTL == 0 {
		config.BootstrapTTL = defaultBootstrapTTL
	}
	if config.RenewalTTL == 0 {
		config.RenewalTTL = defaultRenewalTTL
	}
	if config.AbsoluteTTL == 0 {
		config.AbsoluteTTL = defaultAbsoluteTTL
	}
	if config.CSRFGraceTTL == 0 {
		config.CSRFGraceTTL = defaultCSRFGraceTTL
	}
}

// Initialize creates or validates the local token across secure storage and SQLite.
func (manager *AuthManager) Initialize(ctx context.Context) error {
	manager.rotationLock.Lock()
	defer manager.rotationLock.Unlock()
	token, stored, err := manager.store.Load()
	if err != nil {
		return fmt.Errorf("load secure local token: %w", err)
	}
	defer zeroBytes(token)
	record, registered, err := manager.repository.Active(ctx)
	if err != nil {
		return err
	}
	pending, rotating, err := manager.repository.PendingRotation(ctx)
	if err != nil {
		return err
	}
	record, registered, err = manager.recoverRotation(ctx, token, stored, record, registered, pending, rotating)
	if err != nil {
		return err
	}
	if registered && !stored {
		return ErrSecureTokenMissing
	}
	if registered {
		if !verifyToken(token, record.Hash) {
			return ErrAuthenticationFailed
		}
		return nil
	}
	return manager.initializeMissingRecord(ctx, token, stored)
}

func (manager *AuthManager) recoverRotation(ctx context.Context, token []byte, stored bool, active TokenRecord, registered bool, pending TokenRecord, rotating bool) (TokenRecord, bool, error) {
	if !rotating {
		return active, registered, nil
	}
	if !registered || !stored {
		return TokenRecord{}, false, ErrSecureTokenMissing
	}
	if verifyToken(token, pending.Hash) {
		if err := manager.repository.CommitRotation(ctx, active.ID, pending, manager.clock().UTC()); err != nil {
			return TokenRecord{}, false, err
		}
		return pending, true, nil
	}
	if verifyToken(token, active.Hash) {
		if err := manager.repository.ClearRotation(ctx); err != nil {
			return TokenRecord{}, false, err
		}
		return active, true, nil
	}
	return TokenRecord{}, false, ErrAuthenticationFailed
}

func (manager *AuthManager) initializeMissingRecord(ctx context.Context, token []byte, stored bool) error {
	if !stored {
		var err error
		token, err = randomEncoded(32)
		if err != nil {
			return err
		}
		defer zeroBytes(token)
		if err := manager.store.Save(token); err != nil {
			return fmt.Errorf("save secure local token: %w", err)
		}
	}
	hash, err := hashToken(token, manager.params)
	if err != nil {
		return err
	}
	return manager.repository.Create(ctx, TokenRecord{ID: localTokenID, Hash: hash, CreatedAt: manager.clock().UTC()})
}

// AuthenticateBearer verifies a long-lived token and updates only safe usage metadata.
func (manager *AuthManager) AuthenticateBearer(ctx context.Context, token string) error {
	manager.rotationLock.Lock()
	defer manager.rotationLock.Unlock()
	record, found, err := manager.repository.Active(ctx)
	if err != nil {
		return err
	}
	if !found || !verifyToken([]byte(token), record.Hash) {
		return ErrAuthenticationFailed
	}
	return manager.repository.MarkUsed(ctx, record.ID, manager.clock().UTC())
}

// Rotate replaces the OS-protected local token through a crash-recoverable journal.
func (manager *AuthManager) Rotate(ctx context.Context) (TokenRecord, error) {
	manager.rotationLock.Lock()
	defer manager.rotationLock.Unlock()
	oldToken, active, err := manager.loadActiveToken(ctx)
	if err != nil {
		return TokenRecord{}, err
	}
	defer zeroBytes(oldToken)
	pending, newToken, err := manager.newRotationRecord()
	if err != nil {
		return TokenRecord{}, err
	}
	defer zeroBytes(newToken)
	if err := manager.repository.PrepareRotation(ctx, pending); err != nil {
		return TokenRecord{}, err
	}
	if err := manager.store.Save(newToken); err != nil {
		_ = manager.repository.ClearRotation(ctx)
		return TokenRecord{}, fmt.Errorf("save rotated local token: %w", err)
	}
	if err := manager.commitRotation(ctx, active, pending, oldToken); err != nil {
		return TokenRecord{}, err
	}
	manager.clearBrowserCredentials()
	return TokenRecord{ID: pending.ID, CreatedAt: pending.CreatedAt}, nil
}

func (manager *AuthManager) loadActiveToken(ctx context.Context) ([]byte, TokenRecord, error) {
	token, found, err := manager.store.Load()
	if err != nil {
		return nil, TokenRecord{}, err
	}
	record, registered, err := manager.repository.Active(ctx)
	if err != nil {
		zeroBytes(token)
		return nil, TokenRecord{}, err
	}
	if !found || !registered || !verifyToken(token, record.Hash) {
		zeroBytes(token)
		return nil, TokenRecord{}, ErrAuthenticationFailed
	}
	return token, record, nil
}

func (manager *AuthManager) newRotationRecord() (TokenRecord, []byte, error) {
	token, err := randomEncoded(32)
	if err != nil {
		return TokenRecord{}, nil, err
	}
	hash, err := hashToken(token, manager.params)
	if err != nil {
		zeroBytes(token)
		return TokenRecord{}, nil, err
	}
	suffix, err := randomEncoded(8)
	if err != nil {
		zeroBytes(token)
		return TokenRecord{}, nil, err
	}
	id := "local-" + string(suffix)
	zeroBytes(suffix)
	return TokenRecord{ID: id, Hash: hash, CreatedAt: manager.clock().UTC()}, token, nil
}

func (manager *AuthManager) commitRotation(ctx context.Context, active, pending TokenRecord, oldToken []byte) error {
	if err := manager.repository.CommitRotation(ctx, active.ID, pending, manager.clock().UTC()); err == nil {
		return nil
	} else if restoreErr := manager.store.Save(oldToken); restoreErr != nil {
		return errors.Join(fmt.Errorf("commit local token rotation: %w", err), fmt.Errorf("restore prior local token: %w", restoreErr))
	} else if clearErr := manager.repository.ClearRotation(ctx); clearErr != nil {
		return errors.Join(fmt.Errorf("commit local token rotation: %w", err), fmt.Errorf("clear token rotation journal: %w", clearErr))
	} else {
		return fmt.Errorf("commit local token rotation: %w", err)
	}
}

func (manager *AuthManager) clearBrowserCredentials() {
	manager.mutex.Lock()
	clear(manager.bootstraps)
	clear(manager.sessions)
	manager.mutex.Unlock()
}

// IssueBootstrap creates one single-use browser bootstrap code.
func (manager *AuthManager) IssueBootstrap() (string, time.Time, error) {
	code, err := randomEncoded(32)
	if err != nil {
		return "", time.Time{}, err
	}
	now := manager.clock().UTC()
	digest := sha256.Sum256(code)
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.pruneLocked(now)
	if len(manager.bootstraps) >= maxBootstraps {
		zeroBytes(code)
		return "", time.Time{}, ErrAuthCapacity
	}
	expiresAt := now.Add(manager.bootstrapTTL)
	manager.bootstraps[digest] = expiresAt
	value := string(code)
	zeroBytes(code)
	return value, expiresAt, nil
}

// ExchangeBootstrap consumes one code and creates a bounded browser session.
func (manager *AuthManager) ExchangeBootstrap(code string) (BrowserCredentials, error) {
	now := manager.clock().UTC()
	digest := sha256.Sum256([]byte(code))
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.pruneLocked(now)
	expiresAt, found := manager.bootstraps[digest]
	delete(manager.bootstraps, digest)
	if !found || !expiresAt.After(now) {
		return BrowserCredentials{}, ErrBootstrapInvalid
	}
	if len(manager.sessions) >= maxSessions {
		return BrowserCredentials{}, ErrAuthCapacity
	}
	return manager.newSessionLocked(now)
}

func (manager *AuthManager) newSessionLocked(now time.Time) (BrowserCredentials, error) {
	sessionValue, err := randomEncoded(32)
	if err != nil {
		return BrowserCredentials{}, err
	}
	csrfValue, err := randomEncoded(32)
	if err != nil {
		zeroBytes(sessionValue)
		return BrowserCredentials{}, err
	}
	sessionDigest, csrfDigest := sha256.Sum256(sessionValue), sha256.Sum256(csrfValue)
	absoluteExpiresAt := now.Add(manager.absoluteTTL)
	renewalExpiresAt := minimumTime(now.Add(manager.renewalTTL), absoluteExpiresAt)
	session := browserSession{
		csrf: csrfDigest, renewalExpiresAt: renewalExpiresAt, absoluteExpiresAt: absoluteExpiresAt,
	}
	manager.sessions[sessionDigest] = session
	credentials := sessionCredentials(string(sessionValue), csrfValue, session, now)
	zeroBytes(sessionValue)
	zeroBytes(csrfValue)
	return credentials, nil
}

// AuthenticateSession validates an opaque cookie without retaining its plaintext value.
func (manager *AuthManager) AuthenticateSession(value string) error {
	now := manager.clock().UTC()
	digest := sha256.Sum256([]byte(value))
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.pruneLocked(now)
	if session, found := manager.sessions[digest]; !found || sessionExpired(session, now) {
		return ErrSessionInvalid
	}
	return nil
}

// ValidateCSRF proves that a browser mutation value belongs to its session.
func (manager *AuthManager) ValidateCSRF(sessionValue, csrfValue string) error {
	now := manager.clock().UTC()
	sessionDigest, csrfDigest := sha256.Sum256([]byte(sessionValue)), sha256.Sum256([]byte(csrfValue))
	manager.mutex.Lock()
	defer manager.mutex.Unlock()
	manager.pruneLocked(now)
	session, found := manager.sessions[sessionDigest]
	if !found || sessionExpired(session, now) || !validCSRFDigest(session, csrfDigest, now) {
		return ErrCSRFInvalid
	}
	return nil
}

// RevokeSession removes one browser session during logout.
func (manager *AuthManager) RevokeSession(value string) {
	digest := sha256.Sum256([]byte(value))
	manager.mutex.Lock()
	delete(manager.sessions, digest)
	manager.mutex.Unlock()
}

func (manager *AuthManager) pruneLocked(now time.Time) {
	for digest, expiresAt := range manager.bootstraps {
		if !expiresAt.After(now) {
			delete(manager.bootstraps, digest)
		}
	}
	for digest, session := range manager.sessions {
		if sessionExpired(session, now) {
			delete(manager.sessions, digest)
		}
	}
}

func sessionExpired(session browserSession, now time.Time) bool {
	return !session.renewalExpiresAt.After(now) || !session.absoluteExpiresAt.After(now)
}

func validCSRFDigest(session browserSession, digest [32]byte, now time.Time) bool {
	if subtle.ConstantTimeCompare(session.csrf[:], digest[:]) == 1 {
		return true
	}
	return session.previousCSRFExpiresAt.After(now) && subtle.ConstantTimeCompare(session.previousCSRF[:], digest[:]) == 1
}

func sessionCredentials(sessionValue string, csrfValue []byte, session browserSession, now time.Time) BrowserCredentials {
	return BrowserCredentials{
		Session: sessionValue, CSRF: string(csrfValue), ExpiresAt: session.renewalExpiresAt,
		Remaining: session.renewalExpiresAt.Sub(now),
	}
}

func minimumTime(left, right time.Time) time.Time {
	if left.Before(right) {
		return left
	}
	return right
}

func hashToken(token []byte, params argonParams) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate token hash salt: %w", err)
	}
	key := argon2.IDKey(token, salt, params.time, params.memory, params.threads, params.keyLen)
	defer zeroBytes(key)
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s", params.memory, params.time, params.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func verifyToken(token []byte, encoded string) bool {
	params, salt, expected, ok := parseTokenHash(encoded)
	if !ok {
		return false
	}
	actual := argon2.IDKey(token, salt, params.time, params.memory, params.threads, params.keyLen)
	defer zeroBytes(actual)
	return subtle.ConstantTimeCompare(actual, expected) == 1
}

func parseTokenHash(encoded string) (argonParams, []byte, []byte, bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return argonParams{}, nil, nil, false
	}
	var params argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &params.memory, &params.time, &params.threads); err != nil {
		return argonParams{}, nil, nil, false
	}
	salt, saltErr := base64.RawStdEncoding.DecodeString(parts[4])
	key, keyErr := base64.RawStdEncoding.DecodeString(parts[5])
	params.keyLen = uint32(len(key))
	valid := saltErr == nil && keyErr == nil && len(salt) >= 16 && params.memory >= 8*1024 && params.memory <= 256*1024 &&
		params.time > 0 && params.time <= 10 && params.threads > 0 && params.threads <= 16 && params.keyLen >= 16 && params.keyLen <= 64
	return params, salt, key, valid
}

func randomEncoded(size int) ([]byte, error) {
	raw := make([]byte, size)
	if _, err := rand.Read(raw); err != nil {
		return nil, fmt.Errorf("generate authentication value: %w", err)
	}
	encoded := make([]byte, base64.RawURLEncoding.EncodedLen(len(raw)))
	base64.RawURLEncoding.Encode(encoded, raw)
	zeroBytes(raw)
	return encoded, nil
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
