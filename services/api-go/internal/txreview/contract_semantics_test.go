package txreview

import (
	"context"
	"encoding/hex"
	"errors"
	"math/big"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

const positionManagerAddress = "0x000000000000000000000000000000000000beef"

type semanticRegistry struct {
	chainID int
	address string
}

func (s semanticRegistry) ContractAddress(chainID int, name string) (string, bool) {
	return s.address, chainID == s.chainID && name == "PositionManager"
}

func (s semanticRegistry) KnownSpenders(int) []string { return []string{s.address} }

func positionCalldata(t *testing.T, closing, long bool, price, deadline *big.Int) string {
	t.Helper()
	index, _ := abi.EncodeAddress("0x000000000000000000000000000000000000aaaa")
	collateral, _ := abi.EncodeAddress("0x000000000000000000000000000000000000bbbb")
	amount, _ := abi.EncodeUint256(big.NewInt(100_000_000))
	size, _ := abi.EncodeUint256(new(big.Int).Exp(big.NewInt(10), big.NewInt(34), nil))
	flag := big.NewInt(0)
	if long {
		flag.SetInt64(1)
	}
	boolArg, _ := abi.EncodeUint256(flag)
	priceArg, _ := abi.EncodeUint256(price)
	deadlineArg, _ := abi.EncodeUint256(deadline)
	signature := openPositionSignature
	if closing {
		signature = closePositionSignature
	}
	return abi.EncodeCall(signature, index, collateral, amount, size, boolArg, priceArg, deadlineArg)
}

func TestPositionSemanticsDirectionAndUnits(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	price, _ := new(big.Int).SetString("3450000000000000000000000000000000", 10)
	registry := semanticRegistry{DefaultChainID, positionManagerAddress}
	for _, tc := range []struct {
		name, bound, field string
		closing, long      bool
	}{
		{"open long", "maximum", "collateralAmount", false, true},
		{"open short", "minimum", "collateralAmount", false, false},
		{"close long", "minimum", "collateralDelta", true, true},
		{"close short", "maximum", "collateralDelta", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := TxShape{ChainID: DefaultChainID, To: positionManagerAddress, Value: "0",
				Data: positionCalldata(t, tc.closing, tc.long, price, big.NewInt(now.Unix()+60))}
			checks := contractSemanticChecks(tx, registry, now)
			priceCheck := findCheck(checks, "contract-price-bound")
			if priceCheck == nil || !strings.Contains(priceCheck.Summary, tc.bound+" execution price is "+price.String()) {
				t.Fatalf("price bound: %+v", priceCheck)
			}
			call := findCheck(checks, "contract-position-call")
			collateralFact := tc.field + "=100000000 (token base units)"
			if tc.closing {
				collateralFact = tc.field + "=100000000 (raw ABI argument; ignored by the current repository PerpMarket implementation)"
			}
			for _, fact := range []string{collateralFact, "sizeDelta=10000000000000000000000000000000000 (USD scaled by 1e30)"} {
				if call == nil || !strings.Contains(call.Summary, fact) {
					t.Errorf("missing exact fact %q: %+v", fact, call)
				}
			}
			if findCheck(checks, "contract-semantics-scope") == nil {
				t.Fatal("decoded facts require explicit evidence limits")
			}
		})
	}
}

func TestNonzeroCloseCollateralDeltaDoesNotPromisePayout(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	tx := TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, true, true, big.NewInt(1), big.NewInt(now.Unix()+60))}
	checks := contractSemanticChecks(tx, semanticRegistry{DefaultChainID, positionManagerAddress}, now)
	check := findCheck(checks, "contract-position-call")
	if check == nil {
		t.Fatal("close semantics missing")
	}
	for _, fact := range []string{"collateralDelta=100000000", "raw ABI argument", "ignored by the current repository PerpMarket implementation", "does not control payout", "proportional to the effective sizeDelta", "capped at the current position size", "PnL and fees", "no payout amount is established"} {
		if !strings.Contains(check.Summary, fact) {
			t.Errorf("missing close payout limitation %q: %s", fact, check.Summary)
		}
	}
	if strings.Contains(check.Summary, "token base units") {
		t.Fatal("ignored collateralDelta must not be described as a token withdrawal amount")
	}
}

func TestPositionSemanticsUnknownTargetsNeverDecode(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	valid := positionCalldata(t, false, true, big.NewInt(1), big.NewInt(now.Unix()+60))
	for _, tc := range []struct {
		name     string
		tx       TxShape
		registry ContractAddressLookup
	}{
		{"unregistered", TxShape{DefaultChainID, positionManagerAddress, "0", valid}, nil},
		{"wrong chain", TxShape{84532, positionManagerAddress, "0", valid}, semanticRegistry{DefaultChainID, positionManagerAddress}},
		{"wrong address", TxShape{DefaultChainID, smokeAddr, "0", valid}, semanticRegistry{DefaultChainID, positionManagerAddress}},
		{"zero deployment", TxShape{DefaultChainID, "0x0000000000000000000000000000000000000000", "0", valid}, semanticRegistry{DefaultChainID, "0x0000000000000000000000000000000000000000"}},
		{"unknown selector", TxShape{DefaultChainID, positionManagerAddress, "0", "0xdeadbeef"}, semanticRegistry{DefaultChainID, positionManagerAddress}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := contractSemanticChecks(tc.tx, tc.registry, now)
			if len(checks) != 1 || checks[0].ID != "contract-semantics-unknown" || checks[0].Status != StatusWarn {
				t.Fatalf("unknown target was treated as decoded: %+v", checks)
			}
		})
	}
}

