// Package livekit is the Go port of legacy NestJS livekit/. It
// issues LiveKit-shaped access tokens (HS256 JWT signed with the
// API secret) plus exposes the WebSocket URL the frontend dials.
//
// We hand-roll the token instead of pulling in livekit-server-sdk-go
// because the JWT shape is small (~10 lines) and avoiding the SDK
// keeps the api-go module graph minimal.
package livekit

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/golang-jwt/jwt/v5"
)

// ErrNotConfigured surfaces 400 when API key/secret/URL are missing.
var ErrNotConfigured = errors.New("LiveKit is not configured. Please set LIVEKIT_API_KEY, LIVEKIT_API_SECRET, and LIVEKIT_WS_URL environment variables.")

// VideoGrant mirrors livekit-server-sdk's `VideoGrant`. We only emit
// the fields the NestJS service sets (roomJoin/room/canPublish/...);
// extra fields will be added if a caller needs them.
type VideoGrant struct {
	RoomJoin       bool   `json:"roomJoin"`
	Room           string `json:"room"`
	CanPublish     bool   `json:"canPublish"`
	CanSubscribe   bool   `json:"canSubscribe"`
	CanPublishData bool   `json:"canPublishData"`
}

// AccessTokenClaims is the LiveKit-conformant JWT body.
type AccessTokenClaims struct {
	Name     string     `json:"name,omitempty"`
	Metadata string     `json:"metadata,omitempty"`
	Video    VideoGrant `json:"video"`
	jwt.RegisteredClaims
}

// Service holds the LiveKit config. A missing field flips the service
// into a degraded mode where generateToken returns ErrNotConfigured.
type Service struct {
	apiKey    string
	apiSecret []byte
	wsURL     string
	ttl       time.Duration
}

// NewService builds the Service. Empty key/secret/url is allowed —
// the routes still mount, but /token returns 400 with the
// human-readable "LiveKit is not configured" message matching NestJS.
func NewService(apiKey, apiSecret, wsURL string) *Service {
	return &Service{
		apiKey:    apiKey,
		apiSecret: []byte(apiSecret),
		wsURL:     wsURL,
		ttl:       6 * time.Hour, // matches livekit-server-sdk default
	}
}

// Configured returns true when all three LiveKit env vars are present.
func (s *Service) Configured() bool {
	return s.apiKey != "" && len(s.apiSecret) > 0 && s.wsURL != ""
}

// GenerateToken issues a LiveKit access token for {identity, room}.
func (s *Service) GenerateToken(room, identity, name, metadata string) (token, url string, err error) {
	if !s.Configured() {
		return "", "", ErrNotConfigured
	}
	if room == "" || len(room) > 64 {
		return "", "", errors.New("Invalid room name")
	}
	if identity == "" {
		return "", "", errors.New("Invalid identity")
	}

	now := time.Now()
	claims := AccessTokenClaims{
		Name:     name,
		Metadata: metadata,
		Video: VideoGrant{
			RoomJoin:       true,
			Room:           room,
			CanPublish:     true,
			CanSubscribe:   true,
			CanPublishData: true,
		},
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    s.apiKey,
			Subject:   identity,
			NotBefore: jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(s.ttl)),
		},
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := t.SignedString(s.apiSecret)
	if err != nil {
		return "", "", fmt.Errorf("sign livekit token: %w", err)
	}
	return signed, s.wsURL, nil
}

// WebSocketURL returns the configured wsUrl, or error when missing.
func (s *Service) WebSocketURL() (string, error) {
	if !s.Configured() {
		return "", ErrNotConfigured
	}
	return s.wsURL, nil
}

// Router mounts /api/livekit/token + /api/livekit/url.
func Router(svc *Service) chi.Router {
	r := chi.NewRouter()
	r.Get("/token", svc.handleToken)
	r.Get("/url", svc.handleURL)
	return r
}

func (s *Service) handleToken(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	room := strings.TrimSpace(q.Get("room"))
	identity := strings.TrimSpace(q.Get("identity"))
	name := strings.TrimSpace(q.Get("name"))

	if room == "" {
		writeError(w, http.StatusBadRequest, "Room name is required")
		return
	}
	if identity == "" {
		identity = fmt.Sprintf("user-%d-%s", time.Now().UnixMilli(), randomSuffix())
	}
	if name == "" {
		name = identity
	}

	tok, url, err := s.GenerateToken(room, identity, name, "")
	if errors.Is(err, ErrNotConfigured) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "livekit generateToken failed", "err", err)
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"token":    tok,
		"url":      url,
		"room":     room,
		"identity": identity,
		"name":     name,
	})
}

func (s *Service) handleURL(w http.ResponseWriter, r *http.Request) {
	url, err := s.WebSocketURL()
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"url": url})
}

func randomSuffix() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "0000"
	}
	return hex.EncodeToString(b[:])
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		slog.Error("livekit response encode failed", "err", err)
	}
}

func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"statusCode": status,
		"message":    message,
	})
}
