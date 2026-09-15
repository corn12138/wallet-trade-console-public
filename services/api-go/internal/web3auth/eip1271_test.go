package web3auth

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

// fakeCaller records the call and replays a scripted answer.
type fakeCaller struct {
	out     []byte
	err     error
	lastTo  string
	lastHex string
	calls   int
}

func (f *fakeCaller) EthCall(_ context.Context, to, dataHex, _ string) ([]byte, error) {
	f.calls++
	f.lastTo = to
	f.lastHex = dataHex
	return f.out, f.err
}

func magicWord() []byte {
	out := make([]byte, 32)
	copy(out, []byte{0x16, 0x26, 0xba, 0x7e})
	return out
}

func TestEncodeIsValidSignature_MatchesABILayout(t *testing.T) {
	digest := make([]byte, 32)
	for i := range digest {
		digest[i] = byte(i + 1)
	}
	sig := []byte{0xaa, 0xbb, 0xcc} // 3 bytes: exercises the tail padding

	got := encodeIsValidSignature(digest, sig)

	if !strings.HasPrefix(got, "0x1626ba7e") {
		t.Fatalf("selector missing: %s", got)
	}
	body := got[len("0x1626ba7e"):]
	if len(body) != 2*(32+32+32+32) {
		t.Fatalf("encoded body = %d hex chars, want %d", len(body), 2*(32*4))
	}
	if body[:64] != hex.EncodeToString(digest) {
		t.Errorf("digest word wrong: %s", body[:64])
	}
	if body[64:128] != strings.Repeat("0", 62)+"40" {
		t.Errorf("bytes offset word = %s, want 0x40", body[64:128])
	}
	if body[128:192] != strings.Repeat("0", 62)+"03" {
		t.Errorf("length word = %s, want 3", body[128:192])
	}
	if body[192:] != "aabbcc"+strings.Repeat("0", 58) {
		t.Errorf("signature word not right-padded: %s", body[192:])
	}
}

func TestEncodeIsValidSignature_ExactWordMultipleGetsNoExtraPadding(t *testing.T) {
	got := encodeIsValidSignature(make([]byte, 32), make([]byte, 64))
	// selector + digest + offset + length + 2 signature words
	want := 2 + 8 + 2*(32*3+64)
	if len(got) != want {
		t.Errorf("len = %d, want %d", len(got), want)
	}
}

func TestIsValidSignature_AcceptsOnlyTheExactMagicWord(t *testing.T) {
	digest := make([]byte, 32)
	cases := []struct {
		name string
		out  []byte
		want bool
	}{
		{"magic value", magicWord(), true},
		{"wrong selector", append([]byte{0xff, 0xff, 0xff, 0xff}, make([]byte, 28)...), false},
		{"magic with dirty padding", append(magicWord()[:31], 0x01), false},
		{"short return", []byte{0x16, 0x26, 0xba, 0x7e}, false},
		{"empty return", nil, false},
		{"long return", append(magicWord(), 0x00), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caller := &fakeCaller{out: tc.out}
			v := NewRPCContractVerifier(map[int]EthCaller{11155111: caller})
			ok, err := v.IsValidSignature(context.Background(), 11155111,
				"0x"+strings.Repeat("ab", 20), digest, []byte{0x01})
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if ok != tc.want {
				t.Errorf("valid = %v, want %v", ok, tc.want)
			}
		})
	}
}

func TestIsValidSignature_UnsupportedChainFailsClosed(t *testing.T) {
	v := NewRPCContractVerifier(map[int]EthCaller{11155111: &fakeCaller{out: magicWord()}})
	if v.SupportsChain(1) {
		t.Fatal("mainnet must not be supported by an empty entry")
	}
	_, err := v.IsValidSignature(context.Background(), 1,
		"0x"+strings.Repeat("ab", 20), make([]byte, 32), []byte{0x01})
	if !errors.Is(err, ErrContractVerificationUnavailable) {
		t.Errorf("err = %v, want ErrContractVerificationUnavailable", err)
	}
}

// An empty eth_call result is what an EOA or an undeployed address returns.
// The node answered, so it is a rejection — not an error the caller might
// mistake for a transient outage.
func TestIsValidSignature_EmptyResultIsRejectionNotOutage(t *testing.T) {
	caller := &fakeCaller{err: rpc.ErrEmpty}
	v := NewRPCContractVerifier(map[int]EthCaller{31337: caller})
	ok, err := v.IsValidSignature(context.Background(), 31337,
		"0x"+strings.Repeat("ab", 20), make([]byte, 32), []byte{0x01})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("empty return must not authenticate")
	}
}

func TestIsValidSignature_RevertIsRejection(t *testing.T) {
	caller := &fakeCaller{err: &rpc.RPCError{Code: 3, Message: "execution reverted"}}
	v := NewRPCContractVerifier(map[int]EthCaller{31337: caller})
	ok, err := v.IsValidSignature(context.Background(), 31337,
		"0x"+strings.Repeat("ab", 20), make([]byte, 32), []byte{0x01})
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if ok {
		t.Error("a reverting wallet must not authenticate")
	}
}

// A node that cannot be reached is not the wallet saying no. Reporting it as a
// rejection would tell the user their signature was invalid when it was never
// evaluated.
func TestIsValidSignature_TransportFailureIsNotARejection(t *testing.T) {
	caller := &fakeCaller{err: errors.New("rpc: Post request failed: connection reset")}
	v := NewRPCContractVerifier(map[int]EthCaller{31337: caller})
	_, err := v.IsValidSignature(context.Background(), 31337,
		"0x"+strings.Repeat("ab", 20), make([]byte, 32), []byte{0x01})
	if !errors.Is(err, ErrContractVerificationUnreachable) {
		t.Errorf("err = %v, want ErrContractVerificationUnreachable", err)
	}
}

func TestIsValidSignature_RejectsMalformedInputs(t *testing.T) {
	v := NewRPCContractVerifier(map[int]EthCaller{31337: &fakeCaller{out: magicWord()}})
	if _, err := v.IsValidSignature(context.Background(), 31337,
		"0x"+strings.Repeat("ab", 20), make([]byte, 31), []byte{0x01}); err == nil {
		t.Error("a 31-byte digest must be rejected")
	}
	if _, err := v.IsValidSignature(context.Background(), 31337,
		"not-an-address", make([]byte, 32), []byte{0x01}); err == nil {
		t.Error("a malformed address must be rejected")
	}
}

func TestPersonalSignDigest_MatchesTheEOARecoveryPath(t *testing.T) {
	// The two authentication paths must authenticate the identical bytes;
	// this pins the digest the EOA path hashes internally.
	message := "example.com wants you to sign in"
	prefix := "\x19Ethereum Signed Message:\n" + itoa(len(message))
	want := keccak256([]byte(prefix + message))
	if hex.EncodeToString(PersonalSignDigest(message)) != hex.EncodeToString(want) {
		t.Error("PersonalSignDigest diverges from the EIP-191 hash used by RecoverAddress")
	}
}