func TestPositionSemanticsRejectMalformedABI(t *testing.T) {
	data := positionCalldata(t, false, true, big.NewInt(1), big.NewInt(2))
	raw, _ := hex.DecodeString(data[2:])
	mutateByte := func(index int, value byte) string {
		copy := append([]byte(nil), raw...)
		copy[index] = value
		return "0x" + hex.EncodeToString(copy)
	}
	for name, malformed := range map[string]string{
		"truncated": data[:len(data)-2], "trailing": data + "00",
		"invalid hex":     data[:len(data)-1] + "z",
		"address padding": mutateByte(4, 1), "collateral padding": mutateByte(36, 1),
		"invalid bool": mutateByte(4+4*32+31, 2), "bool padding": mutateByte(4+4*32, 1),
	} {
		t.Run(name, func(t *testing.T) {
			checks := contractSemanticChecks(TxShape{DefaultChainID, positionManagerAddress, "0", malformed}, semanticRegistry{DefaultChainID, positionManagerAddress}, time.Unix(1, 0))
			if len(checks) != 1 || checks[0].ID != "contract-calldata-invalid" || checks[0].Status != StatusFail {
				t.Fatalf("malformed ABI accepted: %+v", checks)
			}
		})
	}
}

func TestPositionSemanticsDeadlineAndUnboundedPrice(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	for _, tc := range []struct {
		name     string
		deadline *big.Int
		status   CheckStatus
	}{
		{"expired", big.NewInt(now.Unix() - 1), StatusFail},
		{"at deadline", big.NewInt(now.Unix()), StatusPass},
		{"future", big.NewInt(now.Unix() + 1), StatusPass},
		{"uint256", new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 256), big.NewInt(1)), StatusPass},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checks := contractSemanticChecks(TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, false, true, big.NewInt(0), tc.deadline)}, semanticRegistry{DefaultChainID, positionManagerAddress}, now)
			if check := findCheck(checks, "contract-deadline"); check == nil || check.Status != tc.status || !strings.Contains(check.Summary, tc.deadline.String()) {
				t.Fatalf("deadline: %+v", check)
			}
			if check := findCheck(checks, "contract-price-bound"); check == nil || check.Status != StatusWarn {
				t.Fatalf("zero price should warn: %+v", check)
			}
		})
	}
}

func TestReviewPositionSemanticsKeepsFrozenTransaction(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000)}, nil, nil, nil, nil,
		semanticRegistry{DefaultChainID, positionManagerAddress}, nil)
	svc.now = func() time.Time { return now }
	tx := TxShape{DefaultChainID, "0x000000000000000000000000000000000000BEEF", "000", positionCalldata(t, true, false, big.NewInt(123), big.NewInt(now.Unix()-1))}
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{OperationType: OpCustom, FromAddress: smokeAddr, ChainID: ptrInt(DefaultChainID), Tx: &tx})
	if err != nil || !reflect.DeepEqual(out.GeneratedTx, tx) || out.ReviewStatus != ReviewBlocked {
		t.Fatalf("frozen transaction or expired deadline lost: %+v, %v", out, err)
	}
	if check := findCheck(out.Checks, "contract-deadline"); check == nil || check.Status != StatusFail {
		t.Fatalf("semantic checks not integrated: %+v", out.Checks)
	}
	tx.Value = "1"
	checks := contractSemanticChecks(tx, semanticRegistry{DefaultChainID, positionManagerAddress}, now)
	if check := findCheck(checks, "contract-nonpayable-value"); check == nil || check.Status != StatusFail {
		t.Fatalf("nonpayable value not rejected: %+v", checks)
	}
}

func TestCustomReviewRejectsValueRewritingAndChainDrift(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	for _, value := range []string{"-1", "0x10", "bad", new(big.Int).Lsh(big.NewInt(1), 256).String()} {
		_, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr,
			Tx: &TxShape{DefaultChainID, positionManagerAddress, value, "0x12345678"}})
		if !errors.Is(err, ErrInvalidInput) {
			t.Errorf("invalid frozen value %q was silently rewritten: %v", value, err)
		}
	}
	_, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr,
		Tx: &TxShape{84532, positionManagerAddress, "0", "0x12345678"}})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("chain drift accepted: %v", err)
	}
}

func TestPositionSelectorsMatchContractABI(t *testing.T) {
	for signature, expected := range map[string]string{openPositionSignature: "6c560004", closePositionSignature: "9d75e58e"} {
		if got := hex.EncodeToString(abi.Selector(signature)); got != expected {
			t.Fatalf("selector for %s = %s, want %s", signature, got, expected)
		}
	}
}
