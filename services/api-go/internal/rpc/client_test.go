package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestEthCall_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s", r.Method)
		}
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.Method != "eth_call" {
			t.Errorf("method = %q", req.Method)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x0000000000000000000000000000000000000000000000000000000000000064"}`))
	}))
	defer srv.Close()

	cli := NewClient(srv.URL, 2*time.Second)
	out, err := cli.EthCall(context.Background(), "0x0000000000000000000000000000000000000001", "0xdeadbeef", "")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	got := new(big.Int).SetBytes(out)
	if got.Cmp(big.NewInt(100)) != 0 {
		t.Errorf("decoded = %s, want 100", got)
	}
}

func TestEthCall_RPCErrorReturnsTyped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32000,"message":"execution reverted"}}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, 0).EthCall(context.Background(), "0x", "0x", "")
	if err == nil {
		t.Fatal("expected err")
	}
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) || rpcErr.Code != -32000 {
		t.Errorf("got = %v, want *RPCError code=-32000", err)
	}
}

func TestEthCall_HTTPNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream down"))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, 0).EthCall(context.Background(), "0x", "0x", "")
	if err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("err = %v, want 502", err)
	}
}

func TestEthCall_EmptyResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x"}`))
	}))
	defer srv.Close()
	_, err := NewClient(srv.URL, 0).EthCall(context.Background(), "0x", "0x", "")
	if !errors.Is(err, ErrEmpty) {
		t.Errorf("err = %v, want ErrEmpty", err)
	}
}

func TestEthCall_NilClient(t *testing.T) {
	var c *Client
	if _, err := c.EthCall(context.Background(), "0x", "0x", ""); err == nil {
		t.Errorf("expected err on nil client")
	}
}

func TestEstimateGas_HexResultParsed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method != "eth_estimateGas" {
			t.Errorf("method = %q", req.Method)
		}
		// 0x5208 = 21000 (basic gas limit)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x5208"}`))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL, 0).EstimateGas(context.Background(), "0xabc", "0xdef", "", "0x1234")
	if err != nil || v.Cmp(big.NewInt(21000)) != 0 {
		t.Errorf("v=%v err=%v, want 21000", v, err)
	}
}

func TestGetGasPrice_ParsesHex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// 0x3b9aca00 = 1 Gwei
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x3b9aca00"}`))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL, 0).GetGasPrice(context.Background())
	if err != nil || v.Cmp(big.NewInt(1_000_000_000)) != 0 {
		t.Errorf("v=%v err=%v, want 1e9", v, err)
	}
}

func TestBlockNumber_ParsesHex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method != "eth_blockNumber" {
			t.Errorf("method = %q", req.Method)
		}
		if len(req.Params) != 0 {
			t.Errorf("params = %#v", req.Params)
		}
		// 0x10d4f = 68_943
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x10d4f"}`))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL, 0).BlockNumber(context.Background())
	if err != nil || v.Cmp(big.NewInt(68_943)) != 0 {
		t.Errorf("v=%v err=%v, want 68943", v, err)
	}
}

func TestGetBalance_DefaultsToLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method != "eth_getBalance" {
			t.Errorf("method = %q", req.Method)
		}
		if len(req.Params) != 2 || req.Params[1] != "latest" {
			t.Errorf("params = %#v", req.Params)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x0"}`))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL, 0).GetBalance(context.Background(), "0xabc", "")
	if err != nil || v.Sign() != 0 {
		t.Errorf("v=%v err=%v, want 0", v, err)
	}
}

func TestCallHex_EmptyResultIsZero(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x"}`))
	}))
	defer srv.Close()
	v, err := NewClient(srv.URL, 0).GetGasPrice(context.Background())
	if err != nil || v.Sign() != 0 {
		t.Errorf("v=%v err=%v, want 0", v, err)
	}
}

