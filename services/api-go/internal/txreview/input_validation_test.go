package txreview

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReviewHTTPBoundsAndUnsupportedExtensions(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		code       int
	}{
		{"unknown-envelope", `{"tx":{"to":"` + smokeAddr + `","data":"0x","accessList":[]}}`, 400},
		{"authorization-extension", `{"tx":{"to":"` + smokeAddr + `","data":"0x","authorizationList":[]}}`, 400},
		{"state-override", `{"tx":{"to":"` + smokeAddr + `","data":"0x"},"stateOverride":{}}`, 400},
		{"trailing-body", `{} {}`, 400},
		{"oversized-body", `{"siteOrigin":"` + strings.Repeat("x", reviewBodyLimit) + `"}`, 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			Router(NewService(nil, nil, nil, nil, nil, nil, nil), nil).ServeHTTP(rec, authedReview(tc.body, smokeAddr))
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tc.code, rec.Body.String())
			}
		})
	}
}

func TestReviewCalldataBoundaryAndMalformedValues(t *testing.T) {
	for _, data := range []string{"0x1", "1234", "0xgg", "0x" + strings.Repeat("ab", 16*1024+1)} {
		body, _ := json.Marshal(ReviewInput{FromAddress: smokeAddr, Tx: &TxShape{DefaultChainID, smokeAddr, "0", data}})
		rec := httptest.NewRecorder()
		Router(NewService(nil, nil, nil, nil, nil, nil, nil), nil).ServeHTTP(rec, authedReview(string(body), smokeAddr))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("invalid calldata accepted: status %d", rec.Code)
		}
	}
	if err := validateCalldata("0x" + strings.Repeat("ab", 16*1024)); err != nil {
		t.Fatalf("16 KiB boundary rejected: %v", err)
	}
}
