package web3events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/executionregistry"
)

func TestWorkbenchRealStoredContextsNegotiateAllStates(t *testing.T) {
	pool, repo := proofReportDatabase(t)
	rev, err := executionregistry.NewRepository(pool).Stage(context.Background(), buildSnapshot(t, "../../../../packages/shared/deployments"), "test", "workbench proof")
	if err != nil {
		t.Fatal(err)
	}
	exit := &ContextWrite{State: ContextUnboundExit, Mode: str("unboundExit"), ClaimDigest: str(strings.Repeat("a", 64)),
		Operation: str("trade.closePosition"), Market: str("ETH-USD"), Consumer: str("web-trade"),
		Manifest: str(strings.Repeat("b", 64)), Reason: str("authorityUnavailable")}
	router := Router(NewService(stubReader{}).WithWrites(repo, auth.StrictMiddleware(writeAuth.Verifier), inputVerifier{}).WithWorkbench(repo))
	token := signWeb3(t, ownerA)
	cases := []map[string]any{}
	for i, tc := range []struct {
		state string
		write *ContextWrite
	}{
		{"notRecorded", nil},
		{ContextRecorded, boundWrite(ContextRecorded, strings.Repeat("c", 64), rev, "")},
		{ContextClaimUnverified, boundWrite(ContextClaimUnverified, strings.Repeat("d", 64), rev, "")},
		{ContextClaimMismatch, boundWrite(ContextClaimMismatch, strings.Repeat("e", 64), rev, "bytesMismatch")},
		{ContextUnboundExit, exit},
	} {
		hash := fmt.Sprintf("0x%064x", i+1)
		if err := submitCtx(t, repo, 11155111, hash, uint64(i+1), tc.write); err != nil {
			t.Fatal(err)
		}
		action, _, _ := actionOf(t, pool, 11155111, hash)
		entry := map[string]any{"sourceState": tc.state}
		for _, variant := range []string{"base", "v2"} {
			req := httptest.NewRequest(http.MethodGet, "/actions/"+action+"/attempts?chainId=11155111", nil)
			req.Header.Set("Authorization", "Bearer "+token)
			if variant == "v2" {
				req.Header.Set("X-Workbench-Variants", WorkbenchVariantContextV2)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s/%s HTTP %d", tc.state, variant, rec.Code)
			}
			var page AttemptListResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil || len(page.Items) != 1 {
				t.Fatalf("%s/%s invalid page: %v", tc.state, variant, err)
			}
			want := tc.state
			if variant == "base" && tc.state != ContextRecorded {
				want = "notRecorded"
			}
			if page.Items[0].ExecutionContext.State != want {
				t.Fatalf("%s/%s state = %s, want %s", tc.state, variant, page.Items[0].ExecutionContext.State, want)
			}
			entry[variant] = json.RawMessage(append([]byte(nil), rec.Body.Bytes()...))
		}
		cases = append(cases, entry)
	}
	if destination := os.Getenv("WEB3_CONTEXT_WORKBENCH_PROOF_OUTPUT"); destination != "" {
		raw, err := json.MarshalIndent(map[string]any{"scope": "Actual current HTTP handler and PostgreSQL repository; synthetic stored transactions and authenticated test session", "cases": cases}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, append(raw, '\n'), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
