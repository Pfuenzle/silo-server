package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	pluginv1 "github.com/Silo-Server/silo-plugin-sdk/pkg/pluginproto/silo/plugin/v1"
	"github.com/go-chi/chi/v5"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/Silo-Server/silo-server/internal/models"
)

// OAuthClient is the host-side gRPC client surface the OAuth handler needs.
// Defined as an interface so handler tests can substitute a fake.
type OAuthClient interface {
	InitAuthorize(ctx context.Context, req *pluginv1.InitAuthorizeRequest) (*pluginv1.InitAuthorizeResponse, error)
	ExchangeCode(ctx context.Context, req *pluginv1.ExchangeCodeRequest) (*pluginv1.AuthenticateResponse, error)
}

// OAuthLoginCompleter wraps the post-ExchangeCode work: lookup or provision
// the user identified by the AuthenticateResponse, create a session, mint a
// token pair. Defined as an interface so tests can avoid spinning up the
// full auth.Service.
type OAuthLoginCompleter interface {
	CompleteOAuthLogin(ctx context.Context, in OAuthLoginInput) (*TokenPair, *models.User, error)
}

// OAuthLoginInput carries everything the completer needs to issue a session.
type OAuthLoginInput struct {
	InstallationID int
	CapabilityID   string
	Response       *pluginv1.AuthenticateResponse
	LinkingUserID  int // 0 = not linking
	DeviceName     string
	IP             string
}

// OAuthHandlerDeps wires the OAuthHandler. ResolveClient turns the URL's
// installation_id into a plugin gRPC client; Provisioner consumes the
// AuthenticateResponse and produces a session.
type OAuthHandlerDeps struct {
	Store           OAuthStore
	CompletionStore OAuthCompletionStore
	StateSecret     []byte
	ResolveClient   func(ctx context.Context, installationID int) (OAuthClient, string, error) // returns (client, capabilityID, err)
	LoginCompleter  OAuthLoginCompleter
	HostBaseURL     string
	StateTTL        time.Duration
	// FrontendCompletePath is the SPA path the callback redirects to after
	// minting a one-time completion code. The SPA exchanges that code for tokens.
	FrontendCompletePath string
}

// OAuthHandler serves /init and /callback for OAuth-capable auth plugins.
type OAuthHandler struct {
	deps OAuthHandlerDeps
}

func NewOAuthHandler(d OAuthHandlerDeps) *OAuthHandler {
	if d.StateTTL == 0 {
		d.StateTTL = 10 * time.Minute
	}
	if d.FrontendCompletePath == "" {
		d.FrontendCompletePath = "/login/oauth-complete"
	}
	if d.CompletionStore == nil {
		if store, ok := d.Store.(OAuthCompletionStore); ok {
			d.CompletionStore = store
		}
	}
	return &OAuthHandler{deps: d}
}

// ErrMissingInstallID is returned when the URL path has no install_id.
var ErrMissingInstallID = errors.New("install_id required")

