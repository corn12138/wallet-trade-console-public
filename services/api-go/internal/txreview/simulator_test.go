package txreview

import (
	"context"
	"errors"
	"math/big"
	"testing"
)

type stubRPC struct {
	estimateGas   *big.Int
	estimateErr   error
	gasPrice      *big.Int
	gasPriceErr   error
	balance       *big.Int
	balanceErr    error
	ethCallResult []byte
	ethCallErr    error

	gotEstimateValue string
	gotEthCallTo     string
}

func (s *stubRPC) EstimateGas(_ context.Context, _, _, value, _ string) (*big.Int, error) {
	s.gotEstimateValue = value
	return s.estimateGas, s.estimateErr
}

func (s *stubRPC) GetGasPrice(_ context.Context) (*big.Int, error) {
	return s.gasPrice, s.gasPriceErr
}

func (s *stubRPC) GetBalance(_ context.Context, _, _ string) (*big.Int, error) {
	return s.balance, s.balanceErr
}

func (s *stubRPC) EthCall(_ context.Context, to, _, _ string) ([]byte, error) {
	s.gotEthCallTo = to
	return s.ethCallResult, s.ethCallErr
}

func TestSimulate_NilClientFallback(t *testing.T) {
	r := Simulate(context.Background(), nil, "0xabc", TxShape{}, "")
	if r.Mode != SimulationModeFallback {
		t.Errorf("mode = %s, want fallback", r.Mode)
	}
	if r.ErrorMessage == nil || *r.ErrorMessage == "" {
		t.Errorf("expected error message")
	}
}

func TestSimulate_HappyPath_BuildsGasFee(t *testing.T) {
	rpc := &stubRPC{
		estimateGas:   big.NewInt(21000),
		gasPrice:      big.NewInt(1_000_000_000),
		balance:       big.NewInt(5_000_000_000_000_000_000),
		ethCallResult: []byte{0x01},
	}
	r := Simulate(context.Background(), rpc, "0xabc", TxShape{To: "0xdef", Data: "0xfeed", Value: "100"}, "")
	if r.Mode != SimulationModeEstimate {
		t.Errorf("mode = %s, want estimate-gas", r.Mode)
	}
	if r.GasEstimateString == nil || *r.GasEstimateString != "21000" {
		t.Errorf("gasEstimate = %v", r.GasEstimateString)
	}
	if r.GasLimitSuggestion == nil || *r.GasLimitSuggestion != "25200" {
		t.Errorf("gasLimitSuggestion = %v, want 25200", r.GasLimitSuggestion)
	}
	if r.EstimatedFeeString == nil || *r.EstimatedFeeString != "21000000000000" {
		t.Errorf("estimatedFeeWei = %v, want 21000000000000", r.EstimatedFeeString)
	}
	if r.CallSucceeded == nil || !*r.CallSucceeded {
		t.Errorf("callSucceeded = %v", r.CallSucceeded)
	}
	if r.NativeBalanceWei == nil || r.NativeBalanceWei.Cmp(big.NewInt(5_000_000_000_000_000_000)) != 0 {
		t.Errorf("nativeBalance = %v", r.NativeBalanceWei)
	}
	// Value 100 → "0x64"
	if rpc.gotEstimateValue != "0x64" {
		t.Errorf("estimate value param = %q", rpc.gotEstimateValue)
	}
}

func TestSimulate_EstimateErrorSurfacesMessage(t *testing.T) {
	rpc := &stubRPC{
		estimateErr:   errors.New("execution reverted: insufficient funds"),
		ethCallResult: []byte{0x01},
	}
	r := Simulate(context.Background(), rpc, "0xabc", TxShape{}, "")
	if r.GasEstimateString != nil {
		t.Errorf("gasEstimate should be nil")
	}
	if r.ErrorMessage == nil || *r.ErrorMessage != "execution reverted: insufficient funds" {
		t.Errorf("errorMessage = %v", r.ErrorMessage)
	}
	// callOK came through OK; mode is estimate-gas since callSucceeded != nil
	if r.Mode != SimulationModeEstimate {
		t.Errorf("mode = %s, want estimate-gas (callSucceeded set)", r.Mode)
	}
}

func TestSimulate_RespectsPreSuppliedBalance(t *testing.T) {
	rpc := &stubRPC{estimateGas: big.NewInt(21000)}
	r := Simulate(context.Background(), rpc, "0xabc", TxShape{}, "777")
	if r.NativeBalanceWei == nil || r.NativeBalanceWei.Cmp(big.NewInt(777)) != 0 {
		t.Errorf("balance = %v, want 777 (pre-supplied)", r.NativeBalanceWei)
	}
}

func TestFetchAllowance_ParsesUint256(t *testing.T) {
	// 32-byte representation of 100.
	buf := make([]byte, 32)
	buf[31] = 100
	rpc := &stubRPC{ethCallResult: buf}
	got := FetchAllowance(context.Background(), rpc,
		"0x000000000000000000000000000000000000beef",
		"0x000000000000000000000000000000000000aaaa",
		"0x000000000000000000000000000000000000bbbb",
	)
	if got == nil || got.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("got = %v, want 100", got)
	}
	if rpc.gotEthCallTo != "0x000000000000000000000000000000000000beef" {
		t.Errorf("EthCall.to = %q", rpc.gotEthCallTo)
	}
}

func TestFetchAllowance_NilOnError(t *testing.T) {
	rpc := &stubRPC{ethCallErr: errors.New("boom")}
	got := FetchAllowance(context.Background(), rpc,
		"0x000000000000000000000000000000000000beef",
		"0x000000000000000000000000000000000000aaaa",
		"0x000000000000000000000000000000000000bbbb",
	)
	if got != nil {
		t.Errorf("got = %v, want nil on error", got)
	}
}

func TestFetchAllowance_NilOnBadAddress(t *testing.T) {
	rpc := &stubRPC{ethCallResult: make([]byte, 32)}
	if FetchAllowance(context.Background(), rpc, "0xbeef", "0xbad", "0xspender") != nil {
		t.Errorf("bad owner should yield nil")
	}
}

func TestNormalizeTokenWarnings(t *testing.T) {
	in := []string{"a", " a ", "", "b", "a"}
	got := NormalizeTokenWarnings(in)
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("got = %v", got)
	}
}
