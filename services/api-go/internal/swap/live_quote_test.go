package swap

import (
	"context"
	"errors"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"strings"
	"testing"
)

func TestQuoteFailureDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, stage, want string
		err               error
	}{
		{"amount timeout", "getAmountsOut", "amounts timeout", context.DeadlineExceeded},
		{"reserve timeout", "getReserves", "reserves timeout", context.DeadlineExceeded},
		{"provider rejection", "getAmountsOut", "amounts RPC rejected", &rpc.RPCError{Code: -32000, Message: "https://provider.invalid/private-key"}},
		{"transport", "getReserves", "reserves RPC failed", errors.New("https://provider.invalid/private-key")},
		{"empty", "getAmountsOut", "invalid quote data", rpc.ErrEmpty},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(newLoader("0x0000000000000000000000000000000000001111"))
			svc.SetRPCClient(&stubCaller{getAmounts: buildUint256Array([]uint64{100, 90}), failOn: tc.stage, failErr: tc.err})
			got, err := svc.GetQuote(QuoteRequest{TokenIn: "0x000000000000000000000000000000000000aaaa", TokenOut: "0x000000000000000000000000000000000000bbbb", AmountIn: "100", TokenInDecimals: 0, TokenOutDecimals: 0})
			if err != nil {
				t.Fatal(err)
			}
			if got.Executable || got.QuoteStatus != QuoteStatusFallback || got.AmountOut != "" || got.MinimumReceived != "" {
				t.Fatalf("unsafe fallback: %+v", got)
			}
			diagnostic := strings.Join(got.Warnings, "|")
			if !strings.Contains(diagnostic, tc.want) || strings.Contains(diagnostic, "private-key") {
				t.Fatalf("wrong or unsafe diagnostic: %s", diagnostic)
			}
		})
	}
}