func TestGetTransactionByHash_DecodesAuthoritativeFields(t *testing.T) {
	to := "0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if req.Method != "eth_getTransactionByHash" {
			t.Errorf("method = %q", req.Method)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"hash":"0xabc","from":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","to":"` + to + `","value":"0x2a"}}`))
	}))
	defer srv.Close()

	tx, ok, err := NewClient(srv.URL, 0).GetTransactionByHash(context.Background(), "0xabc")
	if err != nil || !ok {
		t.Fatalf("tx=%+v ok=%v err=%v", tx, ok, err)
	}
	if tx.TxHash != "0xabc" || tx.From != "0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" || tx.To == nil || *tx.To != to {
		t.Errorf("transaction fields = %+v", tx)
	}
	if tx.Value == nil || tx.Value.Cmp(big.NewInt(42)) != 0 {
		t.Errorf("value = %v, want 42", tx.Value)
	}
}

func TestGetTransactionByHash_NullIsNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":null}`))
	}))
	defer srv.Close()
	_, ok, err := NewClient(srv.URL, 0).GetTransactionByHash(context.Background(), "0xabc")
	if err != nil || ok {
		t.Fatalf("ok=%v err=%v, want false,nil", ok, err)
	}
}

func TestGetTransactionReceipt_DecodesStatusAndGas(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"transactionHash":"0xabc","from":"0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","to":"0xbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","contractAddress":null,"blockNumber":"0x7b","gasUsed":"0x5208","effectiveGasPrice":"0x3b9aca00","status":"0x1"}}`))
	}))
	defer srv.Close()

	receipt, ok, err := NewClient(srv.URL, 0).GetTransactionReceipt(context.Background(), "0xabc")
	if err != nil || !ok {
		t.Fatalf("receipt=%+v ok=%v err=%v", receipt, ok, err)
	}
	if !receipt.Success || receipt.BlockNumber != 123 || receipt.GasUsed == nil || receipt.GasUsed.Int64() != 21000 {
		t.Errorf("receipt = %+v", receipt)
	}
	if receipt.EffectiveGasPrice == nil || receipt.EffectiveGasPrice.Int64() != 1_000_000_000 {
		t.Errorf("gas price = %v", receipt.EffectiveGasPrice)
	}
}

func TestGetTransactionReceipt_RequiresStatusProof(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"transactionHash":"0xabc","blockNumber":"0x7b"}}`))
	}))
	defer srv.Close()
	if _, _, err := NewClient(srv.URL, 0).GetTransactionReceipt(context.Background(), "0xabc"); err == nil {
		t.Fatal("expected missing receipt status to fail verification")
	}
}

func TestDecodeUint256At(t *testing.T) {
	// Build 64 bytes: first 32 = 0x01, second 32 = 0xff
	buf := make([]byte, 64)
	buf[31] = 1
	buf[63] = 0xff
	v, err := DecodeUint256At(buf, 0)
	if err != nil || v.Cmp(big.NewInt(1)) != 0 {
		t.Errorf("v0 = %v err=%v", v, err)
	}
	v, err = DecodeUint256At(buf, 32)
	if err != nil || v.Cmp(big.NewInt(0xff)) != 0 {
		t.Errorf("v1 = %v err=%v", v, err)
	}
	if _, err := DecodeUint256At(buf, 64); err == nil {
		t.Errorf("expected err past end")
	}
}

func TestDecodeDynamicUint256Array(t *testing.T) {
	// Layout: [offset=0x20 (32 bytes)] [length=3 (32)] [v=10, v=20, v=30]
	hexStr := "" +
		// offset 32
		"0000000000000000000000000000000000000000000000000000000000000020" +
		// length 3
		"0000000000000000000000000000000000000000000000000000000000000003" +
		// 10, 20, 30
		"000000000000000000000000000000000000000000000000000000000000000a" +
		"0000000000000000000000000000000000000000000000000000000000000014" +
		"000000000000000000000000000000000000000000000000000000000000001e"
	buf, _ := hex.DecodeString(hexStr)
	arr, err := DecodeDynamicUint256Array(buf)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if len(arr) != 3 {
		t.Fatalf("len = %d", len(arr))
	}
	if arr[0].Cmp(big.NewInt(10)) != 0 || arr[1].Cmp(big.NewInt(20)) != 0 || arr[2].Cmp(big.NewInt(30)) != 0 {
		t.Errorf("vals = %v", arr)
	}
}

