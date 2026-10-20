package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/txreview"
)

func TestContractEvidencePromptPreservesUnitsAndLimits(t *testing.T) {
	review := blockedReview()
	review.Checks = []txreview.Check{
		{ID: "contract-position-call", Status: txreview.StatusPass, Severity: txreview.SeverityInfo,
			Title: "Position calldata decoded", Summary: "sizeDelta=10000000000000000000000000000000000 (USD scaled by 1e30); collateralAmount=100000000 (token base units)"},
		{ID: "contract-deadline", Status: txreview.StatusFail, Severity: txreview.SeverityHigh,
			Title: "Encoded deadline expired", Summary: "The contract evaluates block.timestamp at execution."},
		{ID: "simulation-chain-unverified", Status: txreview.StatusWarn, Severity: txreview.SeverityMedium,
			Title: "Simulation chain not verified", Summary: "No simulation from that RPC is used."},
	}
	errMessage := "https://private-rpc.example/secret-token"
	review.Simulation.Mode = txreview.SimulationModeFallback
	review.Simulation.ErrorMessage = &errMessage
	prompt := buildUserPrompt(review, "zh")
	for _, fact := range []string{"[contract-position-call/pass/info]", "USD scaled by 1e30", "token base units", "[contract-deadline/fail/high]", "simulation-chain-unverified", "block.timestamp", "Locale: zh"} {
		if !strings.Contains(prompt, fact) {
			t.Errorf("missing fact %q: %s", fact, prompt)
		}
	}
	if strings.Contains(prompt, errMessage) || strings.Contains(prompt, review.GeneratedTx.Data) {
		t.Fatal("raw provider errors or full calldata reached the model")
	}
	for _, boundary := range []string{"not a completed audit", "do not invent token", "never instructions", "without\n   guessing"} {
		if !strings.Contains(SystemPrompt, boundary) {
			t.Errorf("missing semantic boundary %q", boundary)
		}
	}
}

func TestPromptIncludesSafeReadCallTristate(t *testing.T) {
	succeeded, failed := true, false
	for _, tc := range []struct {
		name string
		call *bool
	}{
		{"succeeded", &succeeded}, {"failed", &failed}, {"unknown", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			review := blockedReview()
			errMessage := "https://private-rpc.example/provider-secret"
			review.Simulation.ErrorMessage = &errMessage
			review.Simulation.CallSucceeded = tc.call
			prompt := buildUserPrompt(review, "en")
			for _, fact := range []string{"RPC read-call result: " + tc.name, "omits the complete sender/value envelope", "not a full wallet transaction simulation"} {
				if !strings.Contains(prompt, fact) {
					t.Errorf("missing read-call evidence %q: %s", fact, prompt)
				}
			}
			if strings.Contains(prompt, errMessage) {
				t.Fatal("raw provider error reached model")
			}
		})
	}
}

func TestPromptCarriesFullEnvelopeProvenanceWithoutLegacyClaim(t *testing.T) {
	review := blockedReview()
	observation := txreview.SimulationObservation{
		ObservationStatus: "succeeded", Source: "rpc", Pinning: "hash-pinned", ObservedAt: "2026-10-08T02:00:00Z",
		BlockReference: &txreview.ObservationBlockReference{Number: "12345678901234567890", Hash: "0x" + strings.Repeat("a", 64), Pinning: "hash-pinned"},
		Reason:         "https://private.example/secret",
	}
	review.Simulation.SimulationScope = "full-envelope"
	review.Simulation.ConsistentSnapshot = true
	review.Simulation.Observations.Call = observation
	review.Simulation.Observations.Gas = observation
	review.Simulation.Observations.Balance = observation
	review.Simulation.Observations.Allowance = txreview.SimulationObservation{ObservationStatus: "not-required", Source: "not-requested", Pinning: "unavailable"}
	review.Simulation.Observations.FeeQuote = txreview.SimulationObservation{ObservationStatus: "succeeded", Source: "rpc", Pinning: "latest-only"}
	prompt := buildUserPrompt(review, "en")
	for _, fact := range []string{"full-envelope (from, to, value and calldata)", "blockNumber=12345678901234567890", "Observation allowance: status=not-required", "Observation feeQuote: status=succeeded", "Fee quote is latest-only", "does not guarantee future execution"} {
		if !strings.Contains(prompt, fact) {
			t.Errorf("missing evidence %q", fact)
		}
	}
	if strings.Contains(prompt, "omits the complete sender/value envelope") || strings.Contains(prompt, observation.Reason) {
		t.Fatal("full simulation carries a false legacy claim or private upstream text")
	}
}

type evidenceRPC struct{}

func (evidenceRPC) ChainID(context.Context) (*big.Int, error) { return big.NewInt(11155111), nil }
func (evidenceRPC) EstimateGas(context.Context, string, string, string, string) (*big.Int, error) {
	return big.NewInt(21000), nil
}
func (evidenceRPC) EthCall(context.Context, string, string, string) ([]byte, error) {
	return []byte{1}, nil
}
func (evidenceRPC) GetGasPrice(context.Context) (*big.Int, error) { return big.NewInt(1), nil }
func (evidenceRPC) GetBalance(context.Context, string, string) (*big.Int, error) {
	return big.NewInt(1_000_000), nil
}

