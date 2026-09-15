package indexer

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// --- synthetic ABI log builders ------------------------------------------

func word(hexBody string) string {
	if len(hexBody) > 64 {
		panic("word too long")
	}
	return strings.Repeat("0", 64-len(hexBody)) + hexBody
}

func addrWord(addr string) string {
	return word(strings.TrimPrefix(strings.ToLower(addr), "0x"))
}

func uintWord(v *big.Int) string {
	return word(v.Text(16))
}

func boolWord(b bool) string {
	if b {
		return word("1")
	}
	return word("0")
}

// intWord renders a signed value as 32-byte two's complement.
func intWord(v *big.Int) string {
	if v.Sign() >= 0 {
		return uintWord(v)
	}
	max := new(big.Int).Lsh(big.NewInt(1), 256)
	return uintWord(new(big.Int).Add(max, v))
}

const (
	perpAccount    = "0x1111111111111111111111111111111111111111"
	perpIndexToken = "0xffea240cd1eb135c8aa2597ca203efd629ac5fcd"
	perpCollateral = "0x57e554d795a18f3ca0a0e9e03a17ac3c509c3bf8"
)

func increasePositionLog(sizeDelta, collateralDelta, price, fee *big.Int, isLong bool) Log {
	spec := EventSpecByName("IncreasePosition")
	data := "0x" + word("ab") + // key (bytes32)
		addrWord(perpAccount) + addrWord(perpIndexToken) + addrWord(perpCollateral) +
		uintWord(collateralDelta) + uintWord(sizeDelta) + boolWord(isLong) +
		uintWord(price) + uintWord(fee)
	return Log{Address: "0xperp", Topics: []string{spec.Topic0Hex}, Data: data}
}

func decreasePositionLog(sizeDelta, collateralDelta, price, pnl, fee *big.Int, isLong bool) Log {
	spec := EventSpecByName("DecreasePosition")
	data := "0x" + word("ab") +
		addrWord(perpAccount) + addrWord(perpIndexToken) + addrWord(perpCollateral) +
		uintWord(collateralDelta) + uintWord(sizeDelta) + boolWord(isLong) +
		uintWord(price) + intWord(pnl) + uintWord(fee)
	return Log{Address: "0xperp", Topics: []string{spec.Topic0Hex}, Data: data}
}

func liquidatePositionLog(size, collateral, pnl *big.Int, isLong bool) Log {
	spec := EventSpecByName("LiquidatePosition")
	data := "0x" + word("ab") +
		addrWord(perpAccount) + addrWord(perpIndexToken) + boolWord(isLong) +
		uintWord(size) + uintWord(collateral) + intWord(pnl)
	return Log{Address: "0xperp", Topics: []string{spec.Topic0Hex}, Data: data}
}

// --- parser coverage for the new signatures --------------------------------

func TestParse_IncreasePositionDecodesAllArgTypes(t *testing.T) {
	size := big.NewInt(5_000_000)
	col := big.NewInt(1_000_000)
	price := big.NewInt(3_000)
	fee := big.NewInt(9)
	parsed := ParseIndexedLog(increasePositionLog(size, col, price, fee, true))

	if parsed.EventName != "IncreasePosition" {
		t.Fatalf("event = %q", parsed.EventName)
	}
	a := parsed.RuntimeArgs
	if a["account"] != perpAccount {
		t.Errorf("account = %v", a["account"])
	}
	if a["indexToken"] != perpIndexToken {
		t.Errorf("indexToken = %v", a["indexToken"])
	}
	if got, _ := a["isLong"].(bool); !got {
		t.Errorf("isLong = %v", a["isLong"])
	}
	if got := argBigInt(a["sizeDelta"]); got == nil || got.Cmp(size) != 0 {
		t.Errorf("sizeDelta = %v", a["sizeDelta"])
	}
	if key, _ := a["key"].(string); !strings.HasPrefix(key, "0x") || len(key) != 66 {
		t.Errorf("key = %v", a["key"])
	}
	// The actor is the trading account (feeds web3_events.actor_address).
	if parsed.ActorAddress != perpAccount {
		t.Errorf("actor = %q", parsed.ActorAddress)
	}
}

