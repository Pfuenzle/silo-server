package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/Silo-Server/silo-server/internal/clientip"
)

const oauthBrowserCookiePrefix = "silo_oauth_"

func setOAuthBrowserBinding(w http.ResponseWriter, hostBaseURL string, installID int, nonce string, ttl time.Duration) {
	http.SetCookie(w, oauthBrowserCookie(hostBaseURL, installID, nonce, nonce, int(ttl.Seconds())))
}

func hasOAuthBrowserBinding(r *http.Request, nonce string) bool {
	cookie, err := r.Cookie(oauthBrowserCookiePrefix + nonce)
	return err == nil && subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(nonce)) == 1
}

func clearOAuthBrowserBinding(w http.ResponseWriter, hostBaseURL string, installID int, nonce string) {
	cookie := oauthBrowserCookie(hostBaseURL, installID, nonce, "", -1)
	cookie.Expires = time.Unix(1, 0)
	http.SetCookie(w, cookie)
}

func oauthBrowserCookie(hostBaseURL string, installID int, nonce, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name:     oauthBrowserCookiePrefix + nonce,
		Value:    value,
		Path:     "/api/v1/auth/oauth/" + strconv.Itoa(installID) + "/callback",
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   strings.HasPrefix(strings.ToLower(hostBaseURL), "https://"),
		SameSite: http.SameSiteLaxMode,
	}
}

func redirectOAuthCallback(w http.ResponseWriter, r *http.Request, target string) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, target, http.StatusFound)
}

func redirectOAuthFailure(w http.ResponseWriter, r *http.Request, trustedNext string) {
	values := url.Values{}
	values.Set("error", "oauth_failed")
	if trustedNext != "" {
		values.Set("redirect", normalizeOAuthNext(trustedNext))
	}
	redirectOAuthCallback(w, r, "/login?"+values.Encode())
}

func normalizeOAuthNext(next string) string {
	next = strings.TrimSpace(next)
	decoded, err := url.PathUnescape(next)
	if err != nil || decoded == "" || !strings.HasPrefix(decoded, "/") || strings.HasPrefix(decoded, "//") {
		return "/"
	}
	for _, character := range decoded {
		if character == '\\' || character <= 31 || character == 127 {
			return "/"
		}
	}
	return next
}

func clientIP(r *http.Request) string {
	if ip := strings.TrimSpace(clientip.FromContext(r.Context())); ip != "" {
		return ip
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return strings.TrimSpace(host)
	}
	return strings.Trim(strings.TrimSpace(r.RemoteAddr), "[]")
}

func randomHex(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func setCorrelationIDHeader(w http.ResponseWriter, r *http.Request) {
	if reqID := chimw.GetReqID(r.Context()); reqID != "" {
		w.Header().Set("X-Request-Id", reqID)
	}
}
