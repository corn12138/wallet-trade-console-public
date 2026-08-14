package token

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

func TestCreateRequest_Validate(t *testing.T) {
	cases := []struct {
		name    string
		req     CreateRequest
		wantErr error
	}{
		{
			name: "happy minimal",
			req: CreateRequest{
				Symbol:     "TKN",
				Name:       "Token",
				LaunchType: "NEW_COIN",
				ChainID:    11155111,
			},
			wantErr: nil,
		},
		{
			name:    "missing symbol",
			req:     CreateRequest{Name: "X", LaunchType: "NEW_COIN", ChainID: 1},
			wantErr: ErrSymbolRequired,
		},
		{
			name: "symbol too long",
			req: CreateRequest{
				Symbol: strings.Repeat("A", 21), Name: "X",
				LaunchType: "NEW_COIN", ChainID: 1,
			},
			wantErr: ErrSymbolTooLong,
		},
		{
			name:    "missing name",
			req:     CreateRequest{Symbol: "X", LaunchType: "NEW_COIN", ChainID: 1},
			wantErr: ErrNameRequired,
		},
		{
			name: "name too long",
			req: CreateRequest{
				Symbol: "X", Name: strings.Repeat("N", 101),
				LaunchType: "NEW_COIN", ChainID: 1,
			},
			wantErr: ErrNameTooLong,
		},
		{
			name:    "missing chainId",
			req:     CreateRequest{Symbol: "X", Name: "N", LaunchType: "NEW_COIN"},
			wantErr: ErrChainIDRequired,
		},
		{
			name:    "invalid launchType",
			req:     CreateRequest{Symbol: "X", Name: "N", LaunchType: "BOGUS", ChainID: 1},
			wantErr: ErrInvalidLaunchType,
		},
		{
			name: "preBuyPercent above range",
			req: CreateRequest{
				Symbol: "X", Name: "N", LaunchType: "NEW_COIN", ChainID: 1,
				PreBuyPercent: ptrF64(100),
			},
			wantErr: ErrPreBuyOutOfRange,
		},
		{
			name: "preBuyPercent below range",
			req: CreateRequest{
				Symbol: "X", Name: "N", LaunchType: "NEW_COIN", ChainID: 1,
				PreBuyPercent: ptrF64(-0.1),
			},
			wantErr: ErrPreBuyOutOfRange,
		},
		{
			name: "customAddress too long",
			req: CreateRequest{
				Symbol: "X", Name: "N", LaunchType: "NEW_COIN", ChainID: 1,
				CustomAddress: ptrStr("12345"),
			},
			wantErr: ErrCustomAddrTooLong,
		},
	}
	for _, tc := range cases {
		if got := tc.req.Validate(); got != tc.wantErr {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.wantErr)
		}
	}
}

func TestCreate_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.Create(context.Background(), CreateRequest{Symbol: "X", Name: "N", LaunchType: "NEW_COIN", ChainID: 1}, "0xabc")
	if err != ErrCreateRequiresPool {
		t.Errorf("err = %v, want ErrCreateRequiresPool", err)
	}
}

func TestService_CreateTokenSkipsRepoOnValidationError(t *testing.T) {
	// Validation runs before repo, so a nil pool doesn't matter when
	// the DTO is bad — we get the validation error, not the pool error.
	// Catches a regression where validation is moved into the repo and
	// nil-pool short-circuits dominate.
	svc := NewService(NewRepository(nil))
	_, err := svc.CreateToken(context.Background(), CreateRequest{}, "0xabc")
	if err != ErrSymbolRequired {
		t.Errorf("err = %v, want ErrSymbolRequired (validation must fire first)", err)
	}
}

func TestHandler_CreateRequiresAuthContext(t *testing.T) {
	// makeCreateHandler 401s when the auth middleware didn't set an
	// address on the context. Tests the defense-in-depth fallback
	// inside the handler itself, not the middleware.
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil) // no auth middleware
	body := `{"symbol":"X","name":"N","launchType":"NEW_COIN","chainId":1}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401 (handler must 401 when no auth context)", rec.Code)
	}
}

func TestHandler_CreateValidationErrorIs400(t *testing.T) {
	// Skip the middleware entirely by injecting the address via
	// auth.WithAddress on a custom router setup.
	svc := NewService(NewRepository(nil))
	body := `{"symbol":"","name":"N","launchType":"NEW_COIN","chainId":1}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000DEAD"))
	rec := httptest.NewRecorder()
	makeCreateHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "symbol is required") {
		t.Errorf("body = %q, want 'symbol is required'", rec.Body.String())
	}
}

func TestHandler_CreateNilPoolReturns503(t *testing.T) {
	// Degraded mode: no DB pool means we cannot create. /readyz already
	// surfaces 503 under nil pool; the create handler follows suit
	// rather than masquerading as a 500.
	svc := NewService(NewRepository(nil))
	body := `{"symbol":"NEW","name":"NewToken","launchType":"NEW_COIN","chainId":11155111}`
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000DEAD"))
	rec := httptest.NewRecorder()
	makeCreateHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestHandler_CreateMalformedBodyIs400(t *testing.T) {
	svc := NewService(NewRepository(nil))
	req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("not json"))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000DEAD"))
	rec := httptest.NewRecorder()
	makeCreateHandler(svc).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestCreateRequestJSONShape(t *testing.T) {
	// Sanity check the JSON encoding matches what the FE submits. A
	// regression where Tags serializes as `null` instead of an empty
	// list would break the FE's tag-render guard.
	req := CreateRequest{
		Symbol: "X", Name: "N", LaunchType: "NEW_COIN", ChainID: 1,
		Tags: nil,
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Empty tags omitted via `omitempty` — that's the contract; FE
	// guards against missing as well as empty.
	if strings.Contains(string(raw), `"tags":null`) {
		t.Errorf("body = %s, must not serialize tags as null", string(raw))
	}
}

func ptrF64(v float64) *float64 { return &v }
func ptrStr(v string) *string   { return &v }
