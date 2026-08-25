package security

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuthManagerInitializesAndAuthenticatesLocalToken(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	repository := &memoryTokenRepository{}
	store := &memoryTokenStore{}
	manager := newTestAuthManager(t, repository, store, func() time.Time { return now })
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	if len(store.token) == 0 || strings.Contains(repository.record.Hash, string(store.token)) || !strings.HasPrefix(repository.record.Hash, "$argon2id$") {
		t.Fatalf("token persistence boundary was not preserved")
	}
	if err := manager.AuthenticateBearer(context.Background(), string(store.token)); err != nil {
		t.Fatalf("AuthenticateBearer(valid) error = %v", err)
	}
	if err := manager.AuthenticateBearer(context.Background(), "wrong"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("AuthenticateBearer(wrong) error = %v", err)
	}
	if repository.usedAt == nil || !repository.usedAt.Equal(now) {
		t.Fatalf("last used time = %v, want %v", repository.usedAt, now)
	}
}

func TestAuthManagerRecoversSecureTokenWithoutDatabaseRecord(t *testing.T) {
	store := &memoryTokenStore{token: []byte("existing-secure-token-value-1234567890")}
	repository := &memoryTokenRepository{}
	manager := newTestAuthManager(t, repository, store, time.Now)
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() recovery error = %v", err)
	}
	if !repository.found || !verifyToken(store.token, repository.record.Hash) {
		t.Fatal("secure token was not recovered into a hash record")
	}
}

