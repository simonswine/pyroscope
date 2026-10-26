package main

import (
	"errors"
	"testing"
)

func TestRetryBootstrapConnection(t *testing.T) {
	for _, tt := range []struct {
		message string
		retry   bool
	}{
		{"bootstrap greeting: EOF (ssh: connect to host 35.17.0.1 port 22: Connection timed out)", true},
		{"bootstrap greeting: EOF (ssh: connect to host 35.17.0.1 port 22: Connection refused)", true},
		{"bootstrap greeting: EOF (Host key verification failed.)", false},
		{"bootstrap greeting: EOF (sudo: a password is required)", false},
		{"node identity mismatch", false},
	} {
		if got := retryBootstrapConnection(errors.New(tt.message)); got != tt.retry {
			t.Errorf("%q: got retry=%v, want %v", tt.message, got, tt.retry)
		}
	}
}
