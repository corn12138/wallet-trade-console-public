// Package runtimeenv ports the database env contract from
// packages/database/src/database-runtime-env.ts so the Go service consumes the
// exact same vars as the existing NestJS API.
//
// Contract (in order of precedence):
//
//  1. DATABASE_URL — full Postgres URL, used as-is.
//  2. Compound vars assembled into:
//     postgresql://{user}:{password}@{host}:{port}/{name}?schema=public[&sslmode=require]
//
// Compound vars:
//
//   - DATABASE_HOST            (required)
//   - DATABASE_PORT            (default "6543" — PgBouncer-style pooled port)
//   - DATABASE_NAME            (required)
//   - DATABASE_USER            (required)
//   - DATABASE_PASSWORD        (one of the password vars required)
//   - DATABASE_PASSWORD_BASE64 (preferred; if set, decoded as base64)
//   - DATABASE_PASSWORD_ENCODING=base64 (alternative: applied to DATABASE_PASSWORD)
//   - DATABASE_SSL=true        (appends sslmode=require)
//
// User and password are URL-encoded; schema is hardcoded to "public".
package runtimeenv

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// DefaultPort matches the NestJS default in database-runtime-env.ts. It is the
// pooled PgBouncer port used by Supabase/managed Postgres, NOT 5432.
const DefaultPort = "6543"

// ErrDatabaseURLMissing is returned when no DATABASE_URL is present and the
// compound vars are insufficient to assemble one.
var ErrDatabaseURLMissing = errors.New("DATABASE_URL is not configured. Provide DATABASE_URL or DATABASE_HOST/DATABASE_PORT/DATABASE_NAME/DATABASE_USER and DATABASE_PASSWORD or DATABASE_PASSWORD_BASE64")

// Resolve returns the database URL according to the contract.
//
// If the empty string is returned along with a nil error, no URL was assembled
// (no env vars present); callers in non-production contexts may treat that as
// "skip connect" the way PrismaService does when PRISMA_SKIP_CONNECT_ON_BOOT is
// set. Callers in production should treat empty-with-nil as an error.
func Resolve() (string, error) {
	if direct := strings.TrimSpace(os.Getenv("DATABASE_URL")); direct != "" {
		return direct, nil
	}

	host := strings.TrimSpace(os.Getenv("DATABASE_HOST"))
	name := strings.TrimSpace(os.Getenv("DATABASE_NAME"))
	user := strings.TrimSpace(os.Getenv("DATABASE_USER"))
	if host == "" || name == "" || user == "" {
		return "", nil
	}

	password, err := resolvePassword()
	if err != nil {
		return "", err
	}
	if password == nil {
		return "", nil
	}

	port := strings.TrimSpace(os.Getenv("DATABASE_PORT"))
	if port == "" {
		port = DefaultPort
	}

	params := url.Values{}
	params.Set("schema", "public")
	if strings.EqualFold(strings.TrimSpace(os.Getenv("DATABASE_SSL")), "true") {
		params.Set("sslmode", "require")
	}

	encodedUser := url.QueryEscape(user)
	encodedPassword := url.QueryEscape(*password)

	return fmt.Sprintf(
		"postgresql://%s:%s@%s:%s/%s?%s",
		encodedUser,
		encodedPassword,
		host,
		port,
		name,
		params.Encode(),
	), nil
}

// MustResolve enforces production semantics: returns an error if no URL could
// be assembled. Mirrors assertDatabaseRuntimeEnvReady's NODE_ENV=production
// branch.
func MustResolve() (string, error) {
	databaseURL, err := Resolve()
	if err != nil {
		return "", err
	}
	if databaseURL == "" {
		return "", ErrDatabaseURLMissing
	}
	return databaseURL, nil
}

// DescribeTarget returns "host:port/dbname" for log lines, matching the
// PrismaService log format. Falls back to a fixed string on parse failure so
// secrets in malformed URLs aren't accidentally logged.
func DescribeTarget(databaseURL string) string {
	parsed, err := url.Parse(databaseURL)
	if err != nil || parsed.Host == "" {
		return "configured DATABASE_URL target"
	}
	host := parsed.Hostname()
	port := parsed.Port()
	if port == "" {
		port = "5432"
	}
	dbName := strings.TrimPrefix(parsed.Path, "/")
	if dbName == "" {
		dbName = "unknown"
	}
	return fmt.Sprintf("%s:%s/%s", host, port, dbName)
}

// resolvePassword mirrors resolveDatabasePassword in the TS source. Returns
// (nil, nil) when no password var is set so callers can distinguish "not
// configured" from "empty string" without juggling sentinel values.
func resolvePassword() (*string, error) {
	if encoded := strings.TrimSpace(os.Getenv("DATABASE_PASSWORD_BASE64")); encoded != "" {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("DATABASE_PASSWORD_BASE64 is not valid base64: %w", err)
		}
		s := string(decoded)
		return &s, nil
	}

	raw, present := os.LookupEnv("DATABASE_PASSWORD")
	if !present {
		return nil, nil
	}
	if strings.TrimSpace(raw) == "" {
		// Match the TS behavior: a present-but-empty DATABASE_PASSWORD is
		// returned as-is rather than triggering base64 decode.
		return &raw, nil
	}

	if strings.EqualFold(strings.TrimSpace(os.Getenv("DATABASE_PASSWORD_ENCODING")), "base64") {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(raw))
		if err != nil {
			return nil, fmt.Errorf("DATABASE_PASSWORD with DATABASE_PASSWORD_ENCODING=base64 is not valid base64: %w", err)
		}
		s := string(decoded)
		return &s, nil
	}

	return &raw, nil
}
