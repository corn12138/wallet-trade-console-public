package rpc

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTransactionCalldataIsAdditiveEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		want        *string
	}{
		{"omitted", "", nil},
		{"null", `,"input":null`, nil},
		{"number", `,"input":17`, nil},
		{"object", `,"input":{"secret":"payload"}`, nil},
		{"array", `,"input":[]`, nil},
		{"invalid hex", `,"input":"0xzz"`, nil},
		{"odd hex", `,"input":"0x1"`, nil},
		{"empty calldata", `,"input":"0x"`, textPointer("0x")},
		{"normalized", `,"input":"0xAAbb"`, textPointer("0xaabb")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"hash":"0xabc","from":"0x123","value":"0x0"` + tc.field + `}}`))
			}))
			defer srv.Close()
			got, ok, err := NewClient(srv.URL, 0).GetTransactionByHash(context.Background(), "0xabc")
			if err != nil || !ok {
				t.Fatalf("report evidence rejected transaction: ok=%v err=%v", ok, err)
			}
			if tc.want == nil {
				if got.Input != nil {
					t.Fatalf("want unknown input, got %q", *got.Input)
				}
			} else if got.Input == nil || *got.Input != *tc.want {
				t.Fatalf("input = %v, want %q", got.Input, *tc.want)
			}
		})
	}
}

func textPointer(s string) *string { return &s }
