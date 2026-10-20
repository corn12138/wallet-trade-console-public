package ai

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplainRejectsUnboundedOrAmbiguousRequestsBeforeReviewer(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
	}{
		{"oversized", `{"locale":"` + strings.Repeat("a", 65*1024) + `"}`, http.StatusRequestEntityTooLarge},
		{"two-values", explainBody + `{}`, http.StatusBadRequest},
		{"unsupported-envelope", `{"tx":{"to":"0x000000000000000000000000000000000000beef","data":"0x","accessList":[]}}`, http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reviewer := &fakeReviewer{result: blockedReview()}
			svc := NewService(Config{Enabled: false}, nil, reviewer)
			rec := httptest.NewRecorder()
			explainRouter(svc).ServeHTTP(rec, explainRequest(tc.body))
			if rec.Code != tc.status || reviewer.calls != 0 {
				t.Fatalf("code=%d calls=%d", rec.Code, reviewer.calls)
			}
		})
	}
}
