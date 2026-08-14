package ethtx

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
)

// anvilKey0 is Anvil's first well-known development key. It is published in
// Foundry's own docs and holds nothing on any real network — it is used here
// precisely because an independent implementation (`cast`) can produce a
// reference signature from the same key.
const (
	anvilKey0  = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
	anvilAddr0 = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
)

func TestAddressDerivation(t *testing.T) {
	s, err := NewSigner(anvilKey0)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	if s.Address() != anvilAddr0 {
		t.Errorf("address: want %s, got %s", anvilAddr0, s.Address())
	}
}

func TestNewSignerRejectsMalformedKeys(t *testing.T) {
	for _, bad := range []string{"", "0x", "deadbeef", strings.Repeat("z", 64)} {
		if _, err := NewSigner(bad); err == nil {
			t.Errorf("key %q should be rejected", bad)
		}
	}
}

// The whole point of this test: sign a transaction with THIS package and
// compare it byte-for-byte with the same transaction signed by `cast mktx`
// (foundry). Hand-rolled RLP + EIP-155 is easy to get subtly wrong in a way
// that still "looks" like a transaction, and only an independent implementation
// catches that.
//
// Reference produced by:
//
//	cast mktx --private-key <anvilKey0> --nonce 7 --gas-price 1000000000 \
//	  --gas-limit 100000 --chain 11155111 --value 0 --legacy \
//	  0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d 0x1234abcd
const castReference = "0xf86c07843b9aca00830186a0942d68a51fb4c3f3ac26fa48b8a457d150132f185d80" +
	"841234abcd8401546d71a0f7576de1f608f59f1cddc686cd4aea312c54e37fa8033c82f0da7ecdcee94117" +
	"a04487aea8364c44f3eefd5a9fb358a0b6a0c923846f26f8902bc61f9d5bda6fd7"

func TestSignedTxMatchesFoundryByteForByte(t *testing.T) {
	s, err := NewSigner(anvilKey0)
	if err != nil {
		t.Fatalf("NewSigner: %v", err)
	}
	data, err := hex.DecodeString("1234abcd")
	if err != nil {
		t.Fatal(err)
	}

	raw, err := s.SignTx(Tx{
		Nonce:    7,
		GasPrice: big.NewInt(1_000_000_000),
		Gas:      100_000,
		To:       "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d",
		Value:    big.NewInt(0),
		Data:     data,
		ChainID:  big.NewInt(11155111),
	})
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	if raw != castReference {
		t.Errorf("signed tx differs from foundry's:\n  got  %s\n  want %s", raw, castReference)
	}
}

// v must carry the chain id. Without EIP-155 the same signature would be
// replayable on every other EVM chain.
func TestChainIDIsBoundIntoV(t *testing.T) {
	s, _ := NewSigner(anvilKey0)
	base := Tx{
		Nonce:    1,
		GasPrice: big.NewInt(1_000_000_000),
		Gas:      21_000,
		To:       "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d",
		Value:    big.NewInt(0),
		ChainID:  big.NewInt(11155111),
	}
	sepolia, err := s.SignTx(base)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}

	base.ChainID = big.NewInt(84532)
	base2, err := s.SignTx(base)
	if err != nil {
		t.Fatalf("SignTx: %v", err)
	}
	if sepolia == base2 {
		t.Fatal("the same transaction signed for two chains produced identical bytes — EIP-155 binding is missing")
	}
}

func TestMissingChainIDOrGasIsRejected(t *testing.T) {
	s, _ := NewSigner(anvilKey0)
	valid := Tx{
		Nonce:    1,
		GasPrice: big.NewInt(1),
		Gas:      21000,
		To:       "0x2d68a51fb4c3f3ac26fa48b8a457d150132f185d",
		ChainID:  big.NewInt(11155111),
	}

	noChain := valid
	noChain.ChainID = nil
	if _, err := s.SignTx(noChain); err == nil {
		t.Error("signing without a chain id must fail (replay protection)")
	}

	noGas := valid
	noGas.Gas = 0
	if _, err := s.SignTx(noGas); err == nil {
		t.Error("signing without a gas limit must fail")
	}

	noPrice := valid
	noPrice.GasPrice = nil
	if _, err := s.SignTx(noPrice); err == nil {
		t.Error("signing without a gas price must fail")
	}
}

func TestBadToAddressRejected(t *testing.T) {
	s, _ := NewSigner(anvilKey0)
	_, err := s.SignTx(Tx{
		Nonce: 1, GasPrice: big.NewInt(1), Gas: 21000,
		To: "0xdeadbeef", ChainID: big.NewInt(1),
	})
	if err == nil {
		t.Error("a short `to` address must be rejected, not silently padded")
	}
}

// ─── RLP primitives ─────────────────────────────────────────────────────────

func TestRLPZeroEncodesAsEmptyString(t *testing.T) {
	// The classic RLP mistake: encoding 0 as 0x00 instead of 0x80. It changes
	// the signing hash, so the network rejects the transaction with a
	// confusing "invalid sender".
	if got := rlpBig(big.NewInt(0)); len(got) != 1 || got[0] != 0x80 {
		t.Errorf("rlp(0): want [0x80], got %x", got)
	}
	if got := rlpBig(nil); len(got) != 1 || got[0] != 0x80 {
		t.Errorf("rlp(nil): want [0x80], got %x", got)
	}
}

func TestRLPSingleLowByteIsItsOwnEncoding(t *testing.T) {
	if got := rlpBytes([]byte{0x7f}); len(got) != 1 || got[0] != 0x7f {
		t.Errorf("rlp(0x7f): want [0x7f], got %x", got)
	}
	// 0x80 is NOT below the threshold, so it gets a length prefix.
	if got := rlpBytes([]byte{0x80}); len(got) != 2 || got[0] != 0x81 || got[1] != 0x80 {
		t.Errorf("rlp(0x80): want [0x81 0x80], got %x", got)
	}
}

func TestRLPLongStringUsesLongForm(t *testing.T) {
	long := make([]byte, 56)
	got := rlpBytes(long)
	// 56 bytes crosses the short-form boundary: prefix becomes 0xb8 + length.
	if got[0] != 0xb8 || got[1] != 56 {
		t.Errorf("long-form prefix: got %x %x", got[0], got[1])
	}
	if len(got) != 58 {
		t.Errorf("length: want 58, got %d", len(got))
	}
}

func TestHashOfSignedTx(t *testing.T) {
	h := Hash(castReference)
	if !strings.HasPrefix(h, "0x") || len(h) != 66 {
		t.Fatalf("unexpected hash shape: %s", h)
	}
	if Hash("not-hex") != "" {
		t.Error("a non-hex input must yield an empty hash, not a garbage one")
	}
}
