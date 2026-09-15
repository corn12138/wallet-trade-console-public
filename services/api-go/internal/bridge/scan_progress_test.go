package bridge

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"testing"

	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/deployments"
	"github.com/corn12138/wallet-trade-console-public/services/api-go/internal/rpc"
)

const scanTestChainID = 11155111

type scanTestClient struct {
	*fakeChainClient
	head    uint64
	logs    []rpc.Log
	filters []rpc.LogFilter
}

func (c *scanTestClient) BlockNumber(context.Context) (*big.Int, error) {
	return new(big.Int).SetUint64(c.head), nil
}

func (c *scanTestClient) GetLogs(_ context.Context, filter rpc.LogFilter) ([]rpc.Log, error) {
	c.filters = append(c.filters, filter)
	out := make([]rpc.Log, 0, len(c.logs))
	for _, lg := range c.logs {
		if lg.BlockNumber >= filter.FromBlock && lg.BlockNumber <= filter.ToBlock {
			out = append(out, lg)
		}
	}
	return out, nil
}

type scanFailureState struct {
	attempts int
	resolved bool
	block    uint64
}

type memoryScanProgress struct {
	next       map[int]uint64
	found      map[int]bool
	saves      []uint64
	failures   map[string]*scanFailureState
	saveErrAt  uint64
	saveErr    error
	loadErr    error
	recordErr  error
	resolveErr error
}

func newMemoryScanProgress() *memoryScanProgress {
	return &memoryScanProgress{
		next:     map[int]uint64{},
		found:    map[int]bool{},
		failures: map[string]*scanFailureState{},
	}
}

func (m *memoryScanProgress) LoadScanProgress(_ context.Context, chainID int) (uint64, bool, error) {
	if m.loadErr != nil {
		return 0, false, m.loadErr
	}
	return m.next[chainID], m.found[chainID], nil
}

func (m *memoryScanProgress) SaveScanProgress(_ context.Context, chainID int, nextBlock uint64) error {
	if m.saveErr != nil && nextBlock == m.saveErrAt {
		return m.saveErr
	}
	for key, state := range m.failures {
		if strings.HasPrefix(key, fmt.Sprintf("%d/", chainID)) && !state.resolved && state.block < nextBlock {
			return ErrUnresolvedLogFailure
		}
	}
	m.saves = append(m.saves, nextBlock)
	if !m.found[chainID] || nextBlock > m.next[chainID] {
		m.next[chainID] = nextBlock
	}
	m.found[chainID] = true
	return nil
}

func (m *memoryScanProgress) RecordLogFailure(_ context.Context, chainID int, lg rpc.Log, _ string) error {
	if m.recordErr != nil {
		return m.recordErr
	}
	key := scanFailureKey(chainID, lg)
	state := m.failures[key]
	if state == nil {
		state = &scanFailureState{block: lg.BlockNumber}
		m.failures[key] = state
	}
	state.attempts++
	state.resolved = false
	if !m.found[chainID] || lg.BlockNumber < m.next[chainID] {
		m.next[chainID] = lg.BlockNumber
		m.found[chainID] = true
	}
	return nil
}

func (m *memoryScanProgress) ResolveLogFailure(_ context.Context, chainID int, lg rpc.Log) error {
	if m.resolveErr != nil {
		return m.resolveErr
	}
	if state := m.failures[scanFailureKey(chainID, lg)]; state != nil {
		state.resolved = true
	}
	return nil
}

func (m *memoryScanProgress) HasUnresolvedLogFailure(_ context.Context, chainID int, fromBlock, toBlock uint64) (bool, error) {
	for key, state := range m.failures {
		if strings.HasPrefix(key, fmt.Sprintf("%d/", chainID)) && !state.resolved && state.block >= fromBlock && state.block <= toBlock {
			return true, nil
		}
	}
	return false, nil
}

func scanFailureKey(chainID int, lg rpc.Log) string {
	return fmt.Sprintf("%d/%d/%d/%s", chainID, lg.BlockNumber, lg.LogIndex, strings.ToLower(lg.TxHash))
}

