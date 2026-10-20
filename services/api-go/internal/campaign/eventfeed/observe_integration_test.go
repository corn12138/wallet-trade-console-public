package eventfeed

import (
	"context"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/auth/authtest"
	v2 "github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/application"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/campaign/v2/postgres"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/durableoutbox"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/web3session"
)

func TestMigratedObserveOwnerIsolationAndBlockedRevocation(t *testing.T) {
	pool := database(t)
	ctx := context.Background()
	sessions := web3session.New(pool)
	f := authtest.New(t, func(p *auth.Policy) { p.Sessions = sessions })
	tokenA, sidA := f.Login(t, walletA, 11155111)
	tokenB, _ := f.Login(t, walletB, 11155111)
	commands := application.NewService(postgres.New(pool), fixtureAuthorization{}, func() v2.Capabilities { return v2.Capabilities{Templates: map[string]bool{"campaignCardV1": true}} })
	revision := seedRevision()
	draft := execute(t, commands, walletA, application.Command{Operation: "create", Revision: &revision})
	s := New(pool, f.Verifier)
	subjectA, err := s.Authenticate(ctx, tokenA)
	if err != nil {
		t.Fatal(err)
	}
	subjectB, err := s.Authenticate(ctx, tokenB)
	if err != nil {
		t.Fatal(err)
	}
	for _, family := range []string{"publication", "interaction", "personal"} {
		_, err := s.Observe(ctx, subjectA, draft.CampaignID, family)
		expectError(t, err, "CAMPAIGN_NOT_FOUND")
	}
	pub := execute(t, commands, walletA, application.Command{Operation: "publish", CampaignID: draft.CampaignID, ExpectedPublication: &draft.PublicationVersion, CandidateRevisionID: draft.RevisionID})
	zero := uint64(0)
	execute(t, commands, walletA, application.Command{Operation: "join", CampaignID: pub.CampaignID, ExpectedPublication: &pub.PublicationVersion, ExpectedState: &zero})
	heads := map[string]int64{}
	for name, subject := range map[string]*Subject{"A": subjectA, "B": subjectB} {
		if heads[name], err = s.Observe(ctx, subject, pub.CampaignID, "personal"); err != nil {
			t.Fatal(err)
		}
	}
	public, err := s.Observe(ctx, nil, pub.CampaignID, "interaction")
	if err != nil || heads["A"] != 1 || heads["B"] != 0 || public != 1 {
		t.Fatalf("heads A=%d B=%d public=%d %v", heads["A"], heads["B"], public, err)
	}
	// Revoke the persisted session while the real repeatable-read query has
	// returned but the observation has not yet run its final check.
	original := s.reader
	entered, release := make(chan struct{}), make(chan struct{})
	s.reader = readFunc(func(ctx context.Context, stream string, after *int64, limit int) (durableoutbox.StreamPage, error) {
		page, err := original.ReadStream(ctx, stream, after, limit)
		close(entered)
		<-release
		return page, err
	})
	done := make(chan error, 1)
	var head int64
	go func() {
		var err error
		head, err = s.Observe(ctx, subjectA, pub.CampaignID, "personal")
		done <- err
	}()
	<-entered
	if _, err := sessions.Revoke(ctx, auth.SessionRef{ID: sidA, Wallet: walletA, ChainID: 11155111, Version: 1, Epoch: f.Policy.Epoch, KeyID: f.Policy.Current.ID}, web3session.ReasonLogout, "self"); err != nil {
		t.Fatal(err)
	}
	close(release)
	expectError(t, <-done, "UNAUTHORIZED")
	if head != 0 {
		t.Fatal("revoked observation returned a head")
	}
	var receipts int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM consumer_receipts`).Scan(&receipts); err != nil || receipts != 0 {
		t.Fatalf("observation wrote receipts: %d %v", receipts, err)
	}
}
