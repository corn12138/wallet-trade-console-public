package eventfeed

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
)

func TestObserveRequiresVerifiedOwnerForPrivateStreams(t *testing.T) {
	f := authtest.New(t)
	token, _ := f.Login(t, walletA, 11155111)
	var owners []string
	s := testService(durableoutbox.StreamPage{Head: 4, ObservedAt: observed})
	s.verifier = f.Verifier
	s.resolver = resolveFunc(func(_ context.Context, _, _, owner string) (string, error) {
		owners = append(owners, owner)
		return "stream", nil
	})
	if _, err := s.Observe(context.Background(), nil, "one", "personal"); err == nil {
		t.Fatal("anonymous private observation succeeded")
	} else {
		expectError(t, err, "UNAUTHORIZED")
	}
	for _, family := range []string{"", "drafts", "Personal"} {
		_, err := s.Observe(context.Background(), nil, "one", family)
		expectError(t, err, "CAMPAIGN_INVALID_INPUT")
	}
	_, err := s.Observe(context.Background(), nil, "bad id", "publication")
	expectError(t, err, "CAMPAIGN_INVALID_INPUT")
	_, err = s.Authenticate(context.Background(), token+"x")
	expectError(t, err, "UNAUTHORIZED")
	subject, err := s.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	head, err := s.Observe(context.Background(), subject, "one", "personal")
	if err != nil || head != 4 {
		t.Fatalf("observe: %d %v", head, err)
	}
	if strings.Join(owners, ",") != walletA+","+walletA {
		t.Fatalf("private stream resolved for %v", owners)
	}
	owners = nil
	if head, err = s.Observe(context.Background(), subject, "one", "publication"); err != nil || head != 4 || strings.Join(owners, ",") != "," {
		t.Fatalf("public observation used owner scope: %v %v", owners, err)
	}
	for _, rendered := range []string{fmt.Sprint(subject), fmt.Sprintf("%v %+v %#v", subject, subject, subject), fmt.Sprintf("%v", *subject)} {
		if strings.Contains(rendered, token[:20]) || strings.Contains(rendered, walletA) {
			t.Fatalf("subject rendering disclosed credential or wallet: %s", rendered)
		}
	}
}

// The reader blocks inside the durable read; the test changes session state
// while it is blocked, then releases it. No timing assumptions are involved.
func TestObserveFinalChecksAfterBlockedRead(t *testing.T) {
	for _, mode := range []string{"revoked", "outage", "visibility", "stream"} {
		t.Run(mode, func(t *testing.T) {
			f := authtest.New(t)
			token, sid := f.Login(t, walletA, 11155111)
			entered, release := make(chan struct{}), make(chan struct{})
			calls := 0
			s := &Service{verifier: f.Verifier,
				resolver: resolveFunc(func(context.Context, string, string, string) (string, error) {
					calls++
					if calls > 1 && mode == "visibility" {
						return "", failure("CAMPAIGN_NOT_FOUND", 404)
					}
					if calls > 1 && mode == "stream" {
						return "other", nil
					}
					return "stream", nil
				}),
				reader: readFunc(func(context.Context, string, *int64, int) (durableoutbox.StreamPage, error) {
					close(entered)
					<-release
					return durableoutbox.StreamPage{Head: 9, ObservedAt: observed}, nil
				})}
			subject, err := s.Authenticate(context.Background(), token)
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			var head int64
			family := "personal"
			if mode == "visibility" {
				family = "publication"
			}
			go func() {
				var err error
				head, err = s.Observe(context.Background(), subject, "one", family)
				done <- err
			}()
			<-entered
			switch mode {
			case "revoked":
				f.Sessions.RevokeID(sid)
			case "outage":
				f.Sessions.SetDown(true)
			}
			close(release)
			err = <-done
			want := map[string]string{"revoked": "UNAUTHORIZED", "outage": "WEB3_SESSION_UNAVAILABLE", "visibility": "CAMPAIGN_NOT_FOUND", "stream": "CAMPAIGN_UNAVAILABLE"}[mode]
			expectError(t, err, want)
			if head != 0 {
				t.Fatalf("head disclosed after failed final check: %d", head)
			}
		})
	}
}

func TestObserveRejectsSessionThatNowNamesAnotherIdentity(t *testing.T) {
	f := authtest.New(t)
	token, _ := f.Login(t, walletA, 11155111)
	other, _ := f.Login(t, walletB, 11155111)
	s := testService(durableoutbox.StreamPage{Head: 1, ObservedAt: observed})
	s.verifier = f.Verifier
	subject, err := s.Authenticate(context.Background(), token)
	if err != nil {
		t.Fatal(err)
	}
	forged := *subject
	forged.token = other // a credential for B cannot continue A's subject
	_, err = s.Observe(context.Background(), &forged, "one", "personal")
	expectError(t, err, "UNAUTHORIZED")
}
