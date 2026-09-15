package web3auth

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
	secp256k1 "github.com/decred/dcrd/dcrec/secp256k1/v4"
)

// acceptanceChain returns an explicit local-chain target plus the fixture wallet
// deployed for this run. Nothing is defaulted: without the variables the
// capability is UNPROVEN, and with them a broken environment FAILS.
func acceptanceChain(t *testing.T) (chainID int, rpcURL, wallet string, owner *secp256k1.PrivateKey) {
	t.Helper()
	rpcURL = strings.TrimSpace(os.Getenv("WEB3AUTH_TEST_RPC_URL"))
	if rpcURL == "" {
		t.Skip("WEB3AUTH_TEST_RPC_URL unset — on-chain EIP-1271 is UNPROVEN, not passing")
	}
	wallet = strings.TrimSpace(os.Getenv("WEB3AUTH_TEST_EIP1271_ADDRESS"))
	if wallet == "" {
		t.Fatal("WEB3AUTH_TEST_RPC_URL is set but WEB3AUTH_TEST_EIP1271_ADDRESS is not; " +
			"deploy the test fixture first (scripts/acceptance/wp1a-auth-local.sh)")
	}
	rawKey := strings.TrimSpace(os.Getenv("WEB3AUTH_TEST_EIP1271_OWNER_KEY"))
	if rawKey == "" {
		t.Fatal("WEB3AUTH_TEST_EIP1271_OWNER_KEY is required to sign as the fixture's owner")
	}
	keyBytes, err := hex.DecodeString(strings.TrimPrefix(rawKey, "0x"))
	if err != nil || len(keyBytes) != 32 {
		// The key itself is never echoed, only its shape.
		t.Fatal("WEB3AUTH_TEST_EIP1271_OWNER_KEY is not a 32-byte hex private key")
	}
	return 31337, rpcURL, wallet, secp256k1.PrivKeyFromBytes(keyBytes)
}

func acceptanceVerifier(t *testing.T, chainID int, rpcURL string) *RPCContractVerifier {
	t.Helper()
	client := rpc.NewClient(rpcURL, 0)
	if _, err := client.BlockNumber(context.Background()); err != nil {
		t.Fatalf("acceptance chain is configured but unreachable: %v", err)
	}
	return NewRPCContractVerifier(map[int]EthCaller{chainID: client})
}

// TestEIP1271_LiveChainAcceptsTheOwnersSignature proves the encoding, the
// staticcall and the magic-value check against a real deployed account rather
// than a fake caller.
func TestEIP1271_LiveChainAcceptsTheOwnersSignature(t *testing.T) {
	chainID, rpcURL, wallet, owner := acceptanceChain(t)
	verifier := acceptanceVerifier(t, chainID, rpcURL)

	digest := PersonalSignDigest("wp1a acceptance: owner signature")
	ok, err := verifier.IsValidSignature(context.Background(), chainID, wallet,
		digest, signCompactOverDigest(t, owner, digest))
	if err != nil {
		t.Fatalf("isValidSignature: %v", err)
	}
	if !ok {
		t.Error("the fixture wallet rejected its own owner's signature")
	}
}

func TestEIP1271_LiveChainRejectsAStrangersSignature(t *testing.T) {
	chainID, rpcURL, wallet, _ := acceptanceChain(t)
	verifier := acceptanceVerifier(t, chainID, rpcURL)

	stranger, _ := secp256k1.GeneratePrivateKey()
	digest := PersonalSignDigest("wp1a acceptance: stranger signature")
	ok, err := verifier.IsValidSignature(context.Background(), chainID, wallet,
		digest, signCompactOverDigest(t, stranger, digest))
	if err != nil {
		t.Fatalf("isValidSignature: %v", err)
	}
	if ok {
		t.Error("the fixture wallet accepted a signature from a key it does not own")
	}
}

// An address with no code returns "0x". The verifier must read that as a
// rejection, not as an outage it could retry past.
func TestEIP1271_LiveChainTreatsAnEOAAsRejection(t *testing.T) {
	chainID, rpcURL, _, _ := acceptanceChain(t)
	verifier := acceptanceVerifier(t, chainID, rpcURL)

	stranger, _ := secp256k1.GeneratePrivateKey()
	eoa := addrFromPriv(t, stranger)
	digest := PersonalSignDigest("wp1a acceptance: eoa target")

	ok, err := verifier.IsValidSignature(context.Background(), chainID, eoa,
		digest, signCompactOverDigest(t, stranger, digest))
	if err != nil {
		t.Fatalf("err = %v, want nil (the node answered)", err)
	}
	if ok {
		t.Error("an address with no code must not authenticate")
	}
}

// The full login: a SIWE message whose address is the contract account, signed
// by its owner key, verified end to end against the live chain.
func TestService_ContractWalletLoginAgainstLiveChain(t *testing.T) {
	chainID, rpcURL, wallet, owner := acceptanceChain(t)
	svc := NewServiceWithConfig("acceptance-secret", Config{
		AllowedChainIDs:  []int{chainID},
		ContractVerifier: acceptanceVerifier(t, chainID, rpcURL),
	})

	challenge, err := svc.GenerateNonceFor(context.Background(), wallet)
	if err != nil {
		t.Fatalf("nonce: %v", err)
	}
	message := buildMessage(strings.ToLower(wallet), chainID, challenge)
	sig := "0x" + toHex(signCompactOverDigest(t, owner, PersonalSignDigest(message)))

	out, err := svc.VerifySiwe(context.Background(), VerifyInput{Message: message, Signature: sig})
	if err != nil {
		t.Fatalf("contract wallet login: %v", err)
	}
	if !strings.EqualFold(out.Address, wallet) {
		t.Errorf("session address = %q, want %q", out.Address, wallet)
	}
	if out.ChainID != chainID {
		t.Errorf("session chain = %d, want %d", out.ChainID, chainID)
	}
	if out.Token == "" {
		t.Error("no session token issued")
	}
}

func TestService_ContractWalletLoginRejectsAStranger(t *testing.T) {
	chainID, rpcURL, wallet, _ := acceptanceChain(t)
	svc := NewServiceWithConfig("acceptance-secret", Config{
		AllowedChainIDs:  []int{chainID},
		ContractVerifier: acceptanceVerifier(t, chainID, rpcURL),
	})

	challenge, _ := svc.GenerateNonceFor(context.Background(), wallet)
	message := buildMessage(strings.ToLower(wallet), chainID, challenge)
	stranger, _ := secp256k1.GeneratePrivateKey()
	sig := "0x" + toHex(signCompactOverDigest(t, stranger, PersonalSignDigest(message)))

	if _, err := svc.VerifySiwe(context.Background(), VerifyInput{
		Message: message, Signature: sig,
	}); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
}
