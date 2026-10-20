package txreview

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/abi"
)

// A spender allow-list cannot establish a contract's ABI. Semantic decoding
// requires the named deployment on the requested chain as well as its selector.
type ContractInterfaceLookup interface {
	ContractSupports(chainID int, name, interfaceID string) bool
}

type ContractAddressLookup interface {
	ContractAddress(chainID int, name string) (string, bool)
}

const (
	openPositionSignature  = "openPosition(address,address,uint256,uint256,bool,uint256,uint256)"
	closePositionSignature = "closePosition(address,address,uint256,uint256,bool,uint256,uint256)"
)

type positionCall struct {
	closing                           bool
	indexToken, collateralToken       string
	collateral, size, price, deadline *big.Int
	isLong                            bool
}

func contractSemanticChecks(tx TxShape, registry ContractAddressLookup, now time.Time) []Check {
	unknown := func(summary string) []Check {
		return []Check{{ID: "contract-semantics-unknown", Status: StatusWarn, Severity: SeverityLow,
			Title: "Contract meaning not established", Summary: summary}}
	}
	if registry == nil {
		return unknown("No named deployment could be resolved for this transaction. Its calldata has not been decoded.")
	}
	address, ok := registry.ContractAddress(tx.ChainID, "PositionManager")
	if !ok || !addressRE.MatchString(address) || address == "0x0000000000000000000000000000000000000000" || !strings.EqualFold(address, tx.To) {
		return unknown("The target is not the registered PositionManager on this chain. Its calldata has not been decoded.")
	}
	// Registry-backed reviews require the specific semantic interface, even if
	// the same address declares another supported PositionManager operation.
	if capabilities, ok := registry.(ContractInterfaceLookup); ok {
		interfaceID := "perp.positionManager.open.v1"
		if strings.HasPrefix(strings.ToLower(tx.Data), "0x"+hex.EncodeToString(abi.Selector(closePositionSignature))) {
			interfaceID = "perp.positionManager.close.v1"
		}
		if !capabilities.ContractSupports(tx.ChainID, "PositionManager", interfaceID) {
			return unknown("The registered target does not declare a supported interface for this operation. Its calldata has not been decoded.")
		}
	}
	call, recognized, valid := decodePositionCall(tx.Data)
	if !recognized {
		return unknown("The registered PositionManager target uses a function outside the supported open/close ABI.")
	}
	if !valid {
		return []Check{{ID: "contract-calldata-invalid", Status: StatusFail, Severity: SeverityHigh,
			Title: "Invalid position calldata", Summary: "The selected PositionManager function does not contain exactly seven canonical ABI arguments."}}
	}
	checks := positionChecks(call, now)
	value := ParseOptionalBigInt(tx.Value)
	if value == nil || value.Sign() != 0 {
		checks = append(checks, Check{ID: "contract-nonpayable-value", Status: StatusFail, Severity: SeverityHigh,
			Title: "Position function rejects native value", Summary: "PositionManager openPosition and closePosition are nonpayable and require zero native transaction value."})
	}
	return checks
}

func decodePositionCall(data string) (positionCall, bool, bool) {
	var call positionCall
	if len(data) < 10 || !strings.HasPrefix(data, "0x") {
		return call, false, false
	}
	selector, err := hex.DecodeString(data[2:10])
	if err != nil {
		return call, false, false
	}
	switch {
	case bytes.Equal(selector, abi.Selector(openPositionSignature)):
	case bytes.Equal(selector, abi.Selector(closePositionSignature)):
		call.closing = true
	default:
		return call, false, false
	}
	// This deliberately accepts only the canonical fixed shape. Extra bytes or
	// noncanonical address/bool words cannot acquire an apparently complete review.
	if len(data) != 2+(4+7*32)*2 {
		return call, true, false
	}
	args, err := hex.DecodeString(data[10:])
	if err != nil || !bytes.Equal(args[:12], make([]byte, 12)) || !bytes.Equal(args[32:44], make([]byte, 12)) {
		return call, true, false
	}
	word := func(i int) *big.Int { return new(big.Int).SetBytes(args[i*32 : (i+1)*32]) }
	flag := word(4)
	if flag.Cmp(big.NewInt(1)) > 0 {
		return call, true, false
	}
	call.indexToken = "0x" + hex.EncodeToString(args[12:32])
	call.collateralToken = "0x" + hex.EncodeToString(args[44:64])
	call.collateral, call.size = word(2), word(3)
	call.isLong = flag.Sign() != 0
	call.price, call.deadline = word(5), word(6)
	return call, true, true
}

func positionChecks(call positionCall, now time.Time) []Check {
	operation := "openPosition"
	collateralFact := fmt.Sprintf("collateralAmount=%s (token base units)", call.collateral.String())
	if call.closing {
		operation = "closePosition"
		// PerpMarket ignores this ABI argument; labeling it as a withdrawal amount
		// would turn decoded intent into a false promise about the eventual payout.
		collateralFact = fmt.Sprintf("collateralDelta=%s (raw ABI argument; ignored by the current repository PerpMarket implementation). This parameter does not control payout. Returned collateral is proportional to the effective sizeDelta, capped at the current position size, with PnL and fees applied; no payout amount is established", call.collateral.String())
	}
	direction := "short"
	if call.isLong {
		direction = "long"
	}
	checks := []Check{{ID: "contract-position-call", Status: StatusPass, Severity: SeverityInfo,
		Title: "Position calldata decoded",
		Summary: fmt.Sprintf("PositionManager.%s: direction=%s; indexToken=%s; collateralToken=%s; %s; sizeDelta=%s (USD scaled by 1e30).",
			operation, direction, call.indexToken, call.collateralToken, collateralFact, call.size.String())}}
	priceCheck := Check{ID: "contract-price-bound", Status: StatusPass, Severity: SeverityInfo,
		Title: "Execution price bound encoded"}
	if call.price.Sign() == 0 {
		priceCheck.Status, priceCheck.Severity = StatusWarn, SeverityHigh
		priceCheck.Title = "Execution price is unbounded"
		priceCheck.Summary = "acceptablePrice=0 disables the PositionManager execution price check. No price protection is encoded."
	} else {
		bound := "minimum"
		if call.isLong != call.closing {
			bound = "maximum"
		}
		priceCheck.Summary = fmt.Sprintf("The encoded %s execution price is %s (USD scaled by 1e30). This is a limit, not a current oracle price or a slippage percentage.", bound, call.price.String())
	}
	checks = append(checks, priceCheck)
	deadlineCheck := Check{ID: "contract-deadline", Status: StatusPass, Severity: SeverityInfo,
		Title: "Deadline encoded", Summary: fmt.Sprintf("The encoded deadline is %s (Unix seconds), not earlier than the review server time %d. The contract evaluates block.timestamp at execution.", call.deadline.String(), now.Unix())}
	if call.deadline.Cmp(big.NewInt(now.Unix())) < 0 {
		deadlineCheck.Status, deadlineCheck.Severity = StatusFail, SeverityHigh
		deadlineCheck.Title = "Encoded deadline expired"
		deadlineCheck.Summary = fmt.Sprintf("The encoded deadline %s (Unix seconds) is earlier than the review server time %d. The contract evaluates block.timestamp at execution.", call.deadline.String(), now.Unix())
	}
	return append(checks, deadlineCheck, Check{ID: "contract-semantics-scope", Status: StatusPass, Severity: SeverityInfo,
		Title: "Decoded intent scope", Summary: "These facts describe the submitted calldata against the registered PositionManager ABI. They do not attest deployed bytecode, current balances, allowance, position ownership, oracle freshness, execution success, or a completed security audit."})
}
