package bridge

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// BridgeGateway ABI selectors and event topics. Generated from the deployed
// contract signatures — see contracts-foundry/src/bridge/BridgeGateway.sol.
const (
	selRoute              = "0xf1784e0c" // route(address,uint256)
	selAvailableLiquidity = "0x181f37c8" // availableLiquidity(address)
	selPaused             = "0x5c975abb" // paused()
	selDeposit            = "0x8b6099db" // deposit(address,uint256,uint256,address)
	selFulfilled          = "0x2aa91bfd" // fulfilled(bytes32)

	// TopicBridgeInitiated is emitted on the SOURCE chain when a user escrows.
	TopicBridgeInitiated = "0x1f6bd835fdfb22a9cecedea9190e8abea8ea7db12959eb0c25f96496857da52c"
	// TopicBridgeFulfilled is emitted on the DESTINATION chain by the relayer.
	TopicBridgeFulfilled = "0x92f9453db747ca6baa3afea0d9202c63506d9908070a663b3dbc6b30f0248d7a"
	// TopicBridgeRefunded is emitted on the SOURCE chain when escrow is returned.
	TopicBridgeRefunded = "0x75ce8ff30eded11949bd149db8bc401d8ed8b3ea153c07c291ee925fa74644bc"
)

// EthCaller is the slice of rpc.Client this package needs, so tests can drive
// every branch without a node.
type EthCaller interface {
	EthCall(ctx context.Context, to, dataHex, block string) ([]byte, error)
}

var _ EthCaller = (*rpc.Client)(nil)

// GatewayRef locates one chain's gateway.
type GatewayRef struct {
	ChainID int    `json:"chainId"`
	Address string `json:"address"`
}

// Registry is the set of chains that have a deployed gateway, plus an RPC
// client per chain.
//
// This is the extension seam: adding a second chain is a deployment-registry
// entry plus an RPC url. No code here changes, and no route is invented —
// whether a route actually works is read from the contract itself.
type Registry struct {
	gateways map[int]GatewayRef
	callers  map[int]EthCaller
}

// NewRegistry builds a registry from the deployments snapshot. A chain without
// a `bridgeGateway` entry is simply absent — it is not an error, it means the
// counterpart has not been deployed yet.
func NewRegistry(chains map[int]deployments.ChainConfig, callers map[int]EthCaller) *Registry {
	reg := &Registry{gateways: map[int]GatewayRef{}, callers: map[int]EthCaller{}}
	for chainID, cfg := range chains {
		addr := deployments.LookupAddress(cfg, "bridgeGateway", "BridgeGateway")
		if addr == "" {
			continue
		}
		reg.gateways[chainID] = GatewayRef{ChainID: chainID, Address: strings.ToLower(addr)}
	}
	for chainID, caller := range callers {
		if caller != nil {
			reg.callers[chainID] = caller
		}
	}
	return reg
}

// Gateway returns the gateway for a chain, if one is deployed.
func (r *Registry) Gateway(chainID int) (GatewayRef, bool) {
	if r == nil {
		return GatewayRef{}, false
	}
	g, ok := r.gateways[chainID]
	return g, ok
}

// Chains lists every chain with a deployed gateway, ascending.
func (r *Registry) Chains() []int {
	if r == nil {
		return nil
	}
	out := make([]int, 0, len(r.gateways))
	for id := range r.gateways {
		out = append(out, id)
	}
	sort.Ints(out)
	return out
}

// caller returns the RPC client for a chain.
func (r *Registry) caller(chainID int) (EthCaller, bool) {
	if r == nil {
		return nil, false
	}
	c, ok := r.callers[chainID]
	return c, ok && c != nil
}

// RouteInfo is the on-chain truth about one route.
type RouteInfo struct {
	Supported bool
	DstToken  string
	MinAmount *big.Int
	// Paused is the source gateway's pause state; a paused gateway rejects
	// deposits, so a route on it is real but temporarily not executable.
	Paused bool
}

// ErrNoGateway means no gateway is deployed on that chain.
var ErrNoGateway = fmt.Errorf("bridge: no gateway deployed on this chain")

// ErrNoRPC means a gateway exists but no RPC client is configured for it, so
// its state cannot be read. Reported as unknown rather than guessed.
var ErrNoRPC = fmt.Errorf("bridge: no RPC configured for this chain")

