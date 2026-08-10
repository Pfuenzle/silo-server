package auth

import "testing"

func TestIsDuplicateEmail_UsesOnlyKnownEmailConstraints(t *testing.T) {
	tests := []struct {
		name       string
		constraint string
		want       bool
	}{
		{name: "email", constraint: "users_email_key", want: true},
		{name: "username", constraint: "users_username_key", want: false},
		{name: "unknown email-like", constraint: "unexpected_emailish_constraint", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Given
			err := &DuplicateUserError{Constraint: tt.constraint}

			// When
			got := IsDuplicateEmail(err)

			// Then
			if got != tt.want {
				t.Fatalf("IsDuplicateEmail(%q) = %t, want %t", tt.constraint, got, tt.want)
			}
		})
	}
}