func TestParse_DecreasePositionDecodesNegativePnl(t *testing.T) {
	pnl := big.NewInt(-123_456)
	parsed := ParseIndexedLog(decreasePositionLog(
		big.NewInt(10), big.NewInt(5), big.NewInt(2_900), pnl, big.NewInt(1), false))

	if parsed.EventName != "DecreasePosition" {
		t.Fatalf("event = %q", parsed.EventName)
	}
	got := argBigInt(parsed.RuntimeArgs["pnl"])
	if got == nil || got.Cmp(pnl) != 0 {
		t.Errorf("pnl = %v, want %v (two's-complement int256)", got, pnl)
	}
	// Persisted form keeps the sign as a decimal string.
	if parsed.PersistedArgs["pnl"] != "-123456" {
		t.Errorf("persisted pnl = %v", parsed.PersistedArgs["pnl"])
	}
}

func TestParse_GraduatedAndStakingEvents(t *testing.T) {
	grad := EventSpecByName("Graduated")
	data := "0x" + uintWord(big.NewInt(69_000)) + uintWord(big.NewInt(1_751_800_000)) +
		addrWord(perpCollateral) + uintWord(big.NewInt(1000))
	parsed := ParseIndexedLog(Log{Address: "0xcurve", Topics: []string{grad.Topic0Hex}, Data: data})
	if parsed.EventName != "Graduated" {
		t.Fatalf("event = %q", parsed.EventName)
	}
	if got := argBigInt(parsed.RuntimeArgs["marketCap"]); got == nil || got.Int64() != 69_000 {
		t.Errorf("marketCap = %v", parsed.RuntimeArgs["marketCap"])
	}
	if parsed.RuntimeArgs["lpToken"] != perpCollateral {
		t.Errorf("lpToken = %v", parsed.RuntimeArgs["lpToken"])
	}

	staked := EventSpecByName("Staked")
	topic1 := "0x" + addrWord(perpAccount)
	sParsed := ParseIndexedLog(Log{
		Address: "0xpool",
		Topics:  []string{staked.Topic0Hex, topic1},
		Data:    "0x" + uintWord(big.NewInt(42)),
	})
	if sParsed.EventName != "Staked" || sParsed.ActorAddress != perpAccount {
		t.Fatalf("staked parse = %+v", sParsed)
	}
	row, ok := stakingActionRowFrom(toParsed(t, sParsed), "STAKE")
	if !ok || row.user != perpAccount || row.amount.Int64() != 42 {
		t.Errorf("staking row = %+v ok=%v", row, ok)
	}

	claimed := EventSpecByName("RewardClaimed")
	cParsed := ParseIndexedLog(Log{
		Address: "0xpool",
		Topics:  []string{claimed.Topic0Hex, topic1},
		Data:    "0x" + uintWord(big.NewInt(7)),
	})
	cRow, ok := stakingActionRowFrom(toParsed(t, cParsed), "CLAIM")
	if !ok || cRow.amount.Int64() != 7 {
		t.Errorf("claim row = %+v ok=%v", cRow, ok)
	}
}

// toParsed wraps a ParsedIndexedLog into the sink's ParsedEvent shape.
func toParsed(t *testing.T, p ParsedIndexedLog) ParsedEvent {
	t.Helper()
	return ParsedEvent{
		ChainID:         11155111,
		ContractAddress: "0xpool",
		TxHash:          "0xtx",
		LogIndex:        3,
		BlockNumber:     100,
		Parsed:          p,
	}
}

// --- perp row extraction + position mutation -------------------------------

func TestPerpEventRowFrom_AllKinds(t *testing.T) {
	inc := ParseIndexedLog(increasePositionLog(big.NewInt(100), big.NewInt(10), big.NewInt(3000), big.NewInt(1), true))
	r, ok := perpEventRowFrom(toParsed(t, inc), "INCREASE")
	if !ok || r.sizeDelta.Int64() != 100 || !r.isLong || r.price.Int64() != 3000 {
		t.Fatalf("increase row = %+v ok=%v", r, ok)
	}

	dec := ParseIndexedLog(decreasePositionLog(big.NewInt(40), big.NewInt(4), big.NewInt(2900), big.NewInt(-5), big.NewInt(1), true))
	dr, ok := perpEventRowFrom(toParsed(t, dec), "DECREASE")
	if !ok || dr.pnl.Int64() != -5 {
		t.Fatalf("decrease row = %+v ok=%v", dr, ok)
	}

	liq := ParseIndexedLog(liquidatePositionLog(big.NewInt(60), big.NewInt(6), big.NewInt(-6), true))
	lr, ok := perpEventRowFrom(toParsed(t, liq), "LIQUIDATION")
	if !ok || lr.sizeDelta.Int64() != 60 || lr.collateralDelta.Int64() != 6 || lr.price != nil {
		t.Fatalf("liquidation row = %+v ok=%v", lr, ok)
	}
}

