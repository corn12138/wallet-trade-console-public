package txreview

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"
)

type chainRPC struct {
	stubRPCSimple
	chain      *big.Int
	err        error
	reads      int
	chainReads int
}

func (s *chainRPC) ChainID(context.Context) (*big.Int, error) {
	s.chainReads++
	return s.chain, s.err
}
func (s *chainRPC) EstimateGas(context.Context, string, string, string, string) (*big.Int, error) {
	s.reads++
	return big.NewInt(21000), nil
}
func (s *chainRPC) EthCall(context.Context, string, string, string) ([]byte, error) {
	s.reads++
	return make([]byte, 32), nil
}
func (s *chainRPC) GetGasPrice(context.Context) (*big.Int, error) {
	s.reads++
	return big.NewInt(1), nil
}
func (s *chainRPC) GetBalance(context.Context, string, string) (*big.Int, error) {
	s.reads++
	return big.NewInt(1), nil
}

func TestReviewRPCChainMismatchNeverUsesRemoteState(t *testing.T) {
	for _, tc := range []struct {
		name  string
		chain *big.Int
		err   error
	}{
		{"mismatch", big.NewInt(84532), nil},
		{"unavailable", nil, errors.New("private RPC address must not leak")},
		{"empty", nil, nil},
		{"overflow", new(big.Int).Lsh(big.NewInt(1), 100), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rpc := &chainRPC{chain: tc.chain, err: tc.err}
			svc := NewService(rpc, nil, nil, nil, nil, nil, nil)
			out, err := svc.ReviewTransaction(context.Background(), ReviewInput{OperationType: OpRevokeApproval, FromAddress: smokeAddr,
				ChainID: ptrInt(DefaultChainID), TokenAddress: ptrStr(smokeAddr), Spender: ptrStr(positionManagerAddress)})
			if err != nil || out.ReviewStatus != ReviewSimulationUnavailable || out.Simulation.Mode != SimulationModeFallback {
				t.Fatalf("want simulation-unavailable: %+v, %v", out, err)
			}
			if rpc.reads != 0 || out.AllowanceChange == nil || out.AllowanceChange.CurrentAllowance != nil || out.Simulation.GasEstimateString != nil {
				t.Fatalf("unverified-chain state leaked: reads=%d review=%+v", rpc.reads, out)
			}
			if findCheck(out.Checks, "simulation-chain-unverified") == nil {
				t.Fatal("missing chain evidence limitation")
			}
		})
	}
}

func TestReviewRPCChainRequiredAndMatched(t *testing.T) {
	for _, caller := range []RPCCaller{nil, &stubRPC{}} {
		verified, check := rpcForReviewChain(context.Background(), caller, DefaultChainID)
		if verified != nil || check == nil {
			t.Fatal("absent chain capability must fail closed")
		}
	}
	rpc := &chainRPC{chain: big.NewInt(DefaultChainID)}
	verified, check := rpcForReviewChain(context.Background(), rpc, DefaultChainID)
	if verified != rpc || check != nil {
		t.Fatal("matched RPC should be used unchanged")
	}
}

func TestUnknownRPCChainNeverSoftensDeterministicBlock(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	base := TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, false, true, big.NewInt(1), big.NewInt(now.Unix()+60))}
	for _, tc := range []struct {
		name, checkID string
		mutate        func(*TxShape)
	}{
		{"expired", "contract-deadline", func(tx *TxShape) { tx.Data = positionCalldata(t, false, true, big.NewInt(1), big.NewInt(now.Unix()-1)) }},
		{"nonpayable", "contract-nonpayable-value", func(tx *TxShape) { tx.Value = "1" }},
		{"malformed", "contract-calldata-invalid", func(tx *TxShape) { tx.Data = tx.Data[:len(tx.Data)-2] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(nil, nil, nil, nil, nil, semanticRegistry{DefaultChainID, positionManagerAddress}, nil)
			svc.now = func() time.Time { return now }
			tx := base
			tc.mutate(&tx)
			out, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr, Tx: &tx})
			if err != nil || out.ReviewStatus != ReviewBlocked || out.Simulation.Mode != SimulationModeFallback {
				t.Fatalf("chain unavailability softened deterministic block: %+v %v", out, err)
			}
			if check := findCheck(out.Checks, tc.checkID); check == nil || check.Status != StatusFail {
				t.Fatalf("failure evidence lost: %+v", check)
			}
		})
	}
}

type revertingReviewRPC struct{ stubRPC }

func (r *revertingReviewRPC) ChainID(context.Context) (*big.Int, error) {
	return big.NewInt(DefaultChainID), nil
}

func TestDecodedPositionDoesNotApproveRevertingSimulation(t *testing.T) {
	rpc := &revertingReviewRPC{stubRPC{estimateErr: errors.New("execution reverted"), ethCallErr: errors.New("execution reverted")}}
	svc := NewService(rpc, nil, nil, nil, nil, semanticRegistry{DefaultChainID, positionManagerAddress}, nil)
	tx := TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, false, true, big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 100))}
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr, Tx: &tx})
	if err != nil || out.ReviewStatus == ReviewApproved || out.Simulation.CallSucceeded == nil || *out.Simulation.CallSucceeded {
		t.Fatalf("decoding is not proof of successful execution: %+v %v", out, err)
	}
	if findCheck(out.Checks, "contract-position-call") == nil {
		t.Fatal("simulation outcome should retain decoded intent evidence")
	}
}

func TestSuccessfulGasEstimateCannotHideFailedReadCall(t *testing.T) {
	rpc := &revertingReviewRPC{stubRPC{estimateGas: big.NewInt(21000), gasPrice: big.NewInt(1), balance: big.NewInt(100000), ethCallErr: errors.New("execution reverted")}}
	svc := NewService(rpc, nil, nil, nil, nil, semanticRegistry{DefaultChainID, positionManagerAddress}, nil)
	tx := TxShape{DefaultChainID, positionManagerAddress, "0", positionCalldata(t, false, true, big.NewInt(1), new(big.Int).Lsh(big.NewInt(1), 100))}
	out, err := svc.ReviewTransaction(context.Background(), ReviewInput{FromAddress: smokeAddr, Tx: &tx})
	if err != nil || out.ReviewStatus != ReviewWarning || out.Simulation.GasEstimateString == nil || out.Simulation.EstimatedFeeString == nil {
		t.Fatalf("gas estimate concealed read-call failure: %+v %v", out, err)
	}
	if check := findCheck(out.Checks, "simulation-call-failed"); check == nil || check.Status != StatusWarn {
		t.Fatalf("missing safe failure observation: %+v", check)
	}
}

func TestUnsupportedOperationCannotBypassContractChecks(t *testing.T) {
	for _, op := range []OperationType{"position", "OPEN", " custom"} {
		rpc := &chainRPC{chain: big.NewInt(DefaultChainID)}
		svc := NewService(rpc, nil, nil, nil, nil, semanticRegistry{DefaultChainID, positionManagerAddress}, nil)
		tx := TxShape{DefaultChainID, positionManagerAddress, "1", positionCalldata(t, false, true, big.NewInt(0), big.NewInt(0))}
		_, err := svc.ReviewTransaction(context.Background(), ReviewInput{OperationType: op, FromAddress: smokeAddr, Tx: &tx})
		if !errors.Is(err, ErrInvalidInput) || rpc.chainReads != 0 || rpc.reads != 0 {
			t.Fatalf("unsupported operation %q reached review/RPC: err=%v chainReads=%d reads=%d", op, err, rpc.chainReads, rpc.reads)
		}
	}
}
