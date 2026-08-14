package runtimeenv

import (
	"encoding/base64"
	"strings"
	"testing"
)

// clearDatabaseEnv wipes every var the resolver touches so a test case starts
// from a known-empty state regardless of what the developer has in their
// shell.
func clearDatabaseEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"DATABASE_URL",
		"DATABASE_HOST",
		"DATABASE_PORT",
		"DATABASE_NAME",
		"DATABASE_USER",
		"DATABASE_PASSWORD",
		"DATABASE_PASSWORD_BASE64",
		"DATABASE_PASSWORD_ENCODING",
		"DATABASE_SSL",
	} {
		t.Setenv(key, "")
	}
}

func TestResolve_DirectURLTakesPrecedence(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_URL", "postgresql://user:pw@db.example:5432/app?schema=public")
	// Compound vars set to wrong values — Resolve must ignore them.
	t.Setenv("DATABASE_HOST", "wrong")
	t.Setenv("DATABASE_USER", "wrong")
	t.Setenv("DATABASE_NAME", "wrong")
	t.Setenv("DATABASE_PASSWORD", "wrong")

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != "postgresql://user:pw@db.example:5432/app?schema=public" {
		t.Errorf("expected direct URL to win, got %q", got)
	}
}

func TestResolve_CompoundVarsAssemble(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_HOST", "db.example")
	t.Setenv("DATABASE_NAME", "wallet")
	t.Setenv("DATABASE_USER", "app")
	t.Setenv("DATABASE_PASSWORD", "s3cret")

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	want := "postgresql://app:s3cret@db.example:6543/wallet?schema=public"
	if got != want {
		t.Errorf("assembly mismatch:\n  got  %q\n  want %q", got, want)
	}
}

func TestResolve_CustomPort(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_HOST", "db.example")
	t.Setenv("DATABASE_PORT", "5432")
	t.Setenv("DATABASE_NAME", "wallet")
	t.Setenv("DATABASE_USER", "app")
	t.Setenv("DATABASE_PASSWORD", "s3cret")

	got, _ := Resolve()
	if !strings.Contains(got, ":5432/") {
		t.Errorf("expected custom port 5432, got %q", got)
	}
}

func TestResolve_SSLAppendsSslmode(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_HOST", "db.example")
	t.Setenv("DATABASE_NAME", "wallet")
	t.Setenv("DATABASE_USER", "app")
	t.Setenv("DATABASE_PASSWORD", "s3cret")
	t.Setenv("DATABASE_SSL", "true")

	got, _ := Resolve()
	if !strings.Contains(got, "sslmode=require") {
		t.Errorf("expected sslmode=require, got %q", got)
	}
}

func TestResolve_URLEncodesUserAndPassword(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_HOST", "db.example")
	t.Setenv("DATABASE_NAME", "wallet")
	t.Setenv("DATABASE_USER", "user@org")
	t.Setenv("DATABASE_PASSWORD", "p@ss/word!")

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if !strings.Contains(got, "user%40org") {
		t.Errorf("expected user '@' encoded, got %q", got)
	}
	if !strings.Contains(got, "p%40ss%2Fword%21") {
		t.Errorf("expected password specials encoded, got %q", got)
	}
}

func TestResolve_PasswordBase64Decoded(t *testing.T) {
	clearDatabaseEnv(t)
	encoded := base64.StdEncoding.EncodeToString([]byte("s3cret"))
	t.Setenv("DATABASE_HOST", "db.example")
	t.Setenv("DATABASE_NAME", "wallet")
	t.Setenv("DATABASE_USER", "app")
	t.Setenv("DATABASE_PASSWORD_BASE64", encoded)

	got, _ := Resolve()
	if !strings.Contains(got, ":s3cret@") {
		t.Errorf("expected decoded password 's3cret', got %q", got)
	}
}

func TestResolve_PasswordEncodingBase64Flag(t *testing.T) {
	clearDatabaseEnv(t)
	encoded := base64.StdEncoding.EncodeToString([]byte("s3cret"))
	t.Setenv("DATABASE_HOST", "db.example")
	t.Setenv("DATABASE_NAME", "wallet")
	t.Setenv("DATABASE_USER", "app")
	t.Setenv("DATABASE_PASSWORD", encoded)
	t.Setenv("DATABASE_PASSWORD_ENCODING", "base64")

	got, _ := Resolve()
	if !strings.Contains(got, ":s3cret@") {
		t.Errorf("expected decoded password 's3cret', got %q", got)
	}
}

func TestResolve_EmptyWhenInsufficientVars(t *testing.T) {
	clearDatabaseEnv(t)
	t.Setenv("DATABASE_HOST", "db.example")
	// missing name, user, password

	got, err := Resolve()
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if got != "" {
		t.Errorf("expected empty URL when vars missing, got %q", got)
	}
}

func TestMustResolve_ErrorsWhenEmpty(t *testing.T) {
	clearDatabaseEnv(t)
	_, err := MustResolve()
	if err == nil {
		t.Fatal("expected error from MustResolve with no env, got nil")
	}
}

func TestDescribeTarget(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{
			name: "explicit port",
			url:  "postgresql://u:p@db.example:6543/wallet?schema=public",
			want: "db.example:6543/wallet",
		},
		{
			name: "default port falls back to 5432",
			url:  "postgresql://u:p@db.example/wallet",
			want: "db.example:5432/wallet",
		},
		{
			name: "malformed url returns placeholder",
			url:  "not a url",
			want: "configured DATABASE_URL target",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeTarget(tc.url); got != tc.want {
				t.Errorf("DescribeTarget(%q) = %q, want %q", tc.url, got, tc.want)
			}
		})
	}
}
