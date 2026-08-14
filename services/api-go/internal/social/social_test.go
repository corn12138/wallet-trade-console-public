package social

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

const (
	profileAddr = "0x2222222222222222222222222222222222222222"
	viewerAddr  = "0x1111111111111111111111111111111111111111"
)

func withWallet(req *http.Request, wallet string) *http.Request {
	return req.WithContext(auth.WithAddress(req.Context(), wallet))
}

func TestFollow_RejectsSelfFollow(t *testing.T) {
	repo := NewRepository(nil)
	if _, err := repo.Follow(context.Background(), viewerAddr, strings.ToUpper(viewerAddr)); err != ErrSelfFollow {
		t.Fatalf("err = %v, want ErrSelfFollow (case-insensitive)", err)
	}
}

func TestFollowHandlers_AuthAndValidation(t *testing.T) {
	r := Router(NewService(NewRepository(nil)), nil, nil)

	// No wallet identity → 401 even with the guard unset (dev fallback).
	req := httptest.NewRequest(http.MethodPost, "/"+profileAddr+"/follow", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("follow without wallet = %d, want 401", rr.Code)
	}

	// Invalid profile address → 400.
	bad := withWallet(httptest.NewRequest(http.MethodPost, "/nothex/follow", nil), viewerAddr)
	badRR := httptest.NewRecorder()
	r.ServeHTTP(badRR, bad)
	if badRR.Code != http.StatusBadRequest {
		t.Errorf("invalid address = %d, want 400", badRR.Code)
	}

	// Self-follow → 400 with the explicit message.
	self := withWallet(httptest.NewRequest(http.MethodPost, "/"+viewerAddr+"/follow", nil), viewerAddr)
	selfRR := httptest.NewRecorder()
	r.ServeHTTP(selfRR, self)
	if selfRR.Code != http.StatusBadRequest || !strings.Contains(selfRR.Body.String(), "yourself") {
		t.Errorf("self-follow = %d %s", selfRR.Code, selfRR.Body.String())
	}

	// Valid follow against a nil pool → 503, never fake success.
	ok := withWallet(httptest.NewRequest(http.MethodPost, "/"+profileAddr+"/follow", nil), viewerAddr)
	okRR := httptest.NewRecorder()
	r.ServeHTTP(okRR, ok)
	if okRR.Code != http.StatusServiceUnavailable {
		t.Errorf("follow (nil pool) = %d, want 503", okRR.Code)
	}
}

func TestFollowState_PublicAndDegraded(t *testing.T) {
	r := Router(NewService(NewRepository(nil)), nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/"+profileAddr+"/follow-state", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("state = %d, want 200 (degrades to zero counts)", rr.Code)
	}
	var state FollowState
	if err := json.Unmarshal(rr.Body.Bytes(), &state); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if state.Address != profileAddr || state.Followers != 0 || state.IsFollowing != nil {
		t.Errorf("state = %+v", state)
	}

	badReq := httptest.NewRequest(http.MethodGet, "/nothex/follow-state", nil)
	badRR := httptest.NewRecorder()
	r.ServeHTTP(badRR, badReq)
	if badRR.Code != http.StatusBadRequest {
		t.Errorf("invalid profile state = %d, want 400", badRR.Code)
	}
}

func TestFollowMutations_GuardWraps(t *testing.T) {
	deny := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		})
	}
	r := Router(NewService(NewRepository(nil)), nil, deny)

	req := withWallet(httptest.NewRequest(http.MethodPost, "/"+profileAddr+"/follow", nil), viewerAddr)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("guarded follow = %d, want 401 from middleware", rr.Code)
	}

	// The public read must stay open under the guard.
	stateReq := httptest.NewRequest(http.MethodGet, "/"+profileAddr+"/follow-state", nil)
	stateRR := httptest.NewRecorder()
	r.ServeHTTP(stateRR, stateReq)
	if stateRR.Code != http.StatusOK {
		t.Errorf("public state with guard wired = %d, want 200", stateRR.Code)
	}
}
