package txreview

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

func TestReviewPreservesFrozenDeadlineWithoutIssuingALease(t *testing.T) {
	deadline := "2030-03-17T13:46:40.123+08:00"
	for _, supplied := range []*string{nil, &deadline} {
		resolver := &fixedExecutionResolver{result: ReviewExecutionContext{State: "bound", RegistryRevision: "1"}}
		svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000)}, nil, nil, nil, nil, nil, nil).WithExecutionContext(resolver)
		tx := TxShape{DefaultChainID, positionManagerAddress, "0", "0x"}
		result, err := svc.ReviewTransaction(context.Background(), ReviewInput{OperationType: OpCustom, FromAddress: smokeAddr, Tx: &tx, ValidUntil: supplied})
		if err != nil {
			t.Fatal(err)
		}
		got := result.ExecutionContext.ValidUntil
		if supplied == nil {
			if got != nil {
				t.Fatal("missing caller deadline became a lease")
			}
		} else if got == nil || *got != "2030-03-17T05:46:40.123Z" {
			t.Fatalf("deadline=%v", got)
		}
	}
	for _, invalid := range []string{"", "tomorrow", "99999999999999999999999999999999999999999999999999999999999999999"} {
		if _, err := reviewValidUntil(&invalid); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("accepted %q", invalid)
		}
	}
}

type scopedSemanticRegistry struct {
	semanticRegistry
	supported map[string]bool
}

func (r scopedSemanticRegistry) ContractSupports(chainID int, name, id string) bool {
	return chainID == r.chainID && name == "PositionManager" && r.supported[id]
}

type fixedExecutionResolver struct {
	result ReviewExecutionContext
	calls  int
}

func (r *fixedExecutionResolver) ResolveReview(context.Context, int, string, string) ReviewExecutionContext {
	r.calls++
	return r.result
}

func TestRegistrySemanticsRequireSpecificDeclaredInterface(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	for _, tc := range []struct {
		name     string
		supports map[string]bool
		close    bool
		decoded  bool
	}{
		{"close-only entry cannot authorize open", map[string]bool{"perp.positionManager.close.v1": true}, false, false},
		{"open-only entry cannot authorize close", map[string]bool{"perp.positionManager.open.v1": true}, true, false},
		{"entry without interfaces", map[string]bool{}, false, false},
		{"declared close", map[string]bool{"perp.positionManager.close.v1": true}, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lookup := scopedSemanticRegistry{semanticRegistry{DefaultChainID, positionManagerAddress}, tc.supports}
			tx := TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, tc.close, true, big.NewInt(1), big.NewInt(now.Unix()+60))}
			checks := contractSemanticChecks(tx, lookup, now)
			if decoded := findCheck(checks, "contract-position-call") != nil; decoded != tc.decoded {
				t.Fatalf("decoded=%v checks=%+v", decoded, checks)
			}
			if !tc.decoded && findCheck(checks, "contract-semantics-unknown") == nil {
				t.Fatal("missing unknown semantics warning")
			}
		})
	}
}

func TestUnavailableRegistryNeverFallsBackToLiveDeployment(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	resolver := &fixedExecutionResolver{result: ReviewExecutionContext{State: "unavailable", Reason: "authorityUnavailable"}}
	svc := NewService(&stubRPCSimple{estimateGas: big.NewInt(21000)}, nil, nil, nil, nil, semanticRegistry{DefaultChainID, positionManagerAddress}, nil).WithExecutionContext(resolver)
	svc.now = func() time.Time { return now }
	tx := TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, false, true, big.NewInt(1), big.NewInt(now.Unix()+60))}
	result, err := svc.ReviewTransaction(context.Background(), ReviewInput{OperationType: OpCustom, FromAddress: smokeAddr, ChainID: ptrInt(DefaultChainID), Tx: &tx})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExecutionContext == nil || result.ExecutionContext.State != "unavailable" || resolver.calls != 1 {
		t.Fatalf("context=%+v calls=%d", result.ExecutionContext, resolver.calls)
	}
	if findCheck(result.Checks, "contract-position-call") != nil || findCheck(result.Checks, "contract-semantics-unknown") == nil {
		t.Fatalf("live deployment fallback: %+v", result.Checks)
	}
}
