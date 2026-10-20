package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

// testSecret is a non-secret fixture value; tests assert it never appears in
// an error message.
var testSecret = strings.Repeat("test-only-", 4)

func envOf(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func validWeb3Env() map[string]string {
	return map[string]string{
		"WEB3_JWT_ISSUER": "wallet-trade-test", "WEB3_JWT_AUDIENCE": "wallet-trade-web3-test",
		"WEB3_JWT_KEY_ID": "k1", "WEB3_JWT_SECRET": testSecret, "WEB3_AUTH_EPOCH": "3",
	}
}

func TestDeploymentProfileRejectsUnknownEnvironments(t *testing.T) {
	for _, env := range []string{"", "prod", "Production", "dev", "staging ", "local"} {
		if _, err := resolveDeploymentProfile(envOf(map[string]string{"NODE_ENV": env}), "127.0.0.1:8080"); err == nil {
			t.Fatalf("NODE_ENV=%q must refuse to start", env)
		}
	}
	for _, tc := range []struct {
		env, listen string
		localTest   bool
	}{
		{"development", "127.0.0.1:8080", true},
		{"test", "localhost:9000", true},
		{"test", "[::1]:8080", true},
		{"development", ":8080", false},
		{"development", "0.0.0.0:8080", false},
		{"test", "192.0.2.5:8080", false},
		{"test", "not-an-address", false},
		{"staging", "127.0.0.1:8080", false},
		{"production", "127.0.0.1:8080", false},
	} {
		profile, err := resolveDeploymentProfile(envOf(map[string]string{"NODE_ENV": tc.env}), tc.listen)
		if err != nil {
			t.Fatalf("%s %s: %v", tc.env, tc.listen, err)
		}
		if profile.localTest() != tc.localTest {
			t.Fatalf("%s on %s: localTest=%v, want %v", tc.env, tc.listen, profile.localTest(), tc.localTest)
		}
	}
}

func TestWeb3CredentialPolicyAcceptsACompleteConfiguration(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	env := validWeb3Env()
	env["WEB3_JWT_PREVIOUS_KEY_ID"] = "k0"
	env["WEB3_JWT_PREVIOUS_SECRET"] = strings.Repeat("p", 40)
	env["WEB3_JWT_PREVIOUS_RETIRE_AT"] = "2026-10-10T00:00:00Z"
	env["WEB3_JWT_LEEWAY_SECONDS"] = "10"
	policy, err := web3CredentialPolicy(envOf(env), now)
	if err != nil {
		t.Fatal(err)
	}
	if policy.Issuer != "wallet-trade-test" || policy.Audience != "wallet-trade-web3-test" || policy.Current.ID != "k1" || policy.Epoch != 3 || policy.Leeway != 10*time.Second {
		t.Fatalf("policy %+v", policy)
	}
	if len(policy.Previous) != 1 || policy.Previous[0].ID != "k0" || !policy.Previous[0].Retire.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("previous key %+v", policy.Previous)
	}
	if without, err := web3CredentialPolicy(envOf(validWeb3Env()), now); err != nil || without.Leeway != defaultJWTLeeway || len(without.Previous) != 0 {
		t.Fatalf("defaults: %+v %v", without, err)
	}
}

