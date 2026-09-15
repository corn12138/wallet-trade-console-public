package web3auth

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// Challenge is the server-issued half of a SIWE login. It travels with the
// nonce rather than being re-derived on consume: two replicas may be configured
// with different allowed domains, and the replica that issued the challenge is
// the one whose configuration the signature was produced against.
type Challenge struct {
	IssuedAt        time.Time `json:"issuedAt"`
	ExpiresAt       time.Time `json:"expiresAt"`
	Address         string    `json:"address,omitempty"`
	Domain          string    `json:"domain"`
	URI             string    `json:"uri"`
	Statement       string    `json:"statement"`
	AllowedChainIDs []int     `json:"allowedChainIds,omitempty"`
}

// NonceStore holds issued SIWE challenges until exactly one caller consumes
// them.
//
// Consume must be atomic across every process sharing the store: for one
// issued nonce exactly one caller receives the Challenge and every other caller
// receives ErrNonceUnknown. A store that cannot guarantee that must not be
// selected in production — see ResolveNonceStore.
type NonceStore interface {
	Issue(ctx context.Context, nonce string, challenge Challenge) error
	Consume(ctx context.Context, nonce string) (Challenge, error)
	// Kind names the backend for diagnostics ("memory", "redis"). It is
	// reported by /api/status and must never carry connection details.
	Kind() string
	Close() error
}

// MemoryNonceStore keeps challenges in this process only. It is correct for a
// single process and wrong for two: a nonce consumed here is still unconsumed
// in every other replica's map. Production selects it only when explicitly
// told to, and ResolveNonceStore refuses that combination.
type MemoryNonceStore struct {
	mu     sync.Mutex
	nonces map[string]Challenge
}

func NewMemoryNonceStore() *MemoryNonceStore {
	return &MemoryNonceStore{nonces: map[string]Challenge{}}
}

func (s *MemoryNonceStore) Kind() string { return "memory" }

func (s *MemoryNonceStore) Close() error { return nil }

func (s *MemoryNonceStore) Issue(_ context.Context, nonce string, challenge Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.evictExpiredLocked(Now())
	s.nonces[nonce] = challenge
	return nil
}

// Consume removes and returns the challenge. Deletion is the replay guard, so
// a second call for the same nonce reports ErrNonceUnknown.
func (s *MemoryNonceStore) Consume(_ context.Context, nonce string) (Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	challenge, ok := s.nonces[nonce]
	if !ok {
		return Challenge{}, ErrNonceUnknown
	}
	delete(s.nonces, nonce)
	if Now().After(challenge.ExpiresAt) {
		return Challenge{}, ErrNonceExpired
	}
	return challenge, nil
}

func (s *MemoryNonceStore) evictExpiredLocked(now time.Time) {
	for nonce, challenge := range s.nonces {
		if now.After(challenge.ExpiresAt) {
			delete(s.nonces, nonce)
		}
	}
}

// Len reports the number of unconsumed challenges. Test-only.
func (s *MemoryNonceStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.nonces)
}

func encodeChallenge(challenge Challenge) ([]byte, error) {
	return json.Marshal(challenge)
}

func decodeChallenge(raw []byte) (Challenge, error) {
	var challenge Challenge
	if err := json.Unmarshal(raw, &challenge); err != nil {
		return Challenge{}, err
	}
	return challenge, nil
}
