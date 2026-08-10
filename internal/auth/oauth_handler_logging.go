package auth

import (
	"context"
	"time"
)

func logOAuthFailure(ctx context.Context, installationID int, reason AuthFailureReason, status int, start time.Time) {
	logOAuthFailureWithDiag(ctx, installationID, reason, status, start)
}

func logOAuthFailureWithDiag(ctx context.Context, installationID int, reason AuthFailureReason, status int, start time.Time) {
	diag := NewOAuthFailureDiagnostic(ctx, reason, status, start, installationID, "")
	LogAuthFailure(diag)
}
