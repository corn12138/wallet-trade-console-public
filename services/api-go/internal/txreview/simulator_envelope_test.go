package txreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// This transport fixture checks provenance, not EVM semantics; the Anvil test
// separately establishes real contract sender/value behavior.
func envelopeRPC(t *testing.T, scenario string) *rpc.Client {
	t.Helper()
	headerReads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     uint64            `json:"id"`
			Method string            `json:"method"`
			Params []json.RawMessage `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		result := any("0x0")
		fail := func(code int, message string) {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"error":{"code":%d,"message":%q}}`, req.ID, code, message)
		}
		switch req.Method {
		case "eth_chainId":
			result = "0xaa36a7"
		case "eth_getBlockByNumber":
			headerReads++
			hash := "a"
			if scenario == "reorg" && headerReads > 1 {
				hash = "b"
			}
			result = map[string]string{"number": "0x12", "hash": "0x" + strings.Repeat(hash, 64)}
		case "eth_call", "eth_estimateGas", "eth_getBalance":
			if len(req.Params) != 2 {
				t.Errorf("missing pinned block: %s", req.Method)
				return
			}
			if scenario == "number-only" && strings.HasPrefix(string(req.Params[1]), "{") {
				fail(-32602, "invalid argument 1: cannot unmarshal object")
				return
			}
			if scenario == "gas-unsupported" && req.Method == "eth_estimateGas" {
				fail(-32602, "too many arguments")
				return
			}
			if scenario == "balance-error" && req.Method == "eth_getBalance" {
				w.WriteHeader(429)
				return
			}
			if req.Method == "eth_getBalance" {
				result = "0xffffffffffffffff"
				break
			}
			var call rpc.TransactionCall
			_ = json.Unmarshal(req.Params[0], &call)
			if call.From != smokeAddr || call.To != positionManagerAddress || call.Value != "0x0" || call.Data != "0x1234" {
				t.Errorf("wrong envelope: %+v", call)
			}
			if scenario == "call-429" && req.Method == "eth_call" {
				w.WriteHeader(429)
				return
			}
			if scenario == "call-revert" && req.Method == "eth_call" {
				fail(3, "execution reverted")
				return
			}
			if scenario == "malformed" && req.Method == "eth_call" {
				result = map[string]string{"unexpected": "shape"}
				break
			}
			result = "0x"
			if req.Method == "eth_estimateGas" {
				result = "0x5208"
			}
		case "eth_gasPrice":
			result = "0x1"
		default:
			t.Errorf("unexpected RPC method %s", req.Method)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
	}))
	t.Cleanup(server.Close)
	return rpc.NewClient(server.URL, 0)
}

func TestFullEnvelopeObservationProvenanceAndDegradation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		consistent bool
		callStatus string
		gasStatus  string
	}{
		{"hash", true, "succeeded", "succeeded"},
		{"number-only", true, "succeeded", "succeeded"},
		{"gas-unsupported", false, "succeeded", "unsupported"},
		{"balance-error", false, "succeeded", "succeeded"},
		{"call-429", false, "unavailable", "succeeded"},
		{"call-revert", false, "reverted", "succeeded"},
		{"malformed", false, "unavailable", "succeeded"},
		{"reorg", false, "unavailable", "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := Simulate(context.Background(), envelopeRPC(t, tc.name), smokeAddr, TxShape{DefaultChainID, positionManagerAddress, "0", "0x1234"}, "999999999999999999999999999999")
			if result.SimulationScope != "full-envelope" || result.ConsistentSnapshot != tc.consistent || result.Observations.Call.ObservationStatus != tc.callStatus || result.Observations.Gas.ObservationStatus != tc.gasStatus {
				t.Fatalf("incorrect scope/evidence: %+v", result)
			}
			if result.Observations.Call.BlockReference.Number != "18" {
				t.Fatal("public block number must be decimal")
			}
			if result.Observations.FeeQuote.Pinning != "latest-only" || result.Observations.FeeQuote.BlockReference != nil {
				t.Fatal("fee quote laundered into historical block")
			}
			if tc.name == "number-only" && result.Observations.Call.Pinning != "number-pinned" {
				t.Fatal("number fallback mislabeled")
			}
			if tc.callStatus == "unavailable" && result.CallSucceeded != nil {
				t.Fatal("infrastructure failure became execution failure")
			}
			if tc.name == "reorg" && (result.NativeBalanceWei != nil || result.GasEstimate != nil) {
				t.Fatal("reorganized observations affected rules")
			}
			if tc.name == "balance-error" && result.NativeBalanceWei != nil {
				t.Fatal("client hint substituted for failed balance")
			}
		})
	}
}

func TestReviewRejectsFrozenClientBalanceBeforeRPC(t *testing.T) {
	svc := NewService(nil, nil, nil, nil, nil, nil, nil)
	_, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr, NativeBalanceWei: ptrStr("999999999999999999999999"), Tx: &TxShape{DefaultChainID, positionManagerAddress, "0", "0x"}})
	if err == nil {
		t.Fatal("client balance override accepted")
	}
}

func TestFullEnvelopeReviewSeparatesRevertFromInfrastructure(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status ReviewStatus
	}{{"call-revert", ReviewBlocked}, {"call-429", ReviewSimulationUnavailable}, {"gas-unsupported", ReviewSimulationUnavailable}, {"reorg", ReviewSimulationUnavailable}} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(envelopeRPC(t, tc.name), nil, nil, nil, nil, nil, nil)
			out, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr, Tx: &TxShape{DefaultChainID, positionManagerAddress, "0", "0x1234"}})
			if err != nil || out.ReviewStatus != tc.status {
				t.Fatalf("status=%s err=%v checks=%+v", out.ReviewStatus, err, out.Checks)
			}
		})
	}
}
