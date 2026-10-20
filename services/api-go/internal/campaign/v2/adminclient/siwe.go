package adminclient

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/sha3"
)

// PendingLogin is the unsigned SIWE challenge. Its nonce is single-use and
// short-lived, so it is safe to keep locally until the external signer answers.
// The CLI never asks for a private key: an operator signs with their own wallet,
// hardware signer or keystore tool, which also covers EIP-1271 contract wallets.
type PendingLogin struct {
	API       string `json:"api"`
	Address   string `json:"address"`
	ChainID   int    `json:"chainId"`
	Message   string `json:"message"`
	ExpiresAt string `json:"expiresAt"`
}

type nonceResponse struct {
	Nonce           string `json:"nonce"`
	Domain          string `json:"domain"`
	URI             string `json:"uri"`
	Statement       string `json:"statement"`
	IssuedAt        string `json:"issuedAt"`
	ExpirationTime  string `json:"expirationTime"`
	AllowedChainIDs []int  `json:"allowedChainIds"`
}

type verifyResponse struct {
	Token            string `json:"token"`
	Address          string `json:"address"`
	ChainID          int    `json:"chainId"`
	SessionExpiresAt string `json:"sessionExpiresAt"`
}

var singleLine = regexp.MustCompile(`^[^\r\n]*$`)
var signaturePattern = regexp.MustCompile(`^0x([0-9a-fA-F]{2})+$`)

func (h Home) pendingPath() string { return filepath.Join(h.Dir, "pending-login.json") }

// PrepareLogin requests a server challenge and renders the exact EIP-4361 text
// the API verifier parses. Every server-supplied line is checked for newlines so
// a hostile challenge cannot smuggle extra fields into the signed message.
func PrepareLogin(ctx context.Context, home Home, client *Client, address string, chainID int) (PendingLogin, error) {
	address = strings.ToLower(address)
	if !walletPattern.MatchString(address) || chainID <= 0 {
		return PendingLogin{}, errors.New("a 0x wallet address and positive chain ID are required")
	}
	raw, _ := json.Marshal(map[string]string{"address": address})
	reply := client.Do(ctx, http.MethodPost, "/auth/nonce", raw, "")
	if reply.Err != nil || reply.Status != http.StatusOK {
		return PendingLogin{}, fmt.Errorf("nonce request failed: %s", reply.Describe())
	}
	var challenge nonceResponse
	if err := json.Unmarshal(reply.Body, &challenge); err != nil || challenge.Nonce == "" || challenge.Domain == "" || challenge.URI == "" {
		return PendingLogin{}, errors.New("nonce response malformed")
	}
	for _, value := range []string{challenge.Nonce, challenge.Domain, challenge.URI, challenge.Statement, challenge.IssuedAt, challenge.ExpirationTime} {
		if !singleLine.MatchString(value) {
			return PendingLogin{}, errors.New("nonce response contains a multi-line field")
		}
	}
	if len(challenge.AllowedChainIDs) > 0 && !containsInt(challenge.AllowedChainIDs, chainID) {
		return PendingLogin{}, fmt.Errorf("chain %d is not accepted by this API", chainID)
	}
	pending := PendingLogin{API: client.Base(), Address: address, ChainID: chainID, ExpiresAt: challenge.ExpirationTime}
	pending.Message = siweMessage(challenge, checksumAddress(address), chainID)
	return pending, writePrivate(home.pendingPath(), pending)
}

func siweMessage(c nonceResponse, address string, chainID int) string {
	lines := []string{c.Domain + " wants you to sign in with your Ethereum account:", address, ""}
	if c.Statement != "" {
		lines = append(lines, c.Statement, "")
	}
	lines = append(lines, "URI: "+c.URI, "Version: 1", "Chain ID: "+strconv.Itoa(chainID), "Nonce: "+c.Nonce, "Issued At: "+c.IssuedAt)
	if c.ExpirationTime != "" {
		lines = append(lines, "Expiration Time: "+c.ExpirationTime)
	}
	return strings.Join(lines, "\n")
}

// CompleteLogin exchanges the external signature for a session and stores it
// in the private home. The response must name the same wallet and chain as the
// challenge; otherwise the token is discarded rather than saved for another identity.
func CompleteLogin(ctx context.Context, home Home, client *Client, signature string) (Credential, error) {
	var pending PendingLogin
	if err := readPrivate(home.pendingPath(), &pending); err != nil {
		return Credential{}, fmt.Errorf("no pending login: %w", err)
	}
	if pending.API != client.Base() {
		return Credential{}, errors.New("pending login belongs to a different API base")
	}
	signature = strings.TrimSpace(signature)
	if !signaturePattern.MatchString(signature) || len(signature) > 64*1024 {
		return Credential{}, errors.New("signature must be 0x-prefixed hex")
	}
	raw, _ := json.Marshal(map[string]string{"message": pending.Message, "signature": signature})
	reply := client.Do(ctx, http.MethodPost, "/auth/verify", raw, "")
	if reply.Err != nil || reply.Status != http.StatusOK {
		return Credential{}, fmt.Errorf("signature verification failed: %s", reply.Describe())
	}
	var session verifyResponse
	if err := json.Unmarshal(reply.Body, &session); err != nil || session.Token == "" {
		return Credential{}, errors.New("verify response malformed")
	}
	expires, err := time.Parse(time.RFC3339, session.SessionExpiresAt)
	if err != nil || strings.ToLower(session.Address) != pending.Address || session.ChainID != pending.ChainID {
		return Credential{}, errors.New("verify response does not match the requested identity")
	}
	credential := Credential{API: pending.API, Address: pending.Address, ChainID: pending.ChainID, Token: session.Token, SessionExpiresAt: expires.UTC().Format(time.RFC3339Nano)}
	if err = home.SaveCredential(credential); err != nil {
		return Credential{}, err
	}
	// The nonce is consumed server-side; removing the challenge prevents a stale
	// message from being signed again by mistake.
	_ = os.Remove(home.pendingPath())
	return credential, nil
}

func containsInt(values []int, target int) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

// checksumAddress renders EIP-55 so wallets display the address they expect.
func checksumAddress(address string) string {
	lower := strings.TrimPrefix(strings.ToLower(address), "0x")
	hash := sha3.NewLegacyKeccak256()
	hash.Write([]byte(lower))
	digest := hex.EncodeToString(hash.Sum(nil))
	out := []byte(lower)
	for i, ch := range out {
		if ch >= 'a' && ch <= 'f' && digest[i] >= '8' {
			out[i] = ch - 32
		}
	}
	return "0x" + string(out)
}
