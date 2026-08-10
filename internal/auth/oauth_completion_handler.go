package auth

import (
	"encoding/json"
	"net/http"
	"strings"
)

type OAuthCompleteRequest struct {
	Code string `json:"code"`
}

type OAuthCompleteResponse struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
	NextURL      string `json:"next"`
}

func (h *OAuthHandler) HandleComplete(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h.deps.CompletionStore == nil {
		setCorrelationIDHeader(w, r)
		http.Error(w, "oauth completion unavailable", http.StatusServiceUnavailable)
		return
	}
	var req OAuthCompleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		setCorrelationIDHeader(w, r)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		setCorrelationIDHeader(w, r)
		http.Error(w, "code required", http.StatusBadRequest)
		return
	}
	completion, err := h.deps.CompletionStore.GetAndDeleteCompletion(r.Context(), code)
	if err != nil {
		setCorrelationIDHeader(w, r)
		http.Error(w, "invalid or expired completion code", http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(OAuthCompleteResponse{
		AccessToken:  completion.AccessToken,
		RefreshToken: completion.RefreshToken,
		ExpiresIn:    completion.ExpiresIn,
		NextURL:      completion.NextURL,
	})
}