// Route reads `route(srcToken, dstChainId)` and `paused()` from the SOURCE
// gateway. The contract is the source of truth: the API never claims a route
// is executable because a config file said so.
func (r *Registry) Route(ctx context.Context, srcChainID int, srcToken string, dstChainID int) (RouteInfo, error) {
	gw, ok := r.Gateway(srcChainID)
	if !ok {
		return RouteInfo{}, ErrNoGateway
	}
	caller, ok := r.caller(srcChainID)
	if !ok {
		return RouteInfo{}, ErrNoRPC
	}

	data := selRoute + padAddress(srcToken) + padUint(big.NewInt(int64(dstChainID)))
	raw, err := caller.EthCall(ctx, gw.Address, data, "latest")
	if err != nil {
		return RouteInfo{}, fmt.Errorf("bridge: route(): %w", err)
	}
	if len(raw) < 96 {
		return RouteInfo{}, fmt.Errorf("bridge: short route() response (%d bytes)", len(raw))
	}

	supported, err := rpc.DecodeUint256At(raw, 0)
	if err != nil {
		return RouteInfo{}, err
	}
	dstTokenWord, err := rpc.DecodeUint256At(raw, 32)
	if err != nil {
		return RouteInfo{}, err
	}
	minAmount, err := rpc.DecodeUint256At(raw, 64)
	if err != nil {
		return RouteInfo{}, err
	}

	info := RouteInfo{
		Supported: supported.Sign() != 0,
		DstToken:  wordToAddress(dstTokenWord),
		MinAmount: minAmount,
	}

	// A pause is operational state, not a route property — read it separately
	// so the UI can say "temporarily paused" instead of "unsupported".
	if pausedRaw, pErr := caller.EthCall(ctx, gw.Address, selPaused, "latest"); pErr == nil && len(pausedRaw) >= 32 {
		if v, dErr := rpc.DecodeUint256At(pausedRaw, 0); dErr == nil {
			info.Paused = v.Sign() != 0
		}
	}
	return info, nil
}

// Liquidity reads the DESTINATION gateway's balance of the delivered token.
// A transfer larger than this cannot be fulfilled, so the API reports it
// rather than promising a delivery that would revert.
func (r *Registry) Liquidity(ctx context.Context, dstChainID int, dstToken string) (*big.Int, error) {
	gw, ok := r.Gateway(dstChainID)
	if !ok {
		return nil, ErrNoGateway
	}
	caller, ok := r.caller(dstChainID)
	if !ok {
		return nil, ErrNoRPC
	}

	data := selAvailableLiquidity + padAddress(dstToken)
	raw, err := caller.EthCall(ctx, gw.Address, data, "latest")
	if err != nil {
		return nil, fmt.Errorf("bridge: availableLiquidity(): %w", err)
	}
	return rpc.DecodeUint256At(raw, 0)
}

// BuildDepositCalldata returns the exact calldata for
// `deposit(srcToken, amount, dstChainId, recipient)`.
//
// The API only ever BUILDS this — it never signs or submits. The user's wallet
// signs it, which is what keeps custody with the user.
func BuildDepositCalldata(srcToken string, amount *big.Int, dstChainID int, recipient string) string {
	return selDeposit +
		padAddress(srcToken) +
		padUint(amount) +
		padUint(big.NewInt(int64(dstChainID))) +
		padAddress(recipient)
}

// padAddress left-pads a 20-byte address into a 32-byte ABI word.
func padAddress(addr string) string {
	clean := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(addr), "0x"))
	if len(clean) > 40 {
		clean = clean[len(clean)-40:]
	}
	return strings.Repeat("0", 64-len(clean)) + clean
}

// padUint left-pads an integer into a 32-byte ABI word.
func padUint(v *big.Int) string {
	if v == nil {
		v = big.NewInt(0)
	}
	h := hex.EncodeToString(v.Bytes())
	if h == "" {
		h = "0"
	}
	if len(h)%2 == 1 {
		h = "0" + h
	}
	if len(h) > 64 {
		h = h[len(h)-64:]
	}
	return strings.Repeat("0", 64-len(h)) + h
}

// wordToAddress renders the low 20 bytes of an ABI word as an address.
func wordToAddress(word *big.Int) string {
	if word == nil || word.Sign() == 0 {
		return ""
	}
	b := word.Bytes()
	if len(b) > 20 {
		b = b[len(b)-20:]
	}
	padded := make([]byte, 20)
	copy(padded[20-len(b):], b)
	return "0x" + hex.EncodeToString(padded)
}