func TestDecodeDynamicUint256Array_RejectsTruncated(t *testing.T) {
	// Offset points past data length.
	hexStr := "0000000000000000000000000000000000000000000000000000000000000400" +
		"0000000000000000000000000000000000000000000000000000000000000003"
	buf, _ := hex.DecodeString(hexStr)
	if _, err := DecodeDynamicUint256Array(buf); err == nil {
		t.Errorf("expected truncated err")
	}
}

func TestGetLogs_EncodesFilterAndDecodesResult(t *testing.T) {
	var gotMethod string
	var gotParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(body, &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		gotMethod = req.Method
		if len(req.Params) == 1 {
			gotParams, _ = req.Params[0].(map[string]any)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[
			{"address":"0xABC","topics":["0xtopic0","0xtopic1"],"data":"0xdead",
			 "blockNumber":"0x10","transactionHash":"0xhash","logIndex":"0x2","removed":false}
		]}`))
	}))
	defer srv.Close()

	logs, err := NewClient(srv.URL, 2*time.Second).GetLogs(context.Background(), LogFilter{
		Address: "0xABC", FromBlock: 16, ToBlock: 32,
	})
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if gotMethod != "eth_getLogs" {
		t.Errorf("method = %q, want eth_getLogs", gotMethod)
	}
	if gotParams["fromBlock"] != "0x10" {
		t.Errorf("fromBlock = %v, want 0x10", gotParams["fromBlock"])
	}
	if gotParams["toBlock"] != "0x20" {
		t.Errorf("toBlock = %v, want 0x20", gotParams["toBlock"])
	}
	if gotParams["address"] != "0xABC" {
		t.Errorf("address = %v, want 0xABC", gotParams["address"])
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %d, want 1", len(logs))
	}
	l := logs[0]
	if l.BlockNumber != 16 {
		t.Errorf("blockNumber = %d, want 16", l.BlockNumber)
	}
	if l.LogIndex != 2 {
		t.Errorf("logIndex = %d, want 2", l.LogIndex)
	}
	if l.TxHash != "0xhash" {
		t.Errorf("txHash = %q, want 0xhash", l.TxHash)
	}
	if len(l.Topics) != 2 || l.Topics[0] != "0xtopic0" {
		t.Errorf("topics = %v", l.Topics)
	}
	if l.Data != "0xdead" {
		t.Errorf("data = %q, want 0xdead", l.Data)
	}
}

func TestGetLogs_OmitsAddressWhenEmpty(t *testing.T) {
	var gotParams map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rpcRequest
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &req)
		if len(req.Params) == 1 {
			gotParams, _ = req.Params[0].(map[string]any)
		}
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[]}`))
	}))
	defer srv.Close()

	logs, err := NewClient(srv.URL, 0).GetLogs(context.Background(), LogFilter{FromBlock: 0, ToBlock: 5})
	if err != nil {
		t.Fatalf("GetLogs: %v", err)
	}
	if _, ok := gotParams["address"]; ok {
		t.Errorf("address should be omitted, got %v", gotParams["address"])
	}
	if gotParams["fromBlock"] != "0x0" {
		t.Errorf("fromBlock = %v, want 0x0", gotParams["fromBlock"])
	}
	if len(logs) != 0 {
		t.Errorf("logs = %d, want 0", len(logs))
	}
}

