package web3auth

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
)

type failingNonceStore struct {
	NonceStore
	err error
}

func (s failingNonceStore) Issue(context.Context, string, Challenge) error {
	return s.err
}

func (s failingNonceStore) Consume(context.Context, string) (Challenge, error) {
	return Challenge{}, s.err
}

func TestNonceProviderFailureDoesNotExposePayload(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })

	svc, _ := withFixture(t, NewService(0, 0, []int{sessionTestChain}))
	priv, err := secp256k1.GeneratePrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	address := addrFromPriv(t, priv)
	challenge, err := svc.GenerateNonceFor(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	message := buildMessage(address, sessionTestChain, challenge)
	signature := signEthMessage(t, priv, message)
	const marker = "private-provider-payload"
	svc.nonces = failingNonceStore{NonceStore: svc.nonces, err: errors.New(marker + "\n" + message + "\n" + signature)}

	for _, tc := range []struct {
		path, body, event string
	}{
		{"/nonce", `{"address":` + jsonString(address) + `}`, "web3auth nonce issue failed"},
		{"/verify", `{"message":` + jsonString(message) + `,"signature":` + jsonString(signature) + `}`, "web3auth verify failed"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			logs.Reset()
			rec := httptest.NewRecorder()
			Router(svc).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.body)))
			if rec.Code != http.StatusServiceUnavailable || !strings.Contains(logs.String(), tc.event) {
				t.Fatalf("missing fixed outage response or log: status=%d log=%s", rec.Code, logs.String())
			}
			for _, sensitive := range []string{marker, signature, challenge.Nonce} {
				if strings.Contains(logs.String()+rec.Body.String(), sensitive) {
					t.Fatal("provider payload reached telemetry or HTTP response")
				}
			}
		})
	}
}
