package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestTransactionCallPreservesEnvelopeAndHashPin(t *testing.T) {
	call := TransactionCall{"0x" + strings.Repeat("a", 40), "0x" + strings.Repeat("b", 40), "0x64", "0xCaFe"}
	block := BlockReference{"0x12", "0x" + strings.Repeat("c", 64), "hash-pinned"}
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
		var actual TransactionCall
		_ = json.Unmarshal(req.Params[0], &actual)
		if actual != call {
			t.Errorf("execution envelope changed: %+v", actual)
		}
		var ref map[string]any
		_ = json.Unmarshal(req.Params[1], &ref)
		if ref["blockHash"] != block.Hash || ref["requireCanonical"] != true {
			t.Errorf("missing hash anchor: %+v", ref)
		}
		result := "0x"
		if req.Method == "eth_estimateGas" {
			result = "0x5208"
		}
		fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":%q}`, req.ID, result)
	}))
	defer server.Close()
	client := NewClient(server.URL, 0)
	if data, err := client.EthCallTransaction(context.Background(), call, block); err != nil || len(data) != 0 {
		t.Fatalf("empty successful return rejected: %x %v", data, err)
	}
	if gas, err := client.EstimateGasTransaction(context.Background(), call, block); err != nil || gas.Int64() != 21000 {
		t.Fatalf("gas=%v err=%v", gas, err)
	}
}

func TestStrictTransactionRejectsMalformedRPCResponses(t *testing.T) {
	for _, response := range []string{
		`{"jsonrpc":"2.0","id":2,"result":"0x5208"}`,
		`{"jsonrpc":"1.0","id":1,"result":"0x5208"}`,
		`{"jsonrpc":"2.0","id":1,"result":null}`,
		`{"jsonrpc":"2.0","id":1,"result":"0x5208","error":{"code":3,"message":"execution reverted"}}`,
		`{"jsonrpc":"2.0","id":1,"result":"0x"}`,
		`{"jsonrpc":"2.0","id":1,"result":"5208"}`,
		`{"jsonrpc":"2.0","id":1,"result":"0x00"}`,
	} {
		t.Run(response, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
			defer server.Close()
			_, err := NewClient(server.URL, 0).GasPriceQuote(context.Background())
			if err == nil {
				t.Fatal("malformed observation accepted")
			}
		})
	}
}

func TestBlockReferenceRejectsUnavailableAndMismatchedCoordinates(t *testing.T) {
	for _, result := range []string{`null`, `{"number":"0x12","hash":null}`, `{"number":"0x13","hash":"0x` + strings.Repeat("a", 64) + `"}`} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `{"jsonrpc":"2.0","id":1,"result":%s}`, result)
		}))
		_, err := NewClient(server.URL, 0).BlockReferenceAt(context.Background(), "0x12")
		server.Close()
		if err == nil {
			t.Fatalf("invalid block accepted: %s", result)
		}
	}
}

func TestTransactionRPCErrorClassification(t *testing.T) {
	if !IsExecutionRevert(&RPCError{Code: 3, Message: "execution reverted"}) {
		t.Fatal("revert not recognized")
	}
	if IsExecutionRevert(fmt.Errorf("HTTP 429 execution reverted")) {
		t.Fatal("transport error became a contract revert")
	}
	if IsUnsupportedBlock(&RPCError{Code: -32000, Message: "execution reverted"}) {
		t.Fatal("revert triggered fallback")
	}
	if !IsUnsupportedBlock(&RPCError{Code: -32602, Message: "invalid argument 1: cannot unmarshal object"}) {
		t.Fatal("hash capability error missing")
	}
}
