import { useEffect, useState } from "react";
import { useNavigate } from "react-router";
import { api, setAccessToken, setRefreshToken } from "@/api/client";
import type { RefreshResponse, User } from "@/api/types";
import { AuthBackground } from "@/components/auth/AuthBackground";
import { OAuthFailure } from "@/components/auth/OAuthFailure";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAuth } from "@/hooks/useAuth";
import { buildLoginRetryHref, sanitizeAuthRedirect } from "@/lib/authRedirect";

type OAuthCompleteResponse = RefreshResponse & {
  next: string;
};

async function completeOAuthCode(code: string): Promise<OAuthCompleteResponse> {
  const res = await fetch("/api/v1/auth/oauth/complete", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ code }),
  });
  if (!res.ok) {
    throw new Error("Sign-in response expired. Please try again.");
  }
  return (await res.json()) as OAuthCompleteResponse;
}

export default function OAuthComplete() {
  const navigate = useNavigate();
  const { completeLogin, resetAuth } = useAuth();
  const [completionRequest] = useState(() => {
    const params = new URLSearchParams(window.location.search);
    return {
      code: params.get("code"),
      next: sanitizeAuthRedirect(params.get("next")),
    } as const;
  });
  const [retryNext, setRetryNext] = useState(completionRequest.next);
  const [failed, setFailed] = useState(() => !completionRequest.code);

  useEffect(() => {
    const code = completionRequest.code;
    if (!code) {
      resetAuth();
      return;
    }
    window.history.replaceState(null, "", window.location.pathname);

    let cancelled = false;
    let tokensPersisted = false;
    let loginCompleted = false;
    (async () => {
      try {
        const tokens = await completeOAuthCode(code);
        if (cancelled) return;
        const next = sanitizeAuthRedirect(tokens.next) || "/";
        setRetryNext(next);
        setAccessToken(tokens.access_token);
        setRefreshToken(tokens.refresh_token);
        tokensPersisted = true;
        const user = await api<User>("/auth/me");
        if (cancelled) return;
        completeLogin({
          access_token: tokens.access_token,
          refresh_token: tokens.refresh_token,
          expires_in: tokens.expires_in,
          user,
        });
        loginCompleted = true;
        navigate(next, { replace: true });
      } catch {
        if (!cancelled) {
          resetAuth();
          setFailed(true);
        }
      }
    })();
    return () => {
      cancelled = true;
      if (tokensPersisted && !loginCompleted) {
        resetAuth();
      }
    };
  }, [completeLogin, completionRequest.code, navigate, resetAuth]);

  if (failed) {
    return (
      <main className="auth-shell">
        <AuthBackground />
        <Card className="auth-card glass panel-border w-full max-w-md border-0">
          <CardHeader>
            <CardTitle className="text-3xl font-extrabold tracking-[-0.04em]">Sign in</CardTitle>
          </CardHeader>
          <CardContent>
            <OAuthFailure
              onRetry={() => navigate(buildLoginRetryHref(retryNext), { replace: true })}
            />
          </CardContent>
        </Card>
      </main>
    );
  }

  return (
    <main className="auth-shell">
      <AuthBackground />
      <div
        className="border-primary h-8 w-8 animate-spin rounded-full border-b-2"
        role="status"
        aria-label="Completing sign in"
      />
    </main>
  );
}
