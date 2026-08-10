package abs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestBearerAuth_allowsOnlyActiveMatchingAccessToken(t *testing.T) {
	secret := []byte("test-secret-32-bytes-aaaaaaaaaaaaa")
	now := time.Now()
	tests := []struct {
		name       string
		tokenTTL   time.Duration
		stored     ABSToken
		lookupErr  error
		wantStatus int
		wantTouch  int
		wantNext   int
	}{
		{name: "active", tokenTTL: time.Hour, stored: ABSToken{UserID: "7", ProfileID: "p1", Type: "access", JTI: "active", ExpiresAt: now.Add(time.Hour)}, wantStatus: http.StatusNoContent, wantTouch: 1, wantNext: 1},
		{name: "missing", tokenTTL: time.Hour, lookupErr: ErrNotFound, wantStatus: http.StatusUnauthorized},
		{name: "revoked", tokenTTL: time.Hour, stored: ABSToken{UserID: "7", ProfileID: "p1", Type: "access", JTI: "revoked", ExpiresAt: now.Add(time.Hour), RevokedAt: &now}, wantStatus: http.StatusUnauthorized},
		{name: "expired JWT", tokenTTL: -time.Hour, stored: ABSToken{UserID: "7", ProfileID: "p1", Type: "access", JTI: "expired", ExpiresAt: now.Add(time.Hour)}, wantStatus: http.StatusUnauthorized},
		{name: "principal mismatch", tokenTTL: time.Hour, stored: ABSToken{UserID: "8", ProfileID: "p1", Type: "access", JTI: "mismatch", ExpiresAt: now.Add(time.Hour)}, wantStatus: http.StatusUnauthorized},
		{name: "store error", tokenTTL: time.Hour, lookupErr: errors.New("database unavailable"), wantStatus: http.StatusUnauthorized},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			jti := test.stored.JTI
			if jti == "" {
				jti = "missing"
			}
			token, err := IssueAccessToken(secret, "7", "p1", jti, test.tokenTTL)
			if err != nil {
				t.Fatalf("issue access token: %v", err)
			}
			store := &bearerTestStore{token: test.stored, lookupErr: test.lookupErr}
			handler := New(Dependencies{Config: &staticConfig{secret: secret}, TokenStore: store, MediaStore: noopMediaStore{}})
			nextCalls := 0
			req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			recorder := httptest.NewRecorder()
			handler.bearerAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				nextCalls++
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(recorder, req)
			if recorder.Code != test.wantStatus || store.touches != test.wantTouch || nextCalls != test.wantNext {
				t.Fatalf("bearer result status=%d touches=%d next=%d, want %d/%d/%d", recorder.Code, store.touches, nextCalls, test.wantStatus, test.wantTouch, test.wantNext)
			}
		})
	}
}

type bearerTestStore struct {
	token     ABSToken
	lookupErr error
	touches   int
}

func (s *bearerTestStore) InsertToken(context.Context, ABSToken) error { return nil }
func (s *bearerTestStore) GetTokenByJTI(context.Context, string) (ABSToken, error) {
	if s.lookupErr != nil {
		return ABSToken{}, s.lookupErr
	}
	return s.token, nil
}
func (s *bearerTestStore) RevokeTokenByJTI(context.Context, string) error { return nil }
func (s *bearerTestStore) RevokeTokenIfActive(context.Context, string) (ABSToken, error) {
	return ABSToken{}, ErrNotFound
}
func (s *bearerTestStore) RevokeTokensForPrincipal(context.Context, string, string) error { return nil }
func (s *bearerTestStore) TouchToken(context.Context, string) error {
	s.touches++
	return nil
}