func TestWeb3CredentialPolicyRefusesUnsafeConfiguration(t *testing.T) {
	now := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	cases := map[string]func(map[string]string){
		"missing issuer":        func(e map[string]string) { delete(e, "WEB3_JWT_ISSUER") },
		"missing audience":      func(e map[string]string) { e["WEB3_JWT_AUDIENCE"] = " " },
		"missing key id":        func(e map[string]string) { delete(e, "WEB3_JWT_KEY_ID") },
		"missing secret":        func(e map[string]string) { delete(e, "WEB3_JWT_SECRET") },
		"missing epoch":         func(e map[string]string) { delete(e, "WEB3_AUTH_EPOCH") },
		"zero epoch":            func(e map[string]string) { e["WEB3_AUTH_EPOCH"] = "0" },
		"negative epoch":        func(e map[string]string) { e["WEB3_AUTH_EPOCH"] = "-2" },
		"non-numeric epoch":     func(e map[string]string) { e["WEB3_AUTH_EPOCH"] = "two" },
		"shared with user-auth": func(e map[string]string) { e["JWT_SECRET"] = testSecret },
		"shared with refresh":   func(e map[string]string) { e["JWT_REFRESH_SECRET"] = testSecret },
		"leeway too large":      func(e map[string]string) { e["WEB3_JWT_LEEWAY_SECONDS"] = "301" },
		"leeway negative":       func(e map[string]string) { e["WEB3_JWT_LEEWAY_SECONDS"] = "-1" },
		"partial previous key":  func(e map[string]string) { e["WEB3_JWT_PREVIOUS_KEY_ID"] = "k0" },
		"previous without retire": func(e map[string]string) {
			e["WEB3_JWT_PREVIOUS_KEY_ID"], e["WEB3_JWT_PREVIOUS_SECRET"] = "k0", strings.Repeat("p", 40)
		},
		"previous retire unparsable": func(e map[string]string) {
			e["WEB3_JWT_PREVIOUS_KEY_ID"], e["WEB3_JWT_PREVIOUS_SECRET"], e["WEB3_JWT_PREVIOUS_RETIRE_AT"] = "k0", strings.Repeat("p", 40), "tomorrow"
		},
		"previous retire beyond 30 days": func(e map[string]string) {
			e["WEB3_JWT_PREVIOUS_KEY_ID"], e["WEB3_JWT_PREVIOUS_SECRET"], e["WEB3_JWT_PREVIOUS_RETIRE_AT"] = "k0", strings.Repeat("p", 40), "2026-11-09T00:00:01Z"
		},
		"previous equals current": func(e map[string]string) {
			e["WEB3_JWT_PREVIOUS_KEY_ID"], e["WEB3_JWT_PREVIOUS_SECRET"], e["WEB3_JWT_PREVIOUS_RETIRE_AT"] = "k0", testSecret, "2026-10-10T00:00:00Z"
		},
	}
	for name, mutate := range cases {
		env := validWeb3Env()
		mutate(env)
		_, err := web3CredentialPolicy(envOf(env), now)
		if err == nil {
			t.Fatalf("%s: accepted", name)
		}
		if strings.Contains(err.Error(), testSecret) || strings.Contains(err.Error(), strings.Repeat("p", 40)) {
			t.Fatalf("%s: error leaks a secret: %v", name, err)
		}
	}
}

// Without a database the credentials still validate (so a misconfiguration
// fails at startup), but no session store exists: sign-in and every private
// route answer 503 rather than trusting unchecked tokens.
func TestBuildWeb3CredentialsWithoutADatabase(t *testing.T) {
	creds, err := buildWeb3Credentials(context.Background(), envOf(validWeb3Env()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if creds.verifier == nil || creds.sessions != nil || creds.adminsGranted {
		t.Fatalf("credentials %+v", creds)
	}
	short := validWeb3Env()
	short["WEB3_JWT_SECRET"] = "too-short-secret"
	if _, err := buildWeb3Credentials(context.Background(), envOf(short), nil); err == nil || strings.Contains(err.Error(), "too-short-secret") {
		t.Fatalf("a short secret must refuse to start without echoing it: %v", err)
	}
	badKey := validWeb3Env()
	badKey["WEB3_JWT_KEY_ID"] = "has spaces"
	if _, err := buildWeb3Credentials(context.Background(), envOf(badKey), nil); err == nil {
		t.Fatal("a malformed key id must refuse to start")
	}
}

func TestAdminGrantFollowsTheAuthEpoch(t *testing.T) {
	for _, tc := range []struct {
		grant   string
		granted bool
		fails   bool
	}{{"", false, false}, {"3", true, false}, {"2", false, false}, {"4", false, false}, {"x", false, true}, {"0", false, true}} {
		granted, err := adminGrantCurrent(envOf(map[string]string{"WEB3_ADMIN_GRANT_EPOCH": tc.grant}), 3)
		if (err != nil) != tc.fails || granted != tc.granted {
			t.Fatalf("grant %q: granted=%v err=%v", tc.grant, granted, err)
		}
	}
	if len(i18nAdmins(false)) != 0 {
		t.Fatal("ungranted i18n allowlist must be empty")
	}
}