func TestAuthManagerRefusesMissingOrMismatchedSecureToken(t *testing.T) {
	params := argonParams{memory: 8 * 1024, time: 1, threads: 1, keyLen: 32}
	hash, err := hashToken([]byte("registered-token-value-1234567890"), params)
	if err != nil {
		t.Fatal(err)
	}
	record := TokenRecord{ID: localTokenID, Hash: hash, CreatedAt: time.Now().UTC()}
	for _, test := range []struct {
		name  string
		store *memoryTokenStore
		want  error
	}{
		{name: "missing", store: &memoryTokenStore{}, want: ErrSecureTokenMissing},
		{name: "mismatch", store: &memoryTokenStore{token: []byte("different-token-value-123456789")}, want: ErrAuthenticationFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &memoryTokenRepository{record: record, found: true}
			manager := newTestAuthManager(t, repository, test.store, time.Now)
			if err := manager.Initialize(context.Background()); !errors.Is(err, test.want) {
				t.Fatalf("Initialize() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestBrowserBootstrapIsSingleUseAndSessionBindsCSRF(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	manager := newTestAuthManager(t, &memoryTokenRepository{}, &memoryTokenStore{}, func() time.Time { return now })
	code, expiresAt, err := manager.IssueBootstrap()
	if err != nil || code == "" || !expiresAt.Equal(now.Add(time.Minute)) {
		t.Fatalf("IssueBootstrap() = (%q, %v, %v)", code, expiresAt, err)
	}
	credentials, err := manager.ExchangeBootstrap(code)
	if err != nil {
		t.Fatalf("ExchangeBootstrap() error = %v", err)
	}
	if _, err := manager.ExchangeBootstrap(code); !errors.Is(err, ErrBootstrapInvalid) {
		t.Fatalf("bootstrap replay error = %v", err)
	}
	if err := manager.AuthenticateSession(credentials.Session); err != nil {
		t.Fatalf("AuthenticateSession() error = %v", err)
	}
	if err := manager.ValidateCSRF(credentials.Session, credentials.CSRF); err != nil {
		t.Fatalf("ValidateCSRF() error = %v", err)
	}
	if err := manager.ValidateCSRF(credentials.Session, "wrong"); !errors.Is(err, ErrCSRFInvalid) {
		t.Fatalf("ValidateCSRF(wrong) error = %v", err)
	}
	now = now.Add(29 * time.Minute)
	refreshed, err := manager.RefreshSession(credentials.Session)
	if err != nil || refreshed.CSRF == "" || !refreshed.ExpiresAt.Equal(now.Add(30*time.Minute)) {
		t.Fatalf("RefreshSession() = (%+v, %v)", refreshed, err)
	}
	if err := manager.ValidateCSRF(credentials.Session, credentials.CSRF); err != nil {
		t.Fatalf("ValidateCSRF(previous grace) error = %v", err)
	}
	if err := manager.ValidateCSRF(credentials.Session, refreshed.CSRF); err != nil {
		t.Fatalf("ValidateCSRF(refreshed) error = %v", err)
	}
	now = now.Add(defaultCSRFGraceTTL)
	if err := manager.ValidateCSRF(credentials.Session, credentials.CSRF); !errors.Is(err, ErrCSRFInvalid) {
		t.Fatalf("ValidateCSRF(expired previous) error = %v", err)
	}
	manager.RevokeSession(credentials.Session)
	if err := manager.AuthenticateSession(credentials.Session); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("revoked session error = %v", err)
	}
}

func TestBrowserBootstrapAndSessionExpire(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	manager := newTestAuthManager(t, &memoryTokenRepository{}, &memoryTokenStore{}, func() time.Time { return now })
	code, _, _ := manager.IssueBootstrap()
	now = now.Add(time.Minute)
	if _, err := manager.ExchangeBootstrap(code); !errors.Is(err, ErrBootstrapInvalid) {
		t.Fatalf("expired bootstrap error = %v", err)
	}
	code, _, _ = manager.IssueBootstrap()
	credentials, _ := manager.ExchangeBootstrap(code)
	now = now.Add(defaultRenewalTTL)
	if err := manager.AuthenticateSession(credentials.Session); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("expired session error = %v", err)
	}
}

func TestBrowserSessionRenewalStopsAtAbsoluteExpiry(t *testing.T) {
	createdAt := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	now := createdAt
	manager := newTestAuthManager(t, &memoryTokenRepository{}, &memoryTokenStore{}, func() time.Time { return now })
	code, _, _ := manager.IssueBootstrap()
	credentials, err := manager.ExchangeBootstrap(code)
	if err != nil {
		t.Fatal(err)
	}
	if !credentials.ExpiresAt.Equal(createdAt.Add(defaultRenewalTTL)) || credentials.Remaining != defaultRenewalTTL {
		t.Fatalf("initial credentials = %+v", credentials)
	}
	sessionValue := credentials.Session
	for now.Before(createdAt.Add(7*time.Hour + 40*time.Minute)) {
		now = now.Add(20 * time.Minute)
		credentials, err = manager.RefreshSession(sessionValue)
		if err != nil {
			t.Fatalf("RefreshSession(%v) error = %v", now.Sub(createdAt), err)
		}
	}
	wantAbsolute := createdAt.Add(defaultAbsoluteTTL)
	if !credentials.ExpiresAt.Equal(wantAbsolute) {
		t.Fatalf("capped expiry = %v, want %v", credentials.ExpiresAt, wantAbsolute)
	}
	now = wantAbsolute
	if _, err := manager.RefreshSession(sessionValue); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("refresh at absolute expiry error = %v", err)
	}
}

func TestBrowserSessionCannotBeRevivedAfterRenewalExpiryOrRestart(t *testing.T) {
	now := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	repository := &memoryTokenRepository{}
	store := &memoryTokenStore{}
	manager := newTestAuthManager(t, repository, store, func() time.Time { return now })
	code, _, _ := manager.IssueBootstrap()
	credentials, _ := manager.ExchangeBootstrap(code)
	restarted := newTestAuthManager(t, repository, store, func() time.Time { return now })
	if _, err := restarted.RefreshSession(credentials.Session); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("refresh after restart error = %v", err)
	}
	now = now.Add(defaultRenewalTTL)
	if _, err := manager.RefreshSession(credentials.Session); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("refresh after renewal expiry error = %v", err)
	}
}