func TestGetLogs_MalformedBlockNumberErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[
			{"address":"0xabc","topics":[],"data":"0x","blockNumber":"0xzz","transactionHash":"0xh","logIndex":"0x0"}
		]}`))
	}))
	defer srv.Close()
	if _, err := NewClient(srv.URL, 0).GetLogs(context.Background(), LogFilter{FromBlock: 0, ToBlock: 1}); err == nil {
		t.Fatal("expected error on malformed blockNumber")
	}
}

func TestHexQuantity_RoundTrip(t *testing.T) {
	for _, n := range []uint64{0, 1, 15, 16, 255, 256, 1_000_000} {
		s := hexQuantity(n)
		got, err := parseHexQuantity(s)
		if err != nil || got != n {
			t.Errorf("roundtrip %d → %q → %d (err=%v)", n, s, got, err)
		}
	}
	if hexQuantity(16) != "0x10" {
		t.Errorf("hexQuantity(16) = %q, want 0x10 (no leading zeros)", hexQuantity(16))
	}
	if v, err := parseHexQuantity(""); err != nil || v != 0 {
		t.Errorf("empty quantity → %d, %v; want 0, nil", v, err)
	}
}

// TestGetTransactionByHashReadsNonceAndBlockHash pins the two fields that make a
// replacement observable. A speed-up reuses the sender's nonce under a new hash,
// so without the nonce the two attempts are indistinguishable.
func TestGetTransactionByHashReadsNonceAndBlockHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"hash":"0xabc","from":"0xdead","to":"0xcafe","value":"0x2a",
			"nonce":"0x11","blockHash":"0xbbb"}}`))
	}))
	defer server.Close()

	tx, ok, err := NewClient(server.URL, 0).GetTransactionByHash(context.Background(), "0xabc")
	if err != nil || !ok {
		t.Fatalf("GetTransactionByHash ok=%v err=%v", ok, err)
	}
	if tx.Nonce == nil || *tx.Nonce != 17 {
		t.Errorf("nonce = %v, want 17 (0x11)", tx.Nonce)
	}
	if tx.BlockHash == nil || *tx.BlockHash != "0xbbb" {
		t.Errorf("blockHash = %v, want 0xbbb", tx.BlockHash)
	}
}

// A mempool transaction has no block. Nodes disagree on whether that is null or
// "", and both must normalise to nil so "mined" is never inferred from a value
// that is merely present.
func TestGetTransactionByHashTreatsAbsentBlockHashAsNotMined(t *testing.T) {
	for _, raw := range []string{`null`, `""`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
				"hash":"0xabc","from":"0xdead","to":null,"value":"0x0",
				"nonce":"0x0","blockHash":` + raw + `}}`))
		}))
		tx, ok, err := NewClient(server.URL, 0).GetTransactionByHash(context.Background(), "0xabc")
		server.Close()
		if err != nil || !ok {
			t.Fatalf("blockHash %s: ok=%v err=%v", raw, ok, err)
		}
		if tx.BlockHash != nil {
			t.Errorf("blockHash %s: got %q, want nil", raw, *tx.BlockHash)
		}
	}
}

func TestGetTransactionReceiptReadsBlockHash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"transactionHash":"0xabc","from":"0xdead","to":"0xcafe",
			"blockNumber":"0x7","blockHash":"0xccc","gasUsed":"0x5208",
			"effectiveGasPrice":"0x1","status":"0x1"}}`))
	}))
	defer server.Close()

	receipt, ok, err := NewClient(server.URL, 0).GetTransactionReceipt(context.Background(), "0xabc")
	if err != nil || !ok {
		t.Fatalf("GetTransactionReceipt ok=%v err=%v", ok, err)
	}
	if receipt.BlockHash == nil || *receipt.BlockHash != "0xccc" {
		t.Errorf("blockHash = %v, want 0xccc", receipt.BlockHash)
	}
}

// A malformed nonce must fail the read rather than default to 0 — nonce 0 is a
// real, meaningful value (a wallet's first transaction).
func TestGetTransactionByHashRejectsAMalformedNonce(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"hash":"0xabc","from":"0xdead","to":null,"value":"0x0","nonce":"zz"}}`))
	}))
	defer server.Close()

	if _, _, err := NewClient(server.URL, 0).GetTransactionByHash(context.Background(), "0xabc"); err == nil {
		t.Fatal("a malformed nonce must be an error, not a silent 0")
	}
}

// An absent nonce must read as "unknown", never as nonce 0. parseHexQuantity
// maps "" to 0 and 0 is a real nonce, so collapsing the two would make every
// first transaction look like it shares a nonce with the others.
func TestGetTransactionByHashLeavesAnAbsentNonceUnknown(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{
			"hash":"0xabc","from":"0xdead","to":null,"value":"0x0"}}`))
	}))
	defer server.Close()

	tx, ok, err := NewClient(server.URL, 0).GetTransactionByHash(context.Background(), "0xabc")
	if err != nil || !ok {
		t.Fatalf("an absent nonce must not fail the read: ok=%v err=%v", ok, err)
	}
	if tx.Nonce != nil {
		t.Fatalf("nonce = %d, want nil (unknown) — 0 is a real nonce", *tx.Nonce)
	}
}