// HandleInit serves POST /api/v1/auth/oauth/{install_id}/init.
func (h *OAuthHandler) HandleInit(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	installID, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || installID <= 0 {
		setCorrelationIDHeader(w, r)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	next := normalizeOAuthNext(r.URL.Query().Get("next"))

	client, _, err := h.deps.ResolveClient(r.Context(), installID)
	if err != nil {
		logOAuthFailure(r.Context(), installID, AuthReasonPluginUnavailable, http.StatusBadGateway, start)
		setCorrelationIDHeader(w, r)
		http.Error(w, "service unavailable", http.StatusBadGateway)
		return
	}

	nonce, err := randomHex(16)
	if err != nil {
		logOAuthFailure(r.Context(), installID, AuthReasonInternalError, http.StatusInternalServerError, start)
		setCorrelationIDHeader(w, r)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	now := time.Now().UTC()
	state := SignState(h.deps.StateSecret, StatePayload{
		Nonce:     nonce,
		InstallID: strconv.Itoa(installID),
		ExpiresAt: now.Add(h.deps.StateTTL),
	})
	redirectURI := strings.TrimRight(h.deps.HostBaseURL, "/") + "/api/v1/auth/oauth/" + strconv.Itoa(installID) + "/callback"

	resp, err := client.InitAuthorize(r.Context(), &pluginv1.InitAuthorizeRequest{
		RedirectUri: redirectURI,
		State:       state,
	})
	if err != nil {
		logOAuthFailure(r.Context(), installID, AuthReasonPluginUnavailable, http.StatusBadGateway, start)
		setCorrelationIDHeader(w, r)
		http.Error(w, "service unavailable", http.StatusBadGateway)
		return
	}
	if resp.GetAuthorizeUrl() == "" {
		logOAuthFailure(r.Context(), installID, AuthReasonInternalError, http.StatusBadGateway, start)
		setCorrelationIDHeader(w, r)
		http.Error(w, "service unavailable", http.StatusBadGateway)
		return
	}

	psBytes, _ := json.Marshal(resp.GetProviderState().AsMap())
	sess := OAuthSession{
		State:         state,
		InstallID:     strconv.Itoa(installID),
		RedirectURI:   redirectURI,
		ProviderState: psBytes,
		NextURL:       next,
		ExpiresAt:     now.Add(h.deps.StateTTL),
	}
	if err := h.deps.Store.Insert(r.Context(), sess); err != nil {
		logOAuthFailure(r.Context(), installID, AuthReasonStateStoreUnavailable, http.StatusInternalServerError, start)
		setCorrelationIDHeader(w, r)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}

	setOAuthBrowserBinding(w, h.deps.HostBaseURL, installID, nonce, h.deps.StateTTL)
	http.Redirect(w, r, resp.GetAuthorizeUrl(), http.StatusFound)
}

// HandleCallback serves GET /api/v1/auth/oauth/{install_id}/callback.
func (h *OAuthHandler) HandleCallback(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	// Single diagnostic boundary: collected at failure sites, emitted once on return.
	var (
		diagInstallID int
		diagReason    AuthFailureReason
		diagPending   bool
	)
	defer func() {
		if diagPending {
			logOAuthFailure(r.Context(), diagInstallID, diagReason, StatusForReason(diagReason), start)
		}
	}()

	installID, err := strconv.Atoi(chi.URLParam(r, "install_id"))
	if err != nil || installID <= 0 {
		setCorrelationIDHeader(w, r)
		http.Error(w, "invalid install_id", http.StatusBadRequest)
		return
	}
	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")
	if state == "" || code == "" {
		setCorrelationIDHeader(w, r)
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}

	payload, err := VerifyState(h.deps.StateSecret, state)
	if err != nil {
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, "")
		return
	}
	if payload.InstallID != strconv.Itoa(installID) {
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, "")
		return
	}
	boundToBrowser := hasOAuthBrowserBinding(r, payload.Nonce)
	clearOAuthBrowserBinding(w, h.deps.HostBaseURL, installID, payload.Nonce)
	if !boundToBrowser {
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, "")
		return
	}
	preflight, err := h.deps.Store.Get(r.Context(), state)
	if err != nil {
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, "")
		return
	}
	if preflight.LinkingUserID != "" && preflight.LinkingUserID != "0" {
		diagInstallID = installID
		diagReason = AuthReasonLinkingUnsupported
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, preflight.NextURL)
		return
	}

	sess, err := h.deps.Store.GetAndDelete(r.Context(), state)
	if err != nil {
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, preflight.NextURL)
		return
	}

	client, capabilityID, err := h.deps.ResolveClient(r.Context(), installID)
	if err != nil {
		diagInstallID = installID
		diagReason = AuthReasonPluginUnavailable
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}

	var ps map[string]any
	if err := json.Unmarshal(sess.ProviderState, &ps); err != nil {
		diagInstallID = installID
		diagReason = AuthReasonInternalError
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}
	psStruct, err := structpb.NewStruct(ps)
	if err != nil {
		diagInstallID = installID
		diagReason = AuthReasonInternalError
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}

	resp, err := client.ExchangeCode(r.Context(), &pluginv1.ExchangeCodeRequest{
		Code:          code,
		State:         state,
		RedirectUri:   sess.RedirectURI,
		ProviderState: psStruct,
	})
	if err != nil {
		diagInstallID = installID
		diagReason = AuthReasonExchangeFailed
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}
	if resp.GetExternalSubject() == "" {
		diagInstallID = installID
		diagReason = AuthReasonInternalError
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}

	linkingUserID := 0
	if sess.LinkingUserID != "" {
		if uid, err := strconv.Atoi(sess.LinkingUserID); err == nil {
			linkingUserID = uid
		}
	}

	pair, _, err := h.deps.LoginCompleter.CompleteOAuthLogin(r.Context(), OAuthLoginInput{
		InstallationID: installID,
		CapabilityID:   capabilityID,
		Response:       resp,
		LinkingUserID:  linkingUserID,
		DeviceName:     r.UserAgent(),
		IP:             clientIP(r),
	})
	if err != nil {
		diagInstallID = installID
		diagReason = ClassifyOAuthFailure(err, "login_completion")
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}

	if h.deps.CompletionStore == nil {
		diagInstallID = installID
		diagReason = AuthReasonCompletionUnavailable
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}
	completionCode, err := randomHex(32)
	if err != nil {
		diagInstallID = installID
		diagReason = AuthReasonInternalError
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, sess.NextURL)
		return
	}
	next := normalizeOAuthNext(sess.NextURL)
	now := time.Now().UTC()
	if err := h.deps.CompletionStore.InsertCompletion(r.Context(), OAuthCompletion{
		Code:         completionCode,
		AccessToken:  pair.AccessToken,
		RefreshToken: pair.RefreshToken,
		ExpiresIn:    pair.ExpiresIn,
		NextURL:      next,
		ExpiresAt:    now.Add(time.Minute),
	}); err != nil {
		diagInstallID = installID
		diagReason = AuthReasonInternalError
		diagPending = true
		setCorrelationIDHeader(w, r)
		redirectOAuthFailure(w, r, next)
		return
	}

	values := url.Values{}
	values.Set("code", completionCode)
	values.Set("next", next)
	completeURL := strings.TrimRight(h.deps.HostBaseURL, "/") + h.deps.FrontendCompletePath + "?" + values.Encode()
	redirectOAuthCallback(w, r, completeURL)
}