func TestApplyPerpMutation_OpenIncreaseDecreaseClose(t *testing.T) {
	open := perpEventRow{kind: "INCREASE", account: perpAccount, indexToken: perpIndexToken,
		isLong: true, sizeDelta: big.NewInt(100), collateralDelta: big.NewInt(10),
		price: big.NewInt(3000), fee: big.NewInt(0)}
	st, ok := applyPerpMutation(nil, open)
	if !ok || st.size.Int64() != 100 || st.entryPrice.Int64() != 3000 || st.status != "OPEN" {
		t.Fatalf("open = %+v ok=%v", st, ok)
	}

	// Increase at a higher price → size-weighted entry (100·3000 + 100·4000)/200 = 3500.
	more := open
	more.price = big.NewInt(4000)
	st2, ok := applyPerpMutation(&st, more)
	if !ok || st2.size.Int64() != 200 || st2.entryPrice.Int64() != 3500 {
		t.Fatalf("weighted entry = %+v ok=%v", st2, ok)
	}
	if st2.collateral.Int64() != 20 || st2.markPrice.Int64() != 4000 {
		t.Errorf("increase collateral/mark = %v/%v", st2.collateral, st2.markPrice)
	}

	// Partial decrease keeps OPEN and accumulates pnl.
	dec := perpEventRow{kind: "DECREASE", sizeDelta: big.NewInt(50), collateralDelta: big.NewInt(5),
		price: big.NewInt(4200), pnl: big.NewInt(35), fee: big.NewInt(0)}
	st3, ok := applyPerpMutation(&st2, dec)
	if !ok || st3.size.Int64() != 150 || st3.status != "OPEN" || st3.pnl.Int64() != 35 {
		t.Fatalf("partial decrease = %+v ok=%v", st3, ok)
	}

	// Full decrease closes.
	closeAll := perpEventRow{kind: "DECREASE", sizeDelta: big.NewInt(150), collateralDelta: big.NewInt(15),
		price: big.NewInt(4100), pnl: big.NewInt(-10), fee: big.NewInt(0)}
	st4, ok := applyPerpMutation(&st3, closeAll)
	if !ok || st4.size.Sign() != 0 || st4.status != "CLOSED" || st4.pnl.Int64() != 25 {
		t.Fatalf("close = %+v ok=%v", st4, ok)
	}
}

func TestApplyPerpMutation_Liquidation(t *testing.T) {
	current := positionState{size: big.NewInt(80), collateral: big.NewInt(8),
		entryPrice: big.NewInt(3000), markPrice: big.NewInt(2500), pnl: big.NewInt(0), status: "OPEN"}
	liq := perpEventRow{kind: "LIQUIDATION", sizeDelta: big.NewInt(80),
		collateralDelta: big.NewInt(8), pnl: big.NewInt(-8), fee: big.NewInt(0)}
	st, ok := applyPerpMutation(&current, liq)
	if !ok || st.status != "LIQUIDATED" || st.size.Sign() != 0 || st.pnl.Int64() != -8 {
		t.Fatalf("liquidation = %+v ok=%v", st, ok)
	}
}

func TestApplyPerpMutation_DecreaseWithoutPositionRefuses(t *testing.T) {
	dec := perpEventRow{kind: "DECREASE", sizeDelta: big.NewInt(1),
		collateralDelta: big.NewInt(0), price: big.NewInt(1), fee: big.NewInt(0)}
	if _, ok := applyPerpMutation(nil, dec); ok {
		t.Errorf("decrease with no open position applied")
	}
}

func TestDecodeInt256(t *testing.T) {
	pos, _ := hex.DecodeString(word("7b")) // 123
	if got := decodeInt256(pos); got.Int64() != 123 {
		t.Errorf("positive = %v", got)
	}
	negWord := intWord(big.NewInt(-2))
	neg, _ := hex.DecodeString(negWord)
	if got := decodeInt256(neg); got.Int64() != -2 {
		t.Errorf("negative = %v", got)
	}
}

func TestHandlePerpEvent_NoResolverIsRetryable(t *testing.T) {
	// Missing symbol ownership cannot turn a raw event into a completed read
	// model; surfacing the error keeps the caller's checkpoint behind it.
	s := NewDBSink(nil)
	inc := ParseIndexedLog(increasePositionLog(big.NewInt(1), big.NewInt(1), big.NewInt(1), big.NewInt(0), true))
	if err := s.handlePerpEvent(t.Context(), nil, toParsed(t, inc), "INCREASE"); err == nil {
		t.Fatal("missing resolver must remain retryable")
	}
}
