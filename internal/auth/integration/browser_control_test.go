//go:build integration

package integration

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"
)

type browserControlKind string

const (
	browserControlMode         browserControlKind = "mode"
	browserControlProvider     browserControlKind = "provider"
	browserControlPresentation browserControlKind = "presentation"
)

type browserFixturePresentation struct {
	ServerName          string `json:"server_name"`
	LoginSubtitle       string `json:"login_subtitle"`
	ProviderDisplayName string `json:"provider_display_name"`
}

type browserControlCommand struct {
	kind         browserControlKind
	mode         fakeOIDCMode
	enabled      bool
	presentation browserFixturePresentation
	result       chan browserControlResult
}

type browserControlResult struct {
	handoff browserFixtureHandoff
	err     error
}

type browserFixtureControl struct {
	url   string
	token string
}

type browserFixtureServer struct {
	url      string
	server   *http.Server
	listener net.Listener
	commands chan browserControlCommand
	errors   chan error
	token    string
}

func newBrowserFixtureServer(t *testing.T) *browserFixtureServer {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen for browser fixture controls: %v", err)
	}
	fixture := &browserFixtureServer{
		url:      "http://" + listener.Addr().String(),
		listener: listener,
		commands: make(chan browserControlCommand),
		errors:   make(chan error, 1),
		token:    newBrowserControlToken(t),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /mode", fixture.handleMode)
	mux.HandleFunc("POST /provider", fixture.handleProvider)
	mux.HandleFunc("POST /presentation", fixture.handlePresentation)
	fixture.server = &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		if err := fixture.server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			fixture.errors <- err
		}
	}()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := fixture.server.Shutdown(ctx); err != nil {
			t.Errorf("shutdown browser fixture controls: %v", err)
		}
	})
	return fixture
}

func (s *browserFixtureServer) handleMode(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Mode fakeOIDCMode `json:"mode"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, "invalid control request", http.StatusBadRequest)
		return
	}
	s.dispatch(writer, request, browserControlCommand{kind: browserControlMode, mode: payload.Mode, result: make(chan browserControlResult, 1)})
}

func (s *browserFixtureServer) handleProvider(writer http.ResponseWriter, request *http.Request) {
	var payload struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		http.Error(writer, "invalid control request", http.StatusBadRequest)
		return
	}
	s.dispatch(writer, request, browserControlCommand{kind: browserControlProvider, enabled: payload.Enabled, result: make(chan browserControlResult, 1)})
}

func (s *browserFixtureServer) handlePresentation(writer http.ResponseWriter, request *http.Request) {
	var presentation browserFixturePresentation
	if err := json.NewDecoder(request.Body).Decode(&presentation); err != nil {
		http.Error(writer, "invalid control request", http.StatusBadRequest)
		return
	}
	if presentation.ServerName == "" || presentation.LoginSubtitle == "" || presentation.ProviderDisplayName == "" {
		http.Error(writer, "presentation fields are required", http.StatusBadRequest)
		return
	}
	s.dispatch(writer, request, browserControlCommand{
		kind:         browserControlPresentation,
		presentation: presentation,
		result:       make(chan browserControlResult, 1),
	})
}

func (s *browserFixtureServer) dispatch(writer http.ResponseWriter, request *http.Request, command browserControlCommand) {
	if request.Header.Get("Authorization") != "Bearer "+s.token {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return
	}
	select {
	case s.commands <- command:
	case <-request.Context().Done():
		return
	}
	select {
	case result := <-command.result:
		if result.err != nil {
			http.Error(writer, "fixture control failed", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(result.handoff)
	case <-request.Context().Done():
	}
}

func (s *browserFixtureServer) control() browserFixtureControl {
	return browserFixtureControl{url: s.url, token: s.token}
}

func newBrowserControlToken(t *testing.T) string {
	t.Helper()
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		t.Fatalf("generate browser control token: %v", err)
	}
	return hex.EncodeToString(value)
}
