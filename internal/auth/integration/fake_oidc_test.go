//go:build integration

package integration

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeOIDCServer struct {
	url         string
	caFile      string
	server      *http.Server
	listener    net.Listener
	requestLog  string
	signingKey  *rsa.PrivateKey
	nonce       string
	mode        fakeOIDCMode
	mu          sync.Mutex
	jwt         string
	redirectURI string
}

type fakeOIDCMode string

const (
	fakeOIDCHappy          fakeOIDCMode = "happy"
	fakeOIDCWrongNonce     fakeOIDCMode = "wrong_nonce"
	fakeOIDCWrongSignature fakeOIDCMode = "wrong_signature"
	fakeOIDCFailure        fakeOIDCMode = "idp_failure"
)

func newFakeOIDCServer(t *testing.T, root string) *fakeOIDCServer {
	t.Helper()
	certificate, caPEM := newOIDCCertificate(t)
	caFile := filepath.Join(root, "oidc-ca.pem")
	if err := os.WriteFile(caFile, caPEM, 0o600); err != nil {
		t.Fatalf("write OIDC CA: %v", err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12})
	if err != nil {
		t.Fatalf("listen fake OIDC TLS: %v", err)
	}
	signingKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate OIDC signing key: %v", err)
	}
	server := &fakeOIDCServer{caFile: caFile, listener: listener, requestLog: filepath.Join(root, "oidc-requests.log"), signingKey: signingKey}
	server.url = "https://" + listener.Addr().String()
	server.server = &http.Server{Handler: http.HandlerFunc(server.handle), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := server.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			t.Logf("fake OIDC server: %v", err)
		}
	}()
	t.Cleanup(func() {
		started := time.Now()
		t.Logf("teardown start fake OIDC")
		defer func() { t.Logf("teardown end fake OIDC duration=%s", time.Since(started)) }()
		ctx, cancel := contextWithTimeout(t)
		defer cancel()
		if err := server.server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown fake OIDC server: %v", err)
		}
	})
	return server
}

func (s *fakeOIDCServer) handle(writer http.ResponseWriter, request *http.Request) {
	if file, err := os.OpenFile(s.requestLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
		_, _ = file.WriteString(request.Method + " " + request.URL.Path + "\n")
		_ = file.Close()
	}
	switch request.URL.Path {
	case "/.well-known/openid-configuration":
		writeOIDCJSON(writer, map[string]any{
			"issuer": s.url, "authorization_endpoint": s.url + "/authorize", "token_endpoint": s.url + "/token", "jwks_uri": s.url + "/jwks", "id_token_signing_alg_values_supported": []string{"RS256"},
		})
	case "/authorize":
		redirectURI := request.URL.Query().Get("redirect_uri")
		redirect, _ := url.Parse(redirectURI)
		query := redirect.Query()
		query.Set("code", "integration-code")
		query.Set("state", request.URL.Query().Get("state"))
		redirect.RawQuery = query.Encode()
		s.mu.Lock()
		s.nonce = request.URL.Query().Get("nonce")
		s.redirectURI = redirectURI
		s.mu.Unlock()
		writer.Header().Set("Location", redirect.String())
		writer.WriteHeader(http.StatusFound)
	case "/jwks":
		writeOIDCJSON(writer, map[string]any{"keys": []any{map[string]string{"kty": "RSA", "kid": "integration", "alg": "RS256", "use": "sig", "n": base64.RawURLEncoding.EncodeToString(s.signingKey.PublicKey.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(s.signingKey.PublicKey.E)).Bytes())}}})
	case "/token":
		_ = request.ParseForm()
		s.mu.Lock()
		if s.mode == fakeOIDCFailure {
			s.mu.Unlock()
			http.Error(writer, "temporarily unavailable", http.StatusServiceUnavailable)
			return
		}
		s.jwt = s.idToken()
		s.mu.Unlock()
		writeOIDCJSON(writer, map[string]string{"access_token": "access", "token_type": "Bearer", "id_token": s.jwt})
	default:
		http.NotFound(writer, request)
	}
}

func (s *fakeOIDCServer) lastRedirectURI() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.redirectURI
}

func (s *fakeOIDCServer) idToken() string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","kid":"integration","typ":"JWT"}`))
	now := time.Now()
	nonce := s.nonce
	if s.mode == fakeOIDCWrongNonce {
		nonce = "wrong-nonce"
	}
	payload, _ := json.Marshal(map[string]any{"iss": s.url, "sub": "integration-subject", "aud": "client-id", "exp": now.Add(time.Minute).Unix(), "nbf": now.Add(-time.Second).Unix(), "iat": now.Unix(), "nonce": nonce, "name": "OIDC User", "email": "oidc@example.test", "role": "admin", "access_group_id": 999})
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(header + "." + encoded))
	signingKey := s.signingKey
	if s.mode == fakeOIDCWrongSignature {
		signingKey, _ = rsa.GenerateKey(rand.Reader, 2048)
	}
	signature, _ := rsa.SignPKCS1v15(rand.Reader, signingKey, crypto.SHA256, digest[:])
	return header + "." + encoded + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func (s *fakeOIDCServer) setMode(mode fakeOIDCMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mode = mode
}

func writeOIDCJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

func newOIDCCertificate(t *testing.T) (tls.Certificate, []byte) {
	t.Helper()
	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate OIDC CA key: %v", err)
	}
	caTemplate := certificateTemplate("Silo integration OIDC CA", true)
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create OIDC CA: %v", err)
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate OIDC leaf key: %v", err)
	}
	leafTemplate := certificateTemplate("127.0.0.1", false)
	leafTemplate.DNSNames = []string{"localhost"}
	leafTemplate.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create OIDC leaf certificate: %v", err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{leafDER, caDER}, PrivateKey: leafKey}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	return certificate, caPEM
}

func certificateTemplate(commonName string, isCA bool) *x509.Certificate {
	return &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: commonName}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), IsCA: isCA,
		BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment | x509.KeyUsageCertSign,
	}
}
