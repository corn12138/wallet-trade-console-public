package livekit

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestService_GenerateTokenNotConfigured(t *testing.T) {
	s := NewService("", "", "")
	if _, _, err := s.GenerateToken("room", "ident", "name", ""); !errors.Is(err, ErrNotConfigured) {
		t.Errorf("err = %v, want ErrNotConfigured", err)
	}
}

func TestService_GenerateTokenRejectsBadRoom(t *testing.T) {
	s := NewService("api-key", "api-secret", "wss://lk.example.com")
	if _, _, err := s.GenerateToken("", "ident", "n", ""); err == nil {
		t.Errorf("empty room should fail")
	}
	long := strings.Repeat("a", 65)
	if _, _, err := s.GenerateToken(long, "ident", "n", ""); err == nil {
		t.Errorf("room > 64 chars should fail")
	}
	if _, _, err := s.GenerateToken("room", "", "n", ""); err == nil {
		t.Errorf("empty identity should fail")
	}
}

func TestService_GenerateTokenRoundTrip(t *testing.T) {
	s := NewService("api-key-1", "api-secret-1", "wss://lk.example.com")
	tok, url, err := s.GenerateToken("room-1", "user-1", "Alice", "")
	if err != nil {
		t.Fatalf("GenerateToken err = %v", err)
	}
	if url != "wss://lk.example.com" {
		t.Errorf("url = %q, want wss://lk.example.com", url)
	}

	parsed, err := jwt.ParseWithClaims(tok, &AccessTokenClaims{}, func(t *jwt.Token) (any, error) {
		return []byte("api-secret-1"), nil
	})
	if err != nil || !parsed.Valid {
		t.Fatalf("parse claims err = %v, valid = %v", err, parsed.Valid)
	}
	claims, ok := parsed.Claims.(*AccessTokenClaims)
	if !ok {
		t.Fatalf("wrong claims type")
	}
	if claims.Issuer != "api-key-1" {
		t.Errorf("iss = %q, want api-key-1", claims.Issuer)
	}
	if claims.Subject != "user-1" {
		t.Errorf("sub = %q, want user-1", claims.Subject)
	}
	if claims.Name != "Alice" {
		t.Errorf("name = %q, want Alice", claims.Name)
	}
	if !claims.Video.RoomJoin || claims.Video.Room != "room-1" {
		t.Errorf("video = %+v, want RoomJoin+room-1", claims.Video)
	}
	if !claims.Video.CanPublish || !claims.Video.CanSubscribe || !claims.Video.CanPublishData {
		t.Errorf("video grants missing: %+v", claims.Video)
	}
}

func TestService_WrongSecretRejected(t *testing.T) {
	s := NewService("k", "right-secret", "wss://lk.example.com")
	tok, _, _ := s.GenerateToken("r", "u", "", "")
	if _, err := jwt.ParseWithClaims(tok, &AccessTokenClaims{}, func(_ *jwt.Token) (any, error) {
		return []byte("wrong-secret"), nil
	}); err == nil {
		t.Errorf("parse with wrong secret should fail")
	}
}

func TestHandler_TokenMissingRoom_400(t *testing.T) {
	mux := Router(NewService("k", "s", "wss://x"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/token", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_TokenNotConfigured_400(t *testing.T) {
	mux := Router(NewService("", "", ""))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/token?room=r", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_TokenHappyPath(t *testing.T) {
	mux := Router(NewService("k", "s", "wss://x"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/token?room=room&identity=u-1&name=Alice", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body=%s)", rec.Code, rec.Body.String())
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["url"] != "wss://x" || body["room"] != "room" || body["identity"] != "u-1" || body["name"] != "Alice" {
		t.Errorf("body = %+v, missing expected fields", body)
	}
	if _, ok := body["token"].(string); !ok {
		t.Errorf("token missing or not string: %v", body["token"])
	}
}

func TestHandler_TokenDefaultsIdentity(t *testing.T) {
	mux := Router(NewService("k", "s", "wss://x"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/token?room=r", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	id, _ := body["identity"].(string)
	if !strings.HasPrefix(id, "user-") {
		t.Errorf("identity = %q, want user-...", id)
	}
	if body["name"] != id {
		t.Errorf("name = %q should default to identity %q", body["name"], id)
	}
}

func TestHandler_URLNotConfigured_400(t *testing.T) {
	mux := Router(NewService("", "", ""))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/url", nil))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_URLConfigured_200(t *testing.T) {
	mux := Router(NewService("k", "s", "wss://lk.example.com"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/url", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["url"] != "wss://lk.example.com" {
		t.Errorf("url = %v", body["url"])
	}
}