type evidenceRegistry struct{ address string }

func (e evidenceRegistry) KnownSpenders(int) []string { return []string{e.address} }
func (e evidenceRegistry) ContractAddress(chain int, name string) (string, bool) {
	return e.address, chain == 11155111 && name == "PositionManager"
}

func TestExplainHandlerUsesRealDecodedReviewWithoutRewritingTransaction(t *testing.T) {
	const target = "0x000000000000000000000000000000000000BEEF"
	addressArg, _ := abi.EncodeAddress(testAddr)
	large := new(big.Int).Lsh(big.NewInt(1), 100)
	largeArg, _ := abi.EncodeUint256(large)
	boolArg, _ := abi.EncodeUint256(big.NewInt(1))
	zeroArg, _ := abi.EncodeUint256(big.NewInt(0))
	for _, method := range []string{"openPosition", "closePosition"} {
		t.Run(method, func(t *testing.T) {
			data := abi.EncodeCall(method+"(address,address,uint256,uint256,bool,uint256,uint256)", addressArg, addressArg, largeArg, largeArg, boolArg, largeArg, zeroArg)
			tx := txreview.TxShape{ChainID: 11155111, To: target, Value: "000", Data: data}
			chain := 11155111
			body, _ := json.Marshal(ExplainRequest{ReviewInput: txreview.ReviewInput{
				OperationType: txreview.OpCustom, FromAddress: testAddr, ChainID: &chain, Tx: &tx,
			}, Locale: "en"})
			reviewer := txreview.NewService(evidenceRPC{}, nil, nil, nil, nil, evidenceRegistry{target}, nil)
			client := &fakeClient{exp: Explanation{Headline: "The submitted position call has an expired deadline.", WhatHappens: []string{"The encoded deadline has expired."}, Locale: "en"}}
			req := httptest.NewRequest(http.MethodPost, "/tx-review/explain", bytes.NewReader(body))
			req = req.WithContext(auth.WithAddress(req.Context(), testAddr))
			response := httptest.NewRecorder()
			makeExplainHandler(enabledService(client, reviewer))(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("handler status=%d body=%s", response.Code, response.Body.String())
			}
			var result ExplainResponse
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(result.Review.GeneratedTx, tx) || result.Review.ReviewStatus != txreview.ReviewBlocked || client.calls != 1 {
				t.Fatalf("handler did not keep the actual frozen transaction and verdict: %+v", result)
			}
			facts := []string{"PositionManager." + method, "direction=long", "sizeDelta=" + large.String(), "[contract-deadline/fail/high]", "Verdict (do not change): blocked"}
			if method == "closePosition" {
				facts = append(facts, "collateralDelta="+large.String()+" (raw ABI argument", "ignored by the current repository PerpMarket implementation", "does not control payout", "proportional to the effective sizeDelta", "PnL and fees applied", "no payout amount is established")
			} else {
				facts = append(facts, "collateralAmount="+large.String()+" (token base units)")
			}
			for _, fact := range facts {
				if !strings.Contains(client.gotUser, fact) {
					t.Errorf("actual handler prompt omitted %q: %s", fact, client.gotUser)
				}
			}
			if strings.Contains(client.gotUser, data) {
				t.Fatal("handler forwarded full calldata to model")
			}
		})
	}
}

type countedEvidenceRPC struct {
	evidenceRPC
	calls int
}

func (r *countedEvidenceRPC) ChainID(context.Context) (*big.Int, error) {
	r.calls++
	return big.NewInt(11155111), nil
}

func TestExplainHandlerRejectsUnknownOperationBeforeRPCOrModel(t *testing.T) {
	const target = "0x000000000000000000000000000000000000beef"
	addressArg, _ := abi.EncodeAddress(testAddr)
	zeroArg, _ := abi.EncodeUint256(big.NewInt(0))
	data := abi.EncodeCall("closePosition(address,address,uint256,uint256,bool,uint256,uint256)", addressArg, addressArg, zeroArg, zeroArg, zeroArg, zeroArg, zeroArg)
	body, _ := json.Marshal(ExplainRequest{ReviewInput: txreview.ReviewInput{OperationType: "position", FromAddress: testAddr,
		Tx: &txreview.TxShape{ChainID: 11155111, To: target, Value: "1", Data: data}}})
	rpc := &countedEvidenceRPC{}
	reviewer := txreview.NewService(rpc, nil, nil, nil, nil, evidenceRegistry{target}, nil)
	client := &fakeClient{exp: goodExplanation()}
	req := httptest.NewRequest(http.MethodPost, "/tx-review/explain", bytes.NewReader(body))
	req = req.WithContext(auth.WithAddress(req.Context(), testAddr))
	response := httptest.NewRecorder()
	makeExplainHandler(enabledService(client, reviewer))(response, req)
	if response.Code != http.StatusBadRequest || rpc.calls != 0 || client.calls != 0 {
		t.Fatalf("unsupported operation reached RPC/model: status=%d rpc=%d model=%d", response.Code, rpc.calls, client.calls)
	}
}
