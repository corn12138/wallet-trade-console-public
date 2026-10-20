package executionregistry

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"strconv"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	"golang.org/x/crypto/sha3"
)

// RPCCodeReader observes code through one RPC client per chain. Each client's
// chain ID is verified before it is trusted, and the code is read at a
// hash-pinned block so the observation names exactly where it was made (D3).
type RPCCodeReader struct {
	Clients map[int]*rpc.Client
}

func (r RPCCodeReader) CodeAt(ctx context.Context, chainID int, address string) (CodeObservation, error) {
	client := r.Clients[chainID]
	if client == nil {
		return CodeObservation{}, fmt.Errorf("no RPC configured for chain %d", chainID)
	}
	reported, err := client.VerifiedChainID(ctx)
	if err != nil {
		return CodeObservation{}, fmt.Errorf("chain %d RPC code observation failed", chainID)
	}
	if reported.Cmp(big.NewInt(int64(chainID))) != 0 {
		return CodeObservation{}, fmt.Errorf("RPC for chain %d reports chain %s", chainID, reported)
	}
	block, err := client.BlockReferenceAt(ctx, "latest")
	if err != nil {
		return CodeObservation{}, fmt.Errorf("chain %d RPC code observation failed", chainID)
	}
	code, err := client.GetCodeAt(ctx, address, block)
	if err != nil {
		return CodeObservation{}, fmt.Errorf("chain %d RPC code observation failed", chainID)
	}
	if len(block.Number) < 3 || block.Number[:2] != "0x" {
		return CodeObservation{}, fmt.Errorf("chain %d RPC block number is invalid", chainID)
	}
	number, err := strconv.ParseUint(block.Number[2:], 16, 64)
	if err != nil {
		return CodeObservation{}, fmt.Errorf("chain %d RPC code observation failed", chainID)
	}
	return CodeObservation{ChainID: chainID, Address: address, CodeHash: KeccakHex(code), BlockNumber: number, BlockHash: lower(block.Hash)}, nil
}

// KeccakHex is 0x + keccak256(data).
func KeccakHex(data []byte) string {
	h := sha3.NewLegacyKeccak256()
	h.Write(data)
	return "0x" + hex.EncodeToString(h.Sum(nil))
}

// Revalidate re-reads every entry's code at the current head and refuses any
// difference (CODE_CHANGED). The observations are stored in the activation
// record; they prove the code at those blocks only.
func Revalidate(ctx context.Context, s Snapshot, code CodeReader) ([]CodeObservation, error) {
	if code == nil {
		return nil, ErrCodeUnverifiable
	}
	var out []CodeObservation
	for _, e := range s.Entries {
		obs, err := code.CodeAt(ctx, e.ChainID, e.Address)
		if err != nil {
			return nil, fmt.Errorf("%w: chain %d %s: %v", ErrCodeUnverifiable, e.ChainID, e.Name, err)
		}
		if obs.ChainID != e.ChainID || lower(obs.Address) != e.Address || !codeHashPattern.MatchString(obs.BlockHash) {
			return nil, fmt.Errorf("%w: chain %d %s", ErrCodeUnverifiable, e.ChainID, e.Name)
		}
		if obs.CodeHash != e.CodeHash {
			return nil, fmt.Errorf("%w: chain %d %s", ErrCodeChanged, e.ChainID, e.Name)
		}
		out = append(out, obs)
	}
	return out, nil
}

var (
	ErrCodeChanged      = fmt.Errorf("executionregistry: CODE_CHANGED")
	ErrCodeUnverifiable = fmt.Errorf("executionregistry: code could not be re-read")
)
