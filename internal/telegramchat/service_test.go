package telegramchat

import (
	"errors"
	"testing"
)

func TestIsInvalidSessionError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "unregistered", err: errors.New("rpc error 401: AUTH_KEY_UNREGISTERED"), want: true},
		{name: "invalid", err: errors.New("AUTH_KEY_INVALID"), want: true},
		{name: "revoked", err: errors.New("SESSION_REVOKED"), want: true},
		{name: "deactivated", err: errors.New("USER_DEACTIVATED"), want: true},
		{name: "network error", err: errors.New("connection reset"), want: false},
		{name: "nil", err: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isInvalidSessionError(test.err); got != test.want {
				t.Fatalf("isInvalidSessionError(%v) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}