func newScanTestRelayer(client ChainClient, progress scanProgressStore, start, maxRange uint64) *Relayer {
	chains := map[int]deployments.ChainConfig{
		scanTestChainID: {
			Contracts: map[string]deployments.ContractInfo{
				"bridgeGateway": {Address: srcGW},
			},
		},
	}
	cfg := DefaultRelayerConfig()
	cfg.ConfirmationDepth = 0
	cfg.MaxBlockRange = maxRange
	cfg.StartBlocks = map[int]uint64{scanTestChainID: start}
	r := NewRelayer(
		NewRegistry(chains, nil),
		NewStore(nil),
		nil,
		map[int]ChainClient{scanTestChainID: client},
		cfg,
		slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil)),
	)
	r.progress = progress
	return r
}

// The failed log shares a block with another log. The checkpoint must remain
// on that whole block so a new process replays both coordinates rather than
// resuming after the range and losing the malformed event forever.
func TestScanChainRetriesFailedBlockAfterRestart(t *testing.T) {
	const failedTx = "0xfailed"
	progress := newMemoryScanProgress()
	firstClient := &scanTestClient{
		fakeChainClient: &fakeChainClient{},
		head:            104,
		logs: []rpc.Log{
			{BlockNumber: 102, LogIndex: 1, TxHash: failedTx, Topics: []string{TopicBridgeInitiated}},
			{BlockNumber: 101, LogIndex: 0, TxHash: "0xok-101", Topics: []string{"0xunknown"}},
			{BlockNumber: 102, LogIndex: 0, TxHash: "0xok-102", Topics: []string{"0xunknown"}},
		},
	}

	first := newScanTestRelayer(firstClient, progress, 100, 10)
	if err := first.ScanChain(context.Background(), scanTestChainID); err == nil {
		t.Fatal("malformed log must fail the scan")
	}
	if got := progress.next[scanTestChainID]; got != 102 {
		t.Fatalf("next block = %d, want failed block 102", got)
	}
	state := progress.failures[scanFailureKey(scanTestChainID, firstClient.logs[0])]
	if state == nil || state.attempts != 1 || state.resolved {
		t.Fatalf("failure journal = %+v, want one unresolved attempt", state)
	}

	secondClient := &scanTestClient{
		fakeChainClient: &fakeChainClient{},
		head:            104,
		logs:            []rpc.Log{firstClient.logs[0]},
	}
	second := newScanTestRelayer(secondClient, progress, 999, 10)
	if err := second.ScanChain(context.Background(), scanTestChainID); err == nil {
		t.Fatal("restart must retry and surface the still-malformed log")
	}
	if len(secondClient.filters) != 1 || secondClient.filters[0].FromBlock != 102 {
		t.Fatalf("retry filters = %+v, want fromBlock 102", secondClient.filters)
	}
	if state.attempts != 2 || state.resolved {
		t.Fatalf("failure journal = %+v, want two unresolved attempts", state)
	}
	if got := progress.next[scanTestChainID]; got != 102 {
		t.Fatalf("next block after failed retry = %d, want 102", got)
	}

	// The provider now returns a decodable replacement at the same durable
	// coordinate. A later process must resolve the failure marker before it can
	// advance beyond the scanned range.
	retryLog := rpc.Log{BlockNumber: 102, LogIndex: 1, TxHash: failedTx, Topics: []string{"0xunknown"}}
	restartClient := &scanTestClient{
		fakeChainClient: &fakeChainClient{},
		head:            104,
		logs:            []rpc.Log{retryLog},
	}
	restarted := newScanTestRelayer(restartClient, progress, 999, 10)
	if err := restarted.ScanChain(context.Background(), scanTestChainID); err != nil {
		t.Fatalf("restart scan: %v", err)
	}
	if len(restartClient.filters) != 1 || restartClient.filters[0].FromBlock != 102 {
		t.Fatalf("restart filters = %+v, want fromBlock 102", restartClient.filters)
	}
	if got := progress.next[scanTestChainID]; got != 105 {
		t.Fatalf("next block after retry = %d, want 105", got)
	}
	if !state.resolved {
		t.Fatal("successful replay must resolve the durable failure marker")
	}
}

