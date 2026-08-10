package auth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Silo-Server/silo-server/internal/secret"
)

func TestInMemoryOAuthStore_InsertAndGetAndDelete(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()

	in := OAuthSession{
		State:         "state-1",
		InstallID:     "42",
		RedirectURI:   "https://example.com/cb",
		ProviderState: []byte(`{"pkce_verifier":"abc"}`),
		NextURL:       "/me",
		ExpiresAt:     time.Now().Add(10 * time.Minute),
	}
	if err := st.Insert(ctx, in); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	out, err := st.GetAndDelete(ctx, "state-1")
	if err != nil {
		t.Fatalf("GetAndDelete: %v", err)
	}
	if out.InstallID != "42" || out.RedirectURI != in.RedirectURI || out.NextURL != "/me" {
		t.Errorf("out = %+v", out)
	}
	if string(out.ProviderState) != `{"pkce_verifier":"abc"}` {
		t.Errorf("ProviderState = %s", out.ProviderState)
	}

	if _, err := st.GetAndDelete(ctx, "state-1"); !errors.Is(err, ErrOAuthSessionNotFound) {
		t.Errorf("expected ErrOAuthSessionNotFound, got %v", err)
	}
}

func TestInMemoryOAuthStore_DefaultsAndValidation(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()

	// Missing state — error.
	if err := st.Insert(ctx, OAuthSession{InstallID: "1", RedirectURI: "/x", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Error("Insert should reject empty state")
	}
	// Missing install_id — error.
	if err := st.Insert(ctx, OAuthSession{State: "s", RedirectURI: "/x", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Error("Insert should reject empty install_id")
	}
	// Missing expires_at — error.
	if err := st.Insert(ctx, OAuthSession{State: "s", InstallID: "1", RedirectURI: "/x"}); err == nil {
		t.Error("Insert should reject zero expires_at")
	}
	// Missing redirect_uri — error.
	if err := st.Insert(ctx, OAuthSession{State: "s", InstallID: "1", ExpiresAt: time.Now().Add(time.Hour)}); err == nil {
		t.Error("Insert should reject empty redirect_uri")
	}

	// next_url and provider_state default.
	if err := st.Insert(ctx, OAuthSession{State: "s2", InstallID: "1", RedirectURI: "/x", ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	out, err := st.GetAndDelete(ctx, "s2")
	if err != nil {
		t.Fatalf("GetAndDelete: %v", err)
	}
	if out.NextURL != "/" {
		t.Errorf("NextURL default = %q, want /", out.NextURL)
	}
	if string(out.ProviderState) != "{}" {
		t.Errorf("ProviderState default = %s, want {}", out.ProviderState)
	}
}

func TestInMemoryOAuthStore_DeleteExpired(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()
	now := time.Now()

	_ = st.Insert(ctx, OAuthSession{State: "old", InstallID: "i", RedirectURI: "/", ExpiresAt: now.Add(-time.Hour)})
	_ = st.Insert(ctx, OAuthSession{State: "new", InstallID: "i", RedirectURI: "/", ExpiresAt: now.Add(time.Hour)})

	deleted, err := st.DeleteExpired(ctx, now)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if deleted != 1 {
		t.Errorf("deleted = %d, want 1", deleted)
	}
	if _, err := st.GetAndDelete(ctx, "new"); err != nil {
		t.Errorf("'new' should still be present: %v", err)
	}
}

func TestInMemoryOAuthStore_DuplicateState(t *testing.T) {
	st := NewInMemoryOAuthStore()
	ctx := context.Background()
	s := OAuthSession{State: "dup", InstallID: "1", RedirectURI: "/", ExpiresAt: time.Now().Add(time.Hour)}
	if err := st.Insert(ctx, s); err != nil {
		t.Fatalf("first Insert: %v", err)
	}
	if err := st.Insert(ctx, s); err == nil {
		t.Error("second Insert should reject duplicate state")
	}
}

func TestPGOAuthStore_EncryptsCompletionTokens(t *testing.T) {
	cipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	st := NewPGOAuthStore(nil, []byte("test-secret"), cipher)
	completion := OAuthCompletion{
		Code:         "completion-code",
		AccessToken:  "access-token-value",
		RefreshToken: "refresh-token-value",
		ExpiresIn:    900,
		NextURL:      "/me",
		ExpiresAt:    time.Now().Add(time.Minute),
	}
	codeHash := oauthCompletionCodeHash(completion.Code)

	ciphertext, err := st.encryptCompletionTokens(completion, codeHash)
	if err != nil {
		t.Fatalf("encryptCompletionTokens: %v", err)
	}
	if strings.Contains(ciphertext, completion.AccessToken) || strings.Contains(ciphertext, completion.RefreshToken) {
		t.Fatalf("ciphertext includes plaintext tokens: %q", ciphertext)
	}

	var out OAuthCompletion
	if err := st.decryptCompletionTokens(ciphertext, codeHash, &out); err != nil {
		t.Fatalf("decryptCompletionTokens: %v", err)
	}
	if out.AccessToken != completion.AccessToken || out.RefreshToken != completion.RefreshToken {
		t.Fatalf("tokens = %q/%q", out.AccessToken, out.RefreshToken)
	}
}

func TestOAuthStateCipherRejectsWrongKeyAndAAD(t *testing.T) {
	// Given
	firstCipher, err := secret.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatalf("new first cipher: %v", err)
	}
	secondCipher, err := secret.New([]byte("abcdefghijklmnopqrstuvwxyz123456"))
	if err != nil {
		t.Fatalf("new second cipher: %v", err)
	}
	session := OAuthSession{State: "state-one", InstallID: "42"}
	ciphertext, err := firstCipher.Encrypt(`{"pkce_verifier":"secret"}`, oauthSessionProviderStateAAD(session))
	if err != nil {
		t.Fatalf("encrypt provider state: %v", err)
	}

	// When
	_, wrongKeyErr := secondCipher.Decrypt(ciphertext, oauthSessionProviderStateAAD(session))
	otherSession := session
	otherSession.State = "state-two"
	_, wrongAADErr := firstCipher.Decrypt(ciphertext, oauthSessionProviderStateAAD(otherSession))

	// Then
	if wrongKeyErr == nil {
		t.Fatal("wrong cipher key decrypted provider state")
	}
	if wrongAADErr == nil {
		t.Fatal("wrong row-bound AAD decrypted provider state")
	}
}
