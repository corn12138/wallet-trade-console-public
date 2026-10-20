package settings

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type visibilityFixture struct {
	prefs                   *UserPreferences
	hidden                  []HiddenAsset
	prefsErr, hiddenErr     error
	prefsOwner, hiddenOwner string
	chainID                 *int
	hiddenRead              bool
}

func (f *visibilityFixture) FindUserPreferences(_ context.Context, owner string) (*UserPreferences, error) {
	f.prefsOwner = owner
	return f.prefs, f.prefsErr
}

func (f *visibilityFixture) ListHiddenAssets(_ context.Context, owner string, chainID *int) ([]HiddenAsset, error) {
	f.hiddenOwner, f.chainID, f.hiddenRead = owner, chainID, true
	return f.hidden, f.hiddenErr
}

func TestPortfolioVisibility_UnconfiguredUserUsesDefaultsOnlyAfterSuccessfulReads(t *testing.T) {
	f := &visibilityFixture{}
	chain := 11155111
	prefs, hidden, err := readPortfolioVisibility(context.Background(), f, "0xABCDEF1234567890ABCDEF1234567890ABCDEF12", &chain)
	if err != nil {
		t.Fatal(err)
	}
	if prefs.Source != "default" || prefs.SpamFilterLevel != "standard" || hidden == nil || len(hidden) != 0 {
		t.Fatalf("prefs=%+v hidden=%+v", prefs, hidden)
	}
	if f.prefsOwner != "0xabcdef1234567890abcdef1234567890abcdef12" || f.hiddenOwner != f.prefsOwner || f.chainID != &chain || !f.hiddenRead {
		t.Fatalf("read scope=%+v", f)
	}
}

func TestPortfolioVisibility_PreservesStoredRules(t *testing.T) {
	f := &visibilityFixture{prefs: &UserPreferences{Source: "stored", SpamFilterLevel: "strict"}, hidden: []HiddenAsset{{AssetKey: "hidden-token"}}}
	prefs, hidden, err := readPortfolioVisibility(context.Background(), f, validAddr, nil)
	if err != nil || prefs != *f.prefs || !reflect.DeepEqual(hidden, f.hidden) {
		t.Fatalf("prefs=%+v hidden=%v err=%v", prefs, hidden, err)
	}
}

func TestPortfolioVisibility_FailuresNeverSubstituteDefaults(t *testing.T) {
	for _, failure := range []error{ErrPoolUnavailable, errors.New("query failure")} {
		for _, where := range []string{"preferences", "hidden"} {
			f := &visibilityFixture{}
			if where == "preferences" {
				f.prefsErr = failure
			} else {
				f.hiddenErr = failure
			}
			prefs, hidden, err := readPortfolioVisibility(context.Background(), f, validAddr, nil)
			if !errors.Is(err, failure) || prefs.Source != "" || hidden != nil {
				t.Fatalf("%s failure returned prefs=%+v hidden=%v err=%v", where, prefs, hidden, err)
			}
			if where == "preferences" && f.hiddenRead {
				t.Fatal("unexpected hidden read after preferences failure")
			}
		}
	}
}

func TestPortfolioVisibility_NilPoolIsStrictWhileExistingSettingsReadsDegrade(t *testing.T) {
	svc := NewService(NewRepository(nil))
	if _, _, err := svc.GetPortfolioVisibility(context.Background(), validAddr, nil); !errors.Is(err, ErrPoolUnavailable) {
		t.Fatalf("strict err=%v", err)
	}
	prefs, err := svc.GetPreferences(context.Background(), validAddr)
	if err != nil || prefs.Source != "default" {
		t.Fatalf("legacy prefs=%+v err=%v", prefs, err)
	}
	hidden, err := svc.ListHiddenAssets(context.Background(), validAddr, nil)
	if err != nil || len(hidden) != 0 {
		t.Fatalf("legacy hidden=%v err=%v", hidden, err)
	}
	if _, _, err := NewService(nil).GetPortfolioVisibility(context.Background(), validAddr, nil); !errors.Is(err, ErrPoolUnavailable) {
		t.Fatalf("nil repo err=%v", err)
	}
}

func TestPortfolioVisibility_InvalidOwnerDoesNotReadRules(t *testing.T) {
	f := &visibilityFixture{}
	if _, _, err := readPortfolioVisibility(context.Background(), f, "invalid", nil); !errors.Is(err, ErrInvalidOwnerAddr) {
		t.Fatalf("err=%v", err)
	}
	if f.prefsOwner != "" || f.hiddenRead {
		t.Fatal("invalid owner queried preferences")
	}
}