func TestBrowserSessionConcurrentAccessIsBounded(t *testing.T) {
	now := time.Date(2026, 8, 19, 1, 0, 0, 0, time.UTC)
	manager := newTestAuthManager(t, &memoryTokenRepository{}, &memoryTokenStore{}, func() time.Time { return now })
	code, _, _ := manager.IssueBootstrap()
	credentials, _ := manager.ExchangeBootstrap(code)

	const workers = 24
	var wait sync.WaitGroup
	errorsSeen := make(chan error, workers*3)
	for index := 0; index < workers; index++ {
		wait.Add(3)
		go func() {
			defer wait.Done()
			_, err := manager.RefreshSession(credentials.Session)
			errorsSeen <- err
		}()
		go func() {
			defer wait.Done()
			errorsSeen <- manager.AuthenticateSession(credentials.Session)
		}()
		go func() {
			defer wait.Done()
			errorsSeen <- manager.ValidateCSRF(credentials.Session, credentials.CSRF)
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil && !errors.Is(err, ErrCSRFInvalid) {
			t.Fatalf("concurrent authentication error = %v", err)
		}
	}
	if len(manager.sessions) != 1 {
		t.Fatalf("session count = %d, want 1", len(manager.sessions))
	}
}

func TestAuthManagerRotatesTokenAndInvalidatesBrowserCredentials(t *testing.T) {
	now := time.Date(2026, 8, 18, 12, 0, 0, 0, time.UTC)
	repository := &memoryTokenRepository{}
	store := &memoryTokenStore{}
	manager := newTestAuthManager(t, repository, store, func() time.Time { return now })
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	prior := append([]byte(nil), store.token...)
	code, _, _ := manager.IssueBootstrap()
	credentials, _ := manager.ExchangeBootstrap(code)
	rotated, err := manager.Rotate(context.Background())
	if err != nil || rotated.ID == "" || rotated.CreatedAt != now {
		t.Fatalf("Rotate() = (%+v, %v)", rotated, err)
	}
	if string(store.token) == string(prior) || repository.pendingFound {
		t.Fatal("rotation did not replace secure token and clear journal")
	}
	if err := manager.AuthenticateBearer(context.Background(), string(prior)); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("prior bearer error = %v", err)
	}
	if err := manager.AuthenticateBearer(context.Background(), string(store.token)); err != nil {
		t.Fatalf("rotated bearer error = %v", err)
	}
	if err := manager.AuthenticateSession(credentials.Session); !errors.Is(err, ErrSessionInvalid) {
		t.Fatalf("prior session error = %v", err)
	}
}

func TestAuthManagerRecoversPreparedTokenRotation(t *testing.T) {
	params := argonParams{memory: 8 * 1024, time: 1, threads: 1, keyLen: 32}
	oldHash, _ := hashToken([]byte("old-local-token-value-1234567890"), params)
	newToken := []byte("new-local-token-value-1234567890")
	newHash, _ := hashToken(newToken, params)
	repository := &memoryTokenRepository{
		record: TokenRecord{ID: "local", Hash: oldHash, CreatedAt: time.Now().UTC()}, found: true,
		pending: TokenRecord{ID: "local-next", Hash: newHash, CreatedAt: time.Now().UTC()}, pendingFound: true,
	}
	manager := newTestAuthManager(t, repository, &memoryTokenStore{token: newToken}, time.Now)
	if err := manager.Initialize(context.Background()); err != nil {
		t.Fatalf("Initialize() rotation recovery error = %v", err)
	}
	if repository.record.ID != "local-next" || repository.pendingFound {
		t.Fatalf("rotation recovery state = active %q, pending=%t", repository.record.ID, repository.pendingFound)
	}
}

func newTestAuthManager(t *testing.T, repository TokenRepository, store TokenStore, clock func() time.Time) *AuthManager {
	t.Helper()
	manager, err := NewAuthManager(AuthConfig{
		Repository: repository, Store: store, Clock: clock,
		ArgonMemory: 8 * 1024, ArgonTime: 1, ArgonThreads: 1,
	})
	if err != nil {
		t.Fatalf("NewAuthManager() error = %v", err)
	}
	return manager
}

type memoryTokenStore struct {
	token []byte
}

func (store *memoryTokenStore) Load() ([]byte, bool, error) {
	return append([]byte(nil), store.token...), len(store.token) > 0, nil
}

func (store *memoryTokenStore) Save(token []byte) error {
	store.token = append([]byte(nil), token...)
	return nil
}

type memoryTokenRepository struct {
	record       TokenRecord
	found        bool
	usedAt       *time.Time
	pending      TokenRecord
	pendingFound bool
}

func (repository *memoryTokenRepository) Active(context.Context) (TokenRecord, bool, error) {
	return repository.record, repository.found, nil
}

func (repository *memoryTokenRepository) Create(_ context.Context, record TokenRecord) error {
	repository.record, repository.found = record, true
	return nil
}

func (repository *memoryTokenRepository) MarkUsed(_ context.Context, _ string, now time.Time) error {
	repository.usedAt = &now
	return nil
}

func (repository *memoryTokenRepository) PendingRotation(context.Context) (TokenRecord, bool, error) {
	return repository.pending, repository.pendingFound, nil
}

func (repository *memoryTokenRepository) PrepareRotation(_ context.Context, record TokenRecord) error {
	repository.pending, repository.pendingFound = record, true
	return nil
}

func (repository *memoryTokenRepository) CommitRotation(_ context.Context, _ string, record TokenRecord, _ time.Time) error {
	repository.record, repository.found = record, true
	repository.pending, repository.pendingFound = TokenRecord{}, false
	return nil
}

func (repository *memoryTokenRepository) ClearRotation(context.Context) error {
	repository.pending, repository.pendingFound = TokenRecord{}, false
	return nil
}
