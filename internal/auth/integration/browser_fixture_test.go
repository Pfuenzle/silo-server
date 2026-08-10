//go:build integration

package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

const browserFixturePollInterval = 250 * time.Millisecond

type browserFixtureProvider struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

type browserFixtureAccount struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type browserFixtureBranding struct {
	ServerName    string `json:"server_name"`
	LoginSubtitle string `json:"login_subtitle"`
}

type browserFixtureHandoff struct {
	SiloURL         string                 `json:"silo_url"`
	ControlURL      string                 `json:"control_url"`
	ControlToken    string                 `json:"control_token"`
	Provider        browserFixtureProvider `json:"provider"`
	Branding        browserFixtureBranding `json:"branding"`
	LocalAccount    browserFixtureAccount  `json:"local_account"`
	OIDCProfileName string                 `json:"oidc_profile_name"`
}

func TestPackagedOIDC_BrowserFixture(t *testing.T) {
	// Given
	handoffPath := os.Getenv("SILO_OIDC_BROWSER_HANDOFF")
	stopPath := os.Getenv("SILO_OIDC_BROWSER_STOP")
	if handoffPath == "" || stopPath == "" {
		t.Fatal("SILO_OIDC_BROWSER_HANDOFF and SILO_OIDC_BROWSER_STOP are required")
	}
	harness := newPackagedOIDCHarness(t)
	result := harness.start(t)
	controls := newBrowserFixtureServer(t)
	handoff := harness.browserHandoff(t, result.providerID, controls.control())
	writeBrowserHandoff(t, handoffPath, handoff)

	// When
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ticker := time.NewTicker(browserFixturePollInterval)
	defer ticker.Stop()
	for {
		select {
		case command := <-controls.commands:
			handoff = harness.applyBrowserControl(t, command, handoff)
			command.result <- browserControlResult{handoff: handoff}
		case err := <-controls.errors:
			t.Fatalf("browser fixture control server: %v", err)
		case <-signals:
			return
		case <-ticker.C:
			if browserFixtureShouldStop(stopPath, os.Getenv("SILO_OIDC_BROWSER_PARENT_PID")) {
				return
			}
		}
	}
}

func (h *packagedOIDCHarness) browserHandoff(t *testing.T, providerID string, control browserFixtureControl) browserFixtureHandoff {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	response := h.admin.request(t, ctx, http.MethodGet, "/api/v1/auth/providers", "", nil, "")
	defer response.Body.Close()
	var providers []browserFixtureProvider
	decodeJSON(t, response.Body, &providers)
	for _, provider := range providers {
		if provider.ID == providerID {
			return h.browserHandoffForProvider(t, provider, control)
		}
	}
	t.Fatalf("browser fixture provider %q is absent", providerID)
	return browserFixtureHandoff{}
}

func (h *packagedOIDCHarness) applyBrowserControl(t *testing.T, command browserControlCommand, current browserFixtureHandoff) browserFixtureHandoff {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	switch command.kind {
	case browserControlMode:
		if command.mode != fakeOIDCHappy && command.mode != fakeOIDCWrongNonce && command.mode != fakeOIDCWrongSignature && command.mode != fakeOIDCFailure {
			t.Fatalf("unsupported browser OIDC mode %q", command.mode)
		}
		h.idp.setMode(command.mode)
		return h.browserHandoff(t, current.Provider.ID, current.control())
	case browserControlProvider:
		h.admin.setInstallationEnabled(t, ctx, h.installID, command.enabled)
		h.restart(t, ctx, h.idp.caFile)
		if command.enabled {
			h.assertProviderAndRedirect(t, ctx, h.process, h.installID)
			return h.browserHandoff(t, current.Provider.ID, current.control())
		} else {
			h.assertProviderAbsent(t, ctx)
			return h.browserHandoffForProvider(t, current.Provider, current.control())
		}
	case browserControlPresentation:
		h.setBrowserPresentation(t, ctx, command.presentation)
		h.restart(t, ctx, h.idp.caFile)
		return h.browserHandoff(t, current.Provider.ID, current.control())
	default:
		t.Fatalf("unsupported browser control kind %q", command.kind)
	}
	return browserFixtureHandoff{}
}

func (h *packagedOIDCHarness) browserHandoffForProvider(t *testing.T, provider browserFixtureProvider, control browserFixtureControl) browserFixtureHandoff {
	t.Helper()
	ctx, cancel := contextWithTimeout(t)
	defer cancel()
	branding := h.browserBranding(t, ctx)
	return browserFixtureHandoff{
		SiloURL: h.process.baseURL, ControlURL: control.url, ControlToken: control.token, Provider: provider,
		Branding:        branding,
		LocalAccount:    browserFixtureAccount{Username: breakGlassUsername, Password: breakGlassPassword},
		OIDCProfileName: "oidc_user",
	}
}

func (h *packagedOIDCHarness) browserBranding(t *testing.T, ctx context.Context) browserFixtureBranding {
	t.Helper()
	response := h.admin.request(t, ctx, http.MethodGet, "/api/v1/theme/branding", "", nil, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("read browser fixture branding: status %d: %s", response.StatusCode, responseBody(t, response))
	}
	var branding browserFixtureBranding
	decodeJSON(t, response.Body, &branding)
	return branding
}

func (h *packagedOIDCHarness) setBrowserPresentation(t *testing.T, ctx context.Context, presentation browserFixturePresentation) {
	t.Helper()
	response := h.admin.requestJSON(t, ctx, http.MethodPut, "/api/v1/admin/settings", h.admin.token, map[string]any{
		"values": map[string]string{
			"branding.server_name":    presentation.ServerName,
			"branding.login_subtitle": presentation.LoginSubtitle,
		},
	})
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("set browser fixture branding: status %d: %s", response.StatusCode, responseBody(t, response))
	}
	displayName := base64.StdEncoding.EncodeToString([]byte(presentation.ProviderDisplayName))
	query := fmt.Sprintf(
		`INSERT INTO plugin_runtime_configs (plugin_installation_id, config_key, config_value)
		 VALUES (%d, 'display_name', jsonb_build_object('value', convert_from(decode('%s', 'base64'), 'UTF8')))
		 ON CONFLICT (plugin_installation_id, config_key)
		 DO UPDATE SET config_value = EXCLUDED.config_value, updated_at = NOW()`,
		h.installID,
		displayName,
	)
	runCommand(
		t,
		ctx,
		h.buildRoot,
		"docker",
		"exec",
		h.resources.postgres,
		"psql",
		"-U",
		"silo",
		"-d",
		"silo",
		"-v",
		"ON_ERROR_STOP=1",
		"-c",
		query,
	)
}

func (h browserFixtureHandoff) control() browserFixtureControl {
	return browserFixtureControl{url: h.ControlURL, token: h.ControlToken}
}

func writeBrowserHandoff(t *testing.T, destination string, handoff browserFixtureHandoff) {
	t.Helper()
	data, err := json.Marshal(handoff)
	if err != nil {
		t.Fatalf("encode browser handoff: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatalf("create browser handoff directory: %v", err)
	}
	temporary := destination + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		t.Fatalf("write browser handoff: %v", err)
	}
	if err := os.Rename(temporary, destination); err != nil {
		t.Fatalf("publish browser handoff: %v", err)
	}
}

func browserFixtureShouldStop(stopPath, parentPID string) bool {
	if _, err := os.Stat(stopPath); err == nil {
		return true
	}
	pid, err := strconv.Atoi(parentPID)
	if err != nil || pid <= 0 {
		return false
	}
	err = syscall.Kill(pid, syscall.Signal(0))
	return errors.Is(err, syscall.ESRCH)
}
