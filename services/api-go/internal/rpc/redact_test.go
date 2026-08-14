package rpc

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"testing"
)

func TestRedactTransportErr_StripsURLAndKey(t *testing.T) {
	secret := "https://sepolia.infura.io/v3/SUPERSECRETKEY"
	ue := &url.Error{Op: "Post", URL: secret, Err: context.Canceled}

	got := redactTransportErr(ue)
	if strings.Contains(got.Error(), "SUPERSECRETKEY") || strings.Contains(got.Error(), secret) {
		t.Fatalf("redacted error still leaks the URL/key: %q", got.Error())
	}
	if !errors.Is(got, context.Canceled) {
		t.Errorf("inner cause must be preserved (errors.Is context.Canceled): %v", got)
	}
}

func TestRedactTransportErr_PassThrough(t *testing.T) {
	base := errors.New("rpc: marshal: boom")
	if got := redactTransportErr(base); got != base {
		t.Errorf("a non-url.Error must pass through unchanged, got %v", got)
	}
}

func TestRedactURL(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://sepolia.infura.io/v3/SECRETKEY", "https://sepolia.infura.io/<redacted>"},
		{"https://eth-sepolia.g.alchemy.com/v2/KEY123", "https://eth-sepolia.g.alchemy.com/<redacted>"},
		{"wss://sepolia.infura.io/ws/v3/WSKEY", "wss://sepolia.infura.io/<redacted>"},
		{"", ""},
		{"garbage", "<redacted-url>"},
	}
	for _, c := range cases {
		got := RedactURL(c.in)
		if got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
		for _, leak := range []string{"SECRETKEY", "KEY123", "WSKEY"} {
			if strings.Contains(got, leak) {
				t.Errorf("RedactURL(%q) leaked %q: %q", c.in, leak, got)
			}
		}
	}
}
