package rpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"strings"
)

// TransactionCall describes only the supported execution envelope. Other EVM
// transaction extensions must not be silently dropped into this representation.
type TransactionCall struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Value string `json:"value"`
	Data  string `json:"data"`
}

// BlockReference records the observed canonical coordinates; Pinning controls
// the RPC argument, not whether a later caller may claim a consistent snapshot.
type BlockReference struct {
	Number  string `json:"number"`
	Hash    string `json:"hash"`
	Pinning string `json:"pinning"`
}

var quantityPattern = regexp.MustCompile(`^0x(?:0|[1-9a-fA-F][0-9a-fA-F]*)$`)
var hashPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
var addressPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)

func (b BlockReference) argument() (any, error) {
	switch b.Pinning {
	case "hash-pinned":
		if !hashPattern.MatchString(b.Hash) {
			return nil, errors.New("rpc: invalid block hash")
		}
		return map[string]any{"blockHash": b.Hash, "requireCanonical": true}, nil
	case "number-pinned":
		if !quantityPattern.MatchString(b.Number) {
			return nil, errors.New("rpc: invalid block number")
		}
		return b.Number, nil
	default:
		return nil, errors.New("rpc: pinned block required")
	}
}

func (c TransactionCall) validate() error {
	if !addressPattern.MatchString(c.From) || !addressPattern.MatchString(c.To) || !quantityPattern.MatchString(c.Value) {
		return errors.New("rpc: invalid transaction envelope")
	}
	if !strings.HasPrefix(c.Data, "0x") || len(c.Data) > 2+16*1024*2 {
		return errors.New("rpc: invalid calldata")
	}
	if _, err := hex.DecodeString(c.Data[2:]); err != nil {
		return errors.New("rpc: invalid calldata")
	}
	value, _ := new(big.Int).SetString(c.Value[2:], 16)
	if value.BitLen() > 256 {
		return errors.New("rpc: transaction value exceeds uint256")
	}
	return nil
}

// BlockReferenceAt refuses pending/null/malformed blocks so absence cannot be
// converted into the genesis block or a synthetic canonical hash.
func (c *Client) BlockReferenceAt(ctx context.Context, number string) (BlockReference, error) {
	if number != "latest" && !quantityPattern.MatchString(number) {
		return BlockReference{}, errors.New("rpc: invalid block number")
	}
	raw, err := c.callRawValidated(ctx, "eth_getBlockByNumber", []any{number, false}, true)
	if err != nil {
		return BlockReference{}, err
	}
	var block struct {
		Number string `json:"number"`
		Hash   string `json:"hash"`
	}
	if err := json.Unmarshal(raw, &block); err != nil || !quantityPattern.MatchString(block.Number) || !hashPattern.MatchString(block.Hash) {
		return BlockReference{}, errors.New("rpc: unavailable or malformed block")
	}
	if number != "latest" && !strings.EqualFold(number, block.Number) {
		return BlockReference{}, errors.New("rpc: mismatched block number")
	}
	return BlockReference{Number: block.Number, Hash: block.Hash, Pinning: "hash-pinned"}, nil
}

// EthCallTransaction preserves sender and value, and accepts empty return data
// as success: payable transfers and non-returning methods legitimately return 0x.
func (c *Client) EthCallTransaction(ctx context.Context, call TransactionCall, block BlockReference) ([]byte, error) {
	if err := call.validate(); err != nil {
		return nil, err
	}
	arg, err := block.argument()
	if err != nil {
		return nil, err
	}
	raw, err := c.callRawValidated(ctx, "eth_call", []any{call, arg}, true)
	if err != nil {
		return nil, err
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !strings.HasPrefix(value, "0x") {
		return nil, errors.New("rpc: malformed call result")
	}
	decoded, err := hex.DecodeString(value[2:])
	if err != nil {
		return nil, errors.New("rpc: malformed call result")
	}
	return decoded, nil
}

func (c *Client) EstimateGasTransaction(ctx context.Context, call TransactionCall, block BlockReference) (*big.Int, error) {
	if err := call.validate(); err != nil {
		return nil, err
	}
	arg, err := block.argument()
	if err != nil {
		return nil, err
	}
	return c.strictQuantity(ctx, "eth_estimateGas", []any{call, arg})
}

func (c *Client) GetBalanceAt(ctx context.Context, address string, block BlockReference) (*big.Int, error) {
	if !addressPattern.MatchString(address) {
		return nil, errors.New("rpc: invalid balance address")
	}
	arg, err := block.argument()
	if err != nil {
		return nil, err
	}
	return c.strictQuantity(ctx, "eth_getBalance", []any{address, arg})
}

// GetCodeAt returns the deployed code at a pinned block (LG-03b code
// observation). Empty code is returned as an empty slice, not an error.
func (c *Client) GetCodeAt(ctx context.Context, address string, block BlockReference) ([]byte, error) {
	if !addressPattern.MatchString(address) {
		return nil, errors.New("rpc: invalid code address")
	}
	arg, err := block.argument()
	if err != nil {
		return nil, err
	}
	raw, err := c.callRawValidated(ctx, "eth_getCode", []any{address, arg}, true)
	if err != nil {
		return nil, err
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !strings.HasPrefix(value, "0x") {
		return nil, errors.New("rpc: malformed code result")
	}
	code, err := hex.DecodeString(value[2:])
	if err != nil {
		return nil, errors.New("rpc: malformed code result")
	}
	return code, nil
}

func (c *Client) VerifiedChainID(ctx context.Context) (*big.Int, error) {
	return c.strictQuantity(ctx, "eth_chainId", []any{})
}

func (c *Client) GasPriceQuote(ctx context.Context) (*big.Int, error) {
	return c.strictQuantity(ctx, "eth_gasPrice", []any{})
}

func (c *Client) strictQuantity(ctx context.Context, method string, params []any) (*big.Int, error) {
	raw, err := c.callRawValidated(ctx, method, params, true)
	if err != nil {
		return nil, err
	}
	var value string
	if json.Unmarshal(raw, &value) != nil || !quantityPattern.MatchString(value) {
		return nil, fmt.Errorf("rpc: malformed %s quantity", method)
	}
	out, _ := new(big.Int).SetString(value[2:], 16)
	if out.BitLen() > 256 {
		return nil, errors.New("rpc: quantity exceeds uint256")
	}
	return out, nil
}

// IsUnsupportedBlock signals only explicit RPC capability errors. Transport,
// rate limiting and contract reverts never trigger an unpinned retry.
func IsUnsupportedBlock(err error) bool {
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	if rpcErr.Code == -32601 {
		return true
	}
	if rpcErr.Code != -32602 && rpcErr.Code != -32000 {
		return false
	}
	message := strings.ToLower(rpcErr.Message)
	return strings.Contains(message, "cannot unmarshal object") || strings.Contains(message, "blockhash") || strings.Contains(message, "block hash argument") || strings.Contains(message, "invalid argument 1") || strings.Contains(message, "too many arguments") || strings.Contains(message, "block parameter")
}

func IsExecutionRevert(err error) bool {
	var rpcErr *RPCError
	if !errors.As(err, &rpcErr) {
		return false
	}
	return rpcErr.Code == 3 || ((rpcErr.Code == -32000 || rpcErr.Code == -32015) && strings.Contains(strings.ToLower(rpcErr.Message), "execution reverted"))
}
