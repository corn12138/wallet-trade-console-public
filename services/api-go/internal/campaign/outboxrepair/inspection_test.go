package outboxrepair

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"
)

func TestInspectionCursorCanonicalAuthorityBinding(t *testing.T) {
	a := authorityFixture()
	p := &Position{At: requestFixture().Expected.DeadLetterAt, EventID: requestFixture().EventID}
	store := &testStore{page: Page{ObservedAt: p.At, Next: p, Entries: []Entry{}}}
	s := New(store, a)
	listing, err := s.Inspect(context.Background(), "", 10)
	if err != nil || listing.NextCursor == "" {
		t.Fatalf("initial inspection: %v", err)
	}
	if _, err = s.Inspect(context.Background(), listing.NextCursor, 5); err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(listing.NextCursor)
	if strings.Contains(string(raw), a.value.Principal) || strings.Contains(string(raw), a.value.Role) {
		t.Fatal("cursor exposes authority identifiers")
	}
	for _, changed := range []string{
		strings.Replace(string(raw), `"schema":1`, `"schema":1,"schema":1`, 1),
		strings.Replace(string(raw), `"schema":1`, `"Schema":1`, 1),
		strings.Replace(string(raw), `"schema":1`, `"schema":1,"extra":true`, 1),
		" " + string(raw),
	} {
		cursor := base64.RawURLEncoding.EncodeToString([]byte(changed))
		if _, err = s.Inspect(context.Background(), cursor, 10); err != ErrInvalidInput {
			t.Fatal("noncanonical cursor accepted")
		}
	}
	a.value.Version++
	if _, err = s.Inspect(context.Background(), listing.NextCursor, 10); err != ErrInvalidInput {
		t.Fatal("cursor crossed authority versions")
	}
	if until := time.Until(store.deadline); until <= 0 || until > 5*time.Second {
		t.Fatalf("inspection lacks bounded context: %s", until)
	}
}

func TestInspectionRechecksAuthorityAndBounds(t *testing.T) {
	a, store := authorityFixture(), &testStore{}
	s := New(store, a)
	for _, limit := range []int{0, 51, -1} {
		if _, err := s.Inspect(context.Background(), "", limit); err != ErrInvalidInput {
			t.Fatal("unbounded page accepted")
		}
	}
	if store.listReads != 0 {
		t.Fatal("invalid page reached persistence")
	}
	store.beforeList = func() { a.err = ErrUnauthorized }
	if listing, err := s.Inspect(context.Background(), "", 10); err != ErrUnauthorized || listing.Entries != nil {
		t.Fatal("inspection data escaped revocation during query")
	}
	if a.calls[0] != InspectOperation {
		t.Fatal("inspection used another maintenance grant")
	}
}
