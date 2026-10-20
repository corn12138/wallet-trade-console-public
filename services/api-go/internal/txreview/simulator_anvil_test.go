package txreview

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// Run with TXREVIEW_ANVIL_INTEGRATION=1. The explicit opt-in makes an absent
// compiler/node a visible skip in ordinary unit runs, and a failure in this gate.
func TestAnvilCompleteEnvelopeSenderValueAndRevert(t *testing.T) {
	if os.Getenv("TXREVIEW_ANVIL_INTEGRATION") != "1" {
		t.Skip("requires explicit local Anvil/solc integration gate")
	}
	forge, err := exec.LookPath("forge")
	if err != nil {
		t.Fatal(err)
	}
	anvil, err := exec.LookPath("anvil")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err = os.Mkdir(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("testdata/EnvelopeFixture.sol")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "src", "EnvelopeFixture.sol"), source, 0600); err != nil {
		t.Fatal(err)
	}
	compile := exec.Command(forge, "inspect", "--root", root, "--use", "0.8.28", "--evm-version", "paris", "EnvelopeFixture", "bytecode")
	bytecode, err := compile.Output()
	if err != nil {
		t.Fatalf("compile fixture: %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	node := exec.CommandContext(ctx, anvil, "--host", "127.0.0.1", "--port", strconv.Itoa(port), "--chain-id", "31337", "--silent")
	if err = node.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); _ = node.Wait() })
	endpoint := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := rpc.NewClient(endpoint, time.Second)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err = client.ChainID(ctx); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Anvil did not start")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var accounts []string
	anvilRPC(t, endpoint, "eth_accounts", []any{}, &accounts)
	var txHash string
	anvilRPC(t, endpoint, "eth_sendTransaction", []any{map[string]string{"from": accounts[0], "data": strings.TrimSpace(string(bytecode)), "gas": "0x200000"}}, &txHash)
	var receipt struct {
		ContractAddress string `json:"contractAddress"`
		Status          string `json:"status"`
	}
	deadline = time.Now().Add(5 * time.Second)
	for {
		anvilRPC(t, endpoint, "eth_getTransactionReceipt", []any{txHash}, &receipt)
		if receipt.Status != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("fixture receipt timed out")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if receipt.Status != "0x1" || receipt.ContractAddress == "" {
		t.Fatal("fixture deployment failed")
	}
	ownerArg, err := abi.EncodeAddress(accounts[0])
	if err != nil {
		t.Fatal(err)
	}
	valueArg := make([]byte, 32)
	valueArg[31] = 7
	calldata := abi.EncodeCall("verify(address,uint256)", ownerArg, valueArg)
	base := TxShape{31337, receipt.ContractAddress, "7", calldata}
	for _, tc := range []struct {
		name, from, value, data string
		status                  string
	}{
		{"sender-and-value", accounts[0], "7", calldata, "succeeded"},
		{"wrong-sender", accounts[1], "7", calldata, "reverted"},
		{"wrong-value", accounts[0], "6", calldata, "reverted"},
		{"explicit-revert", accounts[0], "0", abi.EncodeCall("forceRevert()"), "reverted"},
		{"empty-return-success", accounts[0], "1", abi.EncodeCall("noReturn()"), "succeeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := base
			tx.Value = tc.value
			tx.Data = tc.data
			result := Simulate(ctx, client, tc.from, tx, "")
			if result.SimulationScope != "full-envelope" || result.Observations.Call.ObservationStatus != tc.status || result.Observations.Gas.ObservationStatus != tc.status {
				t.Fatalf("actual EVM evidence mismatch: %+v", result)
			}
			if tc.status == "succeeded" && (!result.ConsistentSnapshot || result.NativeBalanceWei == nil || result.GasEstimate.Cmp(big.NewInt(21000)) < 0) {
				t.Fatalf("missing full pinned evidence: %+v", result)
			}
		})
	}
	t.Run("allowance-shares-canonical-anchor", func(t *testing.T) {
		result := simulateReview(ctx, client, accounts[0], base, &AllowanceRequest{TokenAddress: receipt.ContractAddress, Spender: accounts[1]})
		if !result.ConsistentSnapshot || result.CurrentAllowance == nil || result.CurrentAllowance.Cmp(big.NewInt(42)) != 0 {
			t.Fatalf("missing canonical allowance: %+v", result)
		}
		allowance := result.Observations.Allowance
		call := result.Observations.Call
		if allowance.BlockReference == nil || call.BlockReference == nil || allowance.BlockReference.Hash != call.BlockReference.Hash || allowance.BlockReference.Number != call.BlockReference.Number {
			t.Fatal("allowance uses a different block")
		}
	})
	if _, err = client.EthCall(ctx, receipt.ContractAddress, calldata, ""); !rpc.IsExecutionRevert(err) {
		t.Fatalf("fixture failed to distinguish incomplete view-call: %v", err)
	}
}

func anvilRPC(t *testing.T, endpoint, method string, params []any, out any) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	if err != nil {
		t.Fatal(err)
	}
	response, err := (&http.Client{Timeout: 5 * time.Second}).Post(endpoint, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var frame struct {
		Result json.RawMessage `json:"result"`
		Error  *rpc.RPCError   `json:"error"`
	}
	if err = json.NewDecoder(response.Body).Decode(&frame); err != nil {
		t.Fatal(err)
	}
	if frame.Error != nil {
		t.Fatal(frame.Error)
	}
	if err = json.Unmarshal(frame.Result, out); err != nil {
		t.Fatal(err)
	}
}
