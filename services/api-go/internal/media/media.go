// Package media is the shared upload/pinning service used by Create Token
// (icon/banner) and NFT Studio (artwork + ERC-721 metadata JSON). It exists so
// selected files are actually stored somewhere durable and the returned URL is
// what goes into contract args and DB rows — never a generated placeholder.
//
// Providers:
//   - local: writes under MEDIA_LOCAL_DIR and serves the bytes back at
//     GET /api/media/files/{key}. Real durable storage for dev/self-hosted
//     deploys (point the dir at a persistent volume in production).
//   - s3:    S3-compatible object storage (AWS S3 / Cloudflare R2 / MinIO)
//     via SigV4 PUT, configured entirely from env. Public reads come from
//     MEDIA_S3_PUBLIC_BASE_URL (a public bucket or CDN in front of it).
//
// When nothing is configured in production the service is DISABLED and the
// upload endpoints answer 503 with a human-readable reason — uploads must
// never silently pretend to succeed.
package media

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StoredMedia describes one stored object. URL may be API-relative (local
// provider without MEDIA_PUBLIC_BASE_URL) — the HTTP handler absolutizes it
// from the request before responding.
type StoredMedia struct {
	Key         string `json:"key"`
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
	Size        int64  `json:"size"`
	Provider    string `json:"provider"`
}

// Provider stores immutable, content-addressed blobs.
type Provider interface {
	Store(ctx context.Context, key, contentType string, data []byte) (StoredMedia, error)
	Name() string
}

// ErrNotConfigured is surfaced as a 503 by the handlers.
var ErrNotConfigured = errors.New("media storage is not configured (set MEDIA_STORAGE=local with MEDIA_LOCAL_DIR, or MEDIA_STORAGE=s3 with MEDIA_S3_* env vars)")

// extByContentType maps the accepted upload types to their stored extension.
// This doubles as the image allow-list: anything not in here is rejected.
var extByContentType = map[string]string{
	"image/png":        "png",
	"image/jpeg":       "jpg",
	"image/webp":       "webp",
	"image/gif":        "gif",
	"application/json": "json",
}

// ContentKey derives the content-addressed object key: sha256 prefix + the
// canonical extension. Same bytes → same key, so replayed uploads are
// idempotent instead of accumulating duplicates.
func ContentKey(data []byte, contentType string) (string, error) {
	ext, ok := extByContentType[contentType]
	if !ok {
		return "", fmt.Errorf("media: unsupported content type %q", contentType)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:16]) + "." + ext, nil
}

// LocalProvider persists blobs under Dir. The served URL is
// {PublicBaseURL}/api/media/files/{key}; with an empty PublicBaseURL the URL
// stays relative and the handler prefixes the request origin.
type LocalProvider struct {
	Dir           string
	PublicBaseURL string
}

// NewLocalProvider ensures the directory exists.
func NewLocalProvider(dir, publicBaseURL string) (*LocalProvider, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("media: local provider needs a directory")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("media: create local dir %s: %w", dir, err)
	}
	return &LocalProvider{Dir: dir, PublicBaseURL: strings.TrimRight(publicBaseURL, "/")}, nil
}

// Name identifies the provider in responses/logs.
func (p *LocalProvider) Name() string { return "local" }

// Store writes the blob (write-once: an existing key is left untouched, the
// bytes are identical by construction of ContentKey).
func (p *LocalProvider) Store(_ context.Context, key, contentType string, data []byte) (StoredMedia, error) {
	if p == nil {
		return StoredMedia{}, ErrNotConfigured
	}
	clean, err := SafeKey(key)
	if err != nil {
		return StoredMedia{}, err
	}
	path := filepath.Join(p.Dir, clean)
	if _, statErr := os.Stat(path); statErr != nil {
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return StoredMedia{}, fmt.Errorf("media: write %s: %w", path, err)
		}
	}
	return StoredMedia{
		Key:         clean,
		URL:         p.PublicBaseURL + FilesRoutePrefix + clean,
		ContentType: contentType,
		Size:        int64(len(data)),
		Provider:    p.Name(),
	}, nil
}

// Open returns the stored file path for serving, guarding traversal.
func (p *LocalProvider) Open(key string) (string, error) {
	clean, err := SafeKey(key)
	if err != nil {
		return "", err
	}
	return filepath.Join(p.Dir, clean), nil
}

// FilesRoutePrefix is where the local provider's blobs are served from
// (mounted under /api/media in httpx).
const FilesRoutePrefix = "/api/media/files/"

// SafeKey rejects anything but the flat content-addressed names this package
// generates, so a crafted key can never traverse out of the storage dir.
func SafeKey(key string) (string, error) {
	if key == "" || key != filepath.Base(key) || strings.Contains(key, "..") {
		return "", fmt.Errorf("media: invalid key %q", key)
	}
	for _, c := range key {
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '.' || c == '-':
		default:
			return "", fmt.Errorf("media: invalid key %q", key)
		}
	}
	return key, nil
}

// FromEnv builds the configured provider. Returns (nil, nil) when media
// storage is intentionally disabled — callers mount the routes anyway so the
// API answers 503 with ErrNotConfigured instead of 404.
func FromEnv() (Provider, error) {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("MEDIA_STORAGE")))
	switch mode {
	case "s3":
		return NewS3ProviderFromEnv()
	case "local":
		dir := os.Getenv("MEDIA_LOCAL_DIR")
		if dir == "" {
			dir = defaultLocalDir()
		}
		return NewLocalProvider(dir, os.Getenv("MEDIA_PUBLIC_BASE_URL"))
	case "disabled", "off", "none":
		return nil, nil
	case "":
		// Auto: explicit dir wins; otherwise default to local storage in
		// non-production only. A production deploy must opt in deliberately
		// (persistent volume or S3) — container-local files silently vanishing
		// on redeploy is exactly the kind of fake durability this task bans.
		if dir := os.Getenv("MEDIA_LOCAL_DIR"); dir != "" {
			return NewLocalProvider(dir, os.Getenv("MEDIA_PUBLIC_BASE_URL"))
		}
		if os.Getenv("NODE_ENV") == "production" {
			return nil, nil
		}
		return NewLocalProvider(defaultLocalDir(), os.Getenv("MEDIA_PUBLIC_BASE_URL"))
	default:
		return nil, fmt.Errorf("media: unknown MEDIA_STORAGE %q (want local|s3|disabled)", mode)
	}
}

func defaultLocalDir() string {
	return filepath.Join("data", "media-uploads")
}