// Projection success is not enough to claim durable scan progress. If the
// checkpoint write fails, a restart must request the same range again.
func TestScanChainRetriesRangeWhenCheckpointWriteFails(t *testing.T) {
	progress := newMemoryScanProgress()
	progress.saveErrAt = 13
	progress.saveErr = errors.New("checkpoint unavailable")
	firstClient := &scanTestClient{fakeChainClient: &fakeChainClient{}, head: 12}

	first := newScanTestRelayer(firstClient, progress, 10, 10)
	if err := first.ScanChain(context.Background(), scanTestChainID); !errors.Is(err, progress.saveErr) {
		t.Fatalf("scan error = %v, want checkpoint error", err)
	}
	if progress.found[scanTestChainID] {
		t.Fatalf("failed checkpoint must not create progress: %+v", progress.next)
	}

	progress.saveErr = nil
	restartClient := &scanTestClient{fakeChainClient: &fakeChainClient{}, head: 12}
	restarted := newScanTestRelayer(restartClient, progress, 10, 10)
	if err := restarted.ScanChain(context.Background(), scanTestChainID); err != nil {
		t.Fatalf("restart scan: %v", err)
	}
	if len(restartClient.filters) != 1 || restartClient.filters[0].FromBlock != 10 {
		t.Fatalf("restart filters = %+v, want the original range", restartClient.filters)
	}
	if got := progress.next[scanTestChainID]; got != 13 {
		t.Fatalf("next block = %d, want 13", got)
	}
}

func TestScanChainDoesNotAdvanceWhenFailureJournalWriteFails(t *testing.T) {
	journalErr := errors.New("failure journal unavailable")
	progress := newMemoryScanProgress()
	progress.recordErr = journalErr
	client := &scanTestClient{
		fakeChainClient: &fakeChainClient{},
		head:            5,
		logs: []rpc.Log{
			{BlockNumber: 4, LogIndex: 0, TxHash: "0xbad", Topics: []string{TopicBridgeInitiated}},
		},
	}

	r := newScanTestRelayer(client, progress, 4, 10)
	err := r.ScanChain(context.Background(), scanTestChainID)
	if !errors.Is(err, journalErr) {
		t.Fatalf("scan error = %v, want journal error", err)
	}
	if progress.found[scanTestChainID] {
		t.Fatalf("failed log must remain uncheckpointed: %+v", progress.next)
	}
}

func TestScanChainDoesNotSkipFailureMissingFromRetryResponse(t *testing.T) {
	progress := newMemoryScanProgress()
	failed := rpc.Log{BlockNumber: 22, LogIndex: 1, TxHash: "0xmissing", Topics: []string{TopicBridgeInitiated}}
	firstClient := &scanTestClient{
		fakeChainClient: &fakeChainClient{},
		head:            24,
		logs:            []rpc.Log{failed},
	}
	first := newScanTestRelayer(firstClient, progress, 20, 10)
	if err := first.ScanChain(context.Background(), scanTestChainID); err == nil {
		t.Fatal("malformed log must fail the first scan")
	}

	missingClient := &scanTestClient{fakeChainClient: &fakeChainClient{}, head: 24}
	restarted := newScanTestRelayer(missingClient, progress, 999, 10)
	err := restarted.ScanChain(context.Background(), scanTestChainID)
	if !errors.Is(err, ErrUnresolvedLogFailure) {
		t.Fatalf("retry error = %v, want unresolved failure", err)
	}
	if got := progress.next[scanTestChainID]; got != 22 {
		t.Fatalf("next block = %d, want failed block 22", got)
	}
	if len(missingClient.filters) != 1 || missingClient.filters[0].FromBlock != 22 {
		t.Fatalf("retry filters = %+v, want fromBlock 22", missingClient.filters)
	}
}
