package wallets

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
)

func TestUpsertWatchOnly_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertWatchOnly(context.Background(), CreateWatchOnlyRequest{
		Address:      "0x000000000000000000000000000000000000dEAD",
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
	})
	if err != ErrMutationRequiresPool {
		t.Errorf("err = %v, want ErrMutationRequiresPool", err)
	}
}

func TestUpsertWatchOnly_InvalidWalletAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertWatchOnly(context.Background(), CreateWatchOnlyRequest{
		Address: "not-an-address",
	})
	if err != ErrInvalidWalletAddr {
		t.Errorf("err = %v, want ErrInvalidWalletAddr", err)
	}
}

func TestUpsertWatchOnly_InvalidOwnerAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpsertWatchOnly(context.Background(), CreateWatchOnlyRequest{
		Address:      "0x000000000000000000000000000000000000dEAD",
		OwnerAddress: "bad-owner",
	})
	if err != ErrInvalidOwnerAddr {
		t.Errorf("err = %v, want ErrInvalidOwnerAddr", err)
	}
}

func TestUpsertWatchOnly_OwnerRequiredForGroup(t *testing.T) {
	r := NewRepository(nil)
	gid := "group-abc"
	_, err := r.UpsertWatchOnly(context.Background(), CreateWatchOnlyRequest{
		Address: "0x000000000000000000000000000000000000dEAD",
		GroupID: &gid,
	})
	if err != ErrOwnerRequiredForGroup {
		t.Errorf("err = %v, want ErrOwnerRequiredForGroup", err)
	}
}

func TestCreateGroup_InvalidOwnerAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.CreateGroup(context.Background(), CreateGroupRequest{
		OwnerAddress: "nope", Name: "Trading",
	})
	if err != ErrInvalidOwnerAddr {
		t.Errorf("err = %v, want ErrInvalidOwnerAddr", err)
	}
}

func TestCreateGroup_BlankNameRejected(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.CreateGroup(context.Background(), CreateGroupRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
		Name:         "   ",
	})
	if err != ErrGroupNameRequired {
		t.Errorf("err = %v, want ErrGroupNameRequired", err)
	}
}

func TestUpdateProfile_NilPoolReturnsErr(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpdateProfile(context.Background(), "wallet-1", UpdateProfileRequest{
		OwnerAddress: "0x000000000000000000000000000000000000bEEF",
	})
	if err != ErrMutationRequiresPool {
		t.Errorf("err = %v, want ErrMutationRequiresPool", err)
	}
}

func TestUpdateProfile_InvalidOwnerAddress(t *testing.T) {
	r := NewRepository(nil)
	_, err := r.UpdateProfile(context.Background(), "wallet-1", UpdateProfileRequest{
		OwnerAddress: "bad",
	})
	if err != ErrInvalidOwnerAddr {
		t.Errorf("err = %v, want ErrInvalidOwnerAddr", err)
	}
}

func TestNormalizeOptionalText(t *testing.T) {
	// nil → nil
	if got := normalizeOptionalText(nil); got != nil {
		t.Errorf("nil input got %v, want nil", got)
	}
	// whitespace-only → nil (matches NestJS normalizeOptionalText which
	// returns null for trimmed-empty strings)
	empty := "   "
	if got := normalizeOptionalText(&empty); got != nil {
		t.Errorf("whitespace input got %v, want nil", got)
	}
	// value preserved trimmed
	v := "  Trading  "
	got := normalizeOptionalText(&v)
	if got == nil || *got != "Trading" {
		t.Errorf("got %v, want pointer to \"Trading\"", got)
	}
}

// ---------- handler tests ----------

func mountMutationRouter(t *testing.T) http.Handler {
	t.Helper()
	// nil pool — mutation handlers will all 503 (ErrMutationRequiresPool)
	// before any SQL. That's enough to exercise the request-level paths
	// (auth, body parsing, owner resolution) without a real database.
	svc := NewService(NewRepository(nil))
	openAuth := func(next http.Handler) http.Handler {
		return next // tests inject the auth context via req.WithContext.
	}
	return Router(svc, openAuth)
}

func TestHandler_PostWatchOnlyUnauthorizedWithoutContext(t *testing.T) {
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/watch-only", strings.NewReader(`{"address":"0x000000000000000000000000000000000000dEAD"}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

func TestHandler_PostWatchOnlyOwnerMismatchIs403(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"address":"0x000000000000000000000000000000000000dEAD","ownerAddress":"0x0000000000000000000000000000000000000001"}`
	req := httptest.NewRequest(http.MethodPost, "/watch-only", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("status = %d, want 403 (owner mismatch); body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostWatchOnlyMalformedBodyIs400(t *testing.T) {
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/watch-only", strings.NewReader("not-json"))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_PostWatchOnlyBadWalletAddressIs400(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"address":"not-an-address"}`
	req := httptest.NewRequest(http.MethodPost, "/watch-only", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_PostWatchOnlyNilPoolIs503(t *testing.T) {
	// Address and owner both valid → service layer reaches the repo,
	// which short-circuits on nil pool with 503-shaped sentinel.
	mux := mountMutationRouter(t)
	body := `{"address":"0x000000000000000000000000000000000000dEAD"}`
	req := httptest.NewRequest(http.MethodPost, "/watch-only", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandler_PostGroupsBlankNameIs400(t *testing.T) {
	mux := mountMutationRouter(t)
	body := `{"name":""}`
	req := httptest.NewRequest(http.MethodPost, "/groups", strings.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), "0x000000000000000000000000000000000000bEEF"))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestHandler_PutProfileMissingWalletIdReturns404(t *testing.T) {
	// chi maps empty-trailing routes to 404 by default; this validates
	// the route shape, not the handler. Documents the contract.
	mux := mountMutationRouter(t)
	req := httptest.NewRequest(http.MethodPut, "//profile", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code == http.StatusOK {
		t.Errorf("status = %d, want a non-200 (empty walletId route shouldn't match)", rec.Code)
	}
}

func TestHandler_MutationsDontMountWhenAuthMiddlewareIsNil(t *testing.T) {
	// Belt-and-braces: a deployment without JWT_SECRET configures nil
	// middleware, which the Router treats as "don't mount guarded
	// routes at all". 404 (not 401) confirms the routes are absent.
	svc := NewService(NewRepository(nil))
	mux := Router(svc, nil)
	req := httptest.NewRequest(http.MethodPost, "/watch-only", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404/405 (route should not mount)", rec.Code)
	}
}
