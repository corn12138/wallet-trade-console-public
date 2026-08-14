package indexer

import (
	"encoding/hex"

	internalabi "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

// EventInput describes one field of an indexed event. Mirrors a single
// input in the viem ABI tuple. Order matters: indexed inputs are pulled
// from topics[1..N], non-indexed from log.data in declaration order.
type EventInput struct {
	Name    string
	Type    string // "address", "uint256", "uint112", "string"
	Indexed bool
}

// EventSpec is the parsed Solidity event signature plus the topic0
// keccak that distinguishes it on the wire.
type EventSpec struct {
	Name       string
	Signature  string
	Topic0     [32]byte
	Topic0Hex  string // 0x-prefixed hex, lowercase, 66 chars; used as map key
	Inputs     []EventInput
	ActorField string // arg name to map to ParsedIndexedLog.ActorAddress, "" if none
}

// indexedEvents mirrors INDEXED_EVENTS in
// legacy NestJS indexer/indexer.config.ts. Order doesn't matter at
// runtime — events are looked up by topic0.
var indexedEvents = []EventSpec{
	{
		Name:      "PairCreated",
		Signature: "PairCreated(address,address,address,uint256)",
		Inputs: []EventInput{
			{Name: "token0", Type: "address", Indexed: true},
			{Name: "token1", Type: "address", Indexed: true},
			{Name: "pair", Type: "address"},
			{Name: "3", Type: "uint256"}, // unnamed in Solidity; viem keys it by index
		},
	},
	{
		Name:      "Swap",
		Signature: "Swap(address,uint256,uint256,uint256,uint256,address)",
		Inputs: []EventInput{
			{Name: "sender", Type: "address", Indexed: true},
			{Name: "amount0In", Type: "uint256"},
			{Name: "amount1In", Type: "uint256"},
			{Name: "amount0Out", Type: "uint256"},
			{Name: "amount1Out", Type: "uint256"},
			{Name: "to", Type: "address", Indexed: true},
		},
		ActorField: "sender",
	},
	{
		Name:      "Mint",
		Signature: "Mint(address,uint256,uint256)",
		Inputs: []EventInput{
			{Name: "sender", Type: "address", Indexed: true},
			{Name: "amount0", Type: "uint256"},
			{Name: "amount1", Type: "uint256"},
		},
		ActorField: "sender",
	},
	{
		Name:      "Burn",
		Signature: "Burn(address,uint256,uint256,address)",
		Inputs: []EventInput{
			{Name: "sender", Type: "address", Indexed: true},
			{Name: "amount0", Type: "uint256"},
			{Name: "amount1", Type: "uint256"},
			{Name: "to", Type: "address", Indexed: true},
		},
		ActorField: "sender",
	},
	{
		Name:      "Sync",
		Signature: "Sync(uint112,uint112)",
		Inputs: []EventInput{
			{Name: "reserve0", Type: "uint112"},
			{Name: "reserve1", Type: "uint112"},
		},
	},
	{
		Name:      "Transfer",
		Signature: "Transfer(address,address,uint256)",
		Inputs: []EventInput{
			{Name: "from", Type: "address", Indexed: true},
			{Name: "to", Type: "address", Indexed: true},
			{Name: "value", Type: "uint256"},
		},
		ActorField: "from", // falls back to "to" if from is zero — handled in parser
	},
	{
		Name:      "Approval",
		Signature: "Approval(address,address,uint256)",
		Inputs: []EventInput{
			{Name: "owner", Type: "address", Indexed: true},
			{Name: "spender", Type: "address", Indexed: true},
			{Name: "value", Type: "uint256"},
		},
		ActorField: "owner",
	},
	{
		Name:      "TokenCreated",
		Signature: "TokenCreated(address,address,address,string,string)",
		Inputs: []EventInput{
			{Name: "token", Type: "address", Indexed: true},
			{Name: "bondingCurve", Type: "address", Indexed: true},
			{Name: "creator", Type: "address", Indexed: true},
			{Name: "symbol", Type: "string"},
			{Name: "name", Type: "string"},
		},
		ActorField: "creator",
	},
	{
		Name:      "Buy",
		Signature: "Buy(address,uint256,uint256,uint256)",
		Inputs: []EventInput{
			{Name: "buyer", Type: "address", Indexed: true},
			{Name: "ethIn", Type: "uint256"},
			{Name: "tokensOut", Type: "uint256"},
			{Name: "newPrice", Type: "uint256"},
		},
		ActorField: "buyer",
	},
	{
		Name:      "Sell",
		Signature: "Sell(address,uint256,uint256,uint256)",
		Inputs: []EventInput{
			{Name: "seller", Type: "address", Indexed: true},
			{Name: "tokensIn", Type: "uint256"},
			{Name: "ethOut", Type: "uint256"},
			{Name: "newPrice", Type: "uint256"},
		},
		ActorField: "seller",
	},
	// BondingCurve graduation (contracts/src/launchpad/BondingCurve.sol).
	// No indexed args; the emitting contract IS the bonding curve, which is
	// how the sink resolves the token row.
	{
		Name:      "Graduated",
		Signature: "Graduated(uint256,uint256,address,uint256)",
		Inputs: []EventInput{
			{Name: "marketCap", Type: "uint256"},
			{Name: "timestamp", Type: "uint256"},
			{Name: "lpToken", Type: "address"},
			{Name: "lpAmount", Type: "uint256"},
		},
	},
	// PerpMarket position lifecycle (contracts/src/core/PerpMarket.sol).
	// None of the args are indexed — everything decodes from log.data.
	{
		Name:      "IncreasePosition",
		Signature: "IncreasePosition(bytes32,address,address,address,uint256,uint256,bool,uint256,uint256)",
		Inputs: []EventInput{
			{Name: "key", Type: "bytes32"},
			{Name: "account", Type: "address"},
			{Name: "indexToken", Type: "address"},
			{Name: "collateralToken", Type: "address"},
			{Name: "collateralDelta", Type: "uint256"},
			{Name: "sizeDelta", Type: "uint256"},
			{Name: "isLong", Type: "bool"},
			{Name: "price", Type: "uint256"},
			{Name: "fee", Type: "uint256"},
		},
		ActorField: "account",
	},
	{
		Name:      "DecreasePosition",
		Signature: "DecreasePosition(bytes32,address,address,address,uint256,uint256,bool,uint256,int256,uint256)",
		Inputs: []EventInput{
			{Name: "key", Type: "bytes32"},
			{Name: "account", Type: "address"},
			{Name: "indexToken", Type: "address"},
			{Name: "collateralToken", Type: "address"},
			{Name: "collateralDelta", Type: "uint256"},
			{Name: "sizeDelta", Type: "uint256"},
			{Name: "isLong", Type: "bool"},
			{Name: "price", Type: "uint256"},
			{Name: "pnl", Type: "int256"},
			{Name: "fee", Type: "uint256"},
		},
		ActorField: "account",
	},
	{
		Name:      "LiquidatePosition",
		Signature: "LiquidatePosition(bytes32,address,address,bool,uint256,uint256,int256)",
		Inputs: []EventInput{
			{Name: "key", Type: "bytes32"},
			{Name: "account", Type: "address"},
			{Name: "indexToken", Type: "address"},
			{Name: "isLong", Type: "bool"},
			{Name: "size", Type: "uint256"},
			{Name: "collateral", Type: "uint256"},
			{Name: "pnl", Type: "int256"},
		},
		ActorField: "account",
	},
	// StakingPool lifecycle (contracts/src/launchpad/StakingPool.sol).
	{
		Name:      "Staked",
		Signature: "Staked(address,uint256)",
		Inputs: []EventInput{
			{Name: "user", Type: "address", Indexed: true},
			{Name: "amount", Type: "uint256"},
		},
		ActorField: "user",
	},
	{
		Name:      "Unstaked",
		Signature: "Unstaked(address,uint256)",
		Inputs: []EventInput{
			{Name: "user", Type: "address", Indexed: true},
			{Name: "amount", Type: "uint256"},
		},
		ActorField: "user",
	},
	{
		Name:      "RewardClaimed",
		Signature: "RewardClaimed(address,uint256)",
		Inputs: []EventInput{
			{Name: "user", Type: "address", Indexed: true},
			{Name: "reward", Type: "uint256"},
		},
		ActorField: "user",
	},
	{
		Name:      "EmergencyWithdraw",
		Signature: "EmergencyWithdraw(address,uint256)",
		Inputs: []EventInput{
			{Name: "user", Type: "address", Indexed: true},
			{Name: "amount", Type: "uint256"},
		},
		ActorField: "user",
	},
}

// eventByTopic0 is the runtime lookup index. Populated from indexedEvents
// at init; the keys are lowercase 0x-prefixed 32-byte hashes.
var eventByTopic0 = map[string]*EventSpec{}

func init() {
	for i := range indexedEvents {
		spec := &indexedEvents[i]
		spec.Topic0 = internalabi.EventTopic0(spec.Signature)
		spec.Topic0Hex = "0x" + hex.EncodeToString(spec.Topic0[:])
		eventByTopic0[spec.Topic0Hex] = spec
	}
}

// EventSpecByName returns the registered event by Solidity name, or nil
// if unknown. Useful for tests and event-handler dispatch (Phase 6a.6).
func EventSpecByName(name string) *EventSpec {
	for i := range indexedEvents {
		if indexedEvents[i].Name == name {
			return &indexedEvents[i]
		}
	}
	return nil
}

// EventSpecByTopic0 returns the registered event by lowercase 0x-prefixed
// topic0 hex, or nil if unknown. The 66-char form (0x + 64 hex chars) is
// what eth_getLogs returns directly.
func EventSpecByTopic0(topic0Hex string) *EventSpec {
	return eventByTopic0[topic0Hex]
}
