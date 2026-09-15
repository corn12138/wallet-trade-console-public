package indexer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	currentProjectionVersion         = 1
	DefaultProjectionRepairBatchSize = 250
)

var ErrProjectionRepairIncomplete = errors.New("indexer: incomplete projection repair")

// ProjectionRepairReport separates attempted legacy rows from rows repaired in
// this startup pass. A failed row remains incomplete and is retried on the next
// startup; it is never converted into a completion marker by error logging.
type ProjectionRepairReport struct {
	Attempted int
	Repaired  int
	Failed    int
}

type storedProjectionEvent struct {
	id        int64
	event     ParsedEvent
	decodeErr error
}

type projectionRepairCursor struct {
	set                     bool
	chainID, logIndex       int
	blockNumber, rawEventID int64
}

func (c *projectionRepairCursor) advance(stored storedProjectionEvent) {
	c.set = true
	c.chainID = stored.event.ChainID
	c.blockNumber = stored.event.BlockNumber
	c.logIndex = stored.event.LogIndex
	c.rawEventID = stored.id
}

// RepairIncomplete makes one bounded attempt per incomplete raw event present
// at startup. Its ordered cursor moves past a failed row within this pass; the
// row remains incomplete and is selected again from the beginning next start.
func (s *DBSink) RepairIncomplete(ctx context.Context, batchSize int) (ProjectionRepairReport, error) {
	return s.repairIncompleteAt(ctx, batchSize, time.Now())
}

func (s *DBSink) repairIncompleteAt(ctx context.Context, batchSize int, startedAt time.Time) (ProjectionRepairReport, error) {
	var report ProjectionRepairReport
	if s == nil || s.repairStore == nil || s.beginProjection == nil {
		return report, ErrSinkPoolNil
	}
	if batchSize <= 0 || batchSize > 5_000 {
		batchSize = DefaultProjectionRepairBatchSize
	}

	startedAt = startedAt.UTC().Truncate(time.Millisecond)
	var firstErr error
	var cursor projectionRepairCursor
	for {
		events, err := s.loadIncompleteProjectionBatch(ctx, cursor, batchSize)
		if err != nil {
			return report, err
		}
		if len(events) == 0 {
			break
		}

		for _, stored := range events {
			if err := ctx.Err(); err != nil {
				return report, err
			}
			report.Attempted++
			repairErr := stored.decodeErr
			if repairErr == nil {
				repairErr = s.HandleEvent(ctx, stored.event)
			}
			if repairErr == nil {
				report.Repaired++
			} else {
				report.Failed++
				if firstErr == nil {
					firstErr = repairErr
				}
				if err := s.recordProjectionFailure(ctx, stored.id, startedAt, repairErr); err != nil {
					return report, errors.Join(repairErr, err)
				}
			}
			cursor.advance(stored)
		}
	}
	if report.Failed > 0 {
		return report, fmt.Errorf("%w: %d of %d failed: %v",
			ErrProjectionRepairIncomplete, report.Failed, report.Attempted, firstErr)
	}
	return report, nil
}

func (s *DBSink) loadIncompleteProjectionBatch(ctx context.Context, cursor projectionRepairCursor, limit int) ([]storedProjectionEvent, error) {
	rows, err := s.repairStore.Query(ctx, `
		SELECT id, chain_id, contract_address, event_name, tx_hash, log_index,
		       block_number, actor_address, args
		FROM web3_events
		WHERE (projection_completed_at IS NULL OR projection_version < $1)
		  AND (
		      NOT $2 OR chain_id > $3
		      OR (chain_id = $3 AND block_number > $4)
		      OR (chain_id = $3 AND block_number = $4 AND log_index > $5)
		      OR (chain_id = $3 AND block_number = $4 AND log_index = $5 AND id > $6)
		  )
		ORDER BY chain_id, block_number, log_index, id
		LIMIT $7
	`, currentProjectionVersion, cursor.set, cursor.chainID, cursor.blockNumber,
		cursor.logIndex, cursor.rawEventID, limit)
	if err != nil {
		return nil, fmt.Errorf("indexer: query incomplete projections: %w", err)
	}
	defer rows.Close()

	out := make([]storedProjectionEvent, 0, limit)
	for rows.Next() {
		var (
			id, blockNumber                    int64
			chainID, logIndex                  int
			contractAddress, eventName, txHash string
			actorAddress                       *string
			args                               []byte
		)
		if err := rows.Scan(&id, &chainID, &contractAddress, &eventName, &txHash,
			&logIndex, &blockNumber, &actorAddress, &args); err != nil {
			return nil, fmt.Errorf("indexer: scan incomplete projection: %w", err)
		}
		event, decodeErr := restoreProjectionEvent(chainID, contractAddress, eventName,
			txHash, logIndex, blockNumber, actorAddress, args)
		out = append(out, storedProjectionEvent{id: id, event: event, decodeErr: decodeErr})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("indexer: iterate incomplete projections: %w", err)
	}
	return out, nil
}

func (s *DBSink) recordProjectionFailure(ctx context.Context, id int64, attemptedAt time.Time, cause error) error {
	messageRunes := []rune(cause.Error())
	if len(messageRunes) > 2_000 {
		messageRunes = messageRunes[:2_000]
	}
	tag, err := s.repairStore.Exec(ctx, `
		UPDATE web3_events
		SET projection_error = $2,
		    projection_attempts = projection_attempts + 1,
		    projection_last_attempt_at = $3
		WHERE id = $1
	`, id, string(messageRunes), attemptedAt)
	if err != nil {
		return fmt.Errorf("indexer: record projection failure for event %d: %w", id, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("indexer: projection failure row missing for event %d", id)
	}
	return nil
}

func restoreProjectionEvent(chainID int, contractAddress, eventName, txHash string,
	logIndex int, blockNumber int64, actorAddress *string, rawArgs []byte) (ParsedEvent, error) {
	event := ParsedEvent{
		ChainID: chainID, ContractAddress: contractAddress, BlockNumber: blockNumber,
		TxHash: txHash, LogIndex: logIndex,
	}
	if blockNumber < 0 || logIndex < 0 {
		return event, fmt.Errorf("indexer: invalid stored coordinates %d#%d", blockNumber, logIndex)
	}

	decoder := json.NewDecoder(strings.NewReader(string(rawArgs)))
	decoder.UseNumber()
	persisted := map[string]any{}
	if err := decoder.Decode(&persisted); err != nil {
		return event, fmt.Errorf("indexer: decode stored args for %s#%d: %w", txHash, logIndex, err)
	}
	runtimeArgs, err := restoreRuntimeArgs(eventName, persisted)
	if err != nil {
		return event, fmt.Errorf("indexer: restore %s args for %s#%d: %w", eventName, txHash, logIndex, err)
	}
	actor := ""
	if actorAddress != nil {
		actor = *actorAddress
	}
	event.Parsed = ParsedIndexedLog{
		EventName: eventName, RuntimeArgs: runtimeArgs,
		PersistedArgs: persisted, ActorAddress: actor,
	}
	return event, nil
}

func restoreRuntimeArgs(eventName string, persisted map[string]any) (map[string]any, error) {
	spec := EventSpecByName(eventName)
	if spec == nil {
		return persisted, nil
	}
	out := make(map[string]any, len(persisted))
	for key, value := range persisted {
		out[key] = value
	}
	for _, input := range spec.Inputs {
		value, ok := persisted[input.Name]
		if !ok {
			continue
		}
		switch input.Type {
		case "uint", "uint112", "uint256", "int256":
			text := ""
			switch value := value.(type) {
			case string:
				text = value
			case json.Number:
				text = value.String()
			default:
				return nil, fmt.Errorf("%s has non-integer JSON type %T", input.Name, value)
			}
			integer, ok := new(big.Int).SetString(text, 10)
			if !ok || (input.Type != "int256" && integer.Sign() < 0) {
				return nil, fmt.Errorf("%s has invalid %s value %q", input.Name, input.Type, text)
			}
			out[input.Name] = integer
		case "address":
			address, ok := value.(string)
			if !ok || !isLikelyAddress(address) {
				return nil, fmt.Errorf("%s has invalid address", input.Name)
			}
			out[input.Name] = strings.ToLower(address)
		case "string", "bytes32":
			if _, ok := value.(string); !ok {
				return nil, fmt.Errorf("%s has non-string JSON type %T", input.Name, value)
			}
		case "bool":
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("%s has non-boolean JSON type %T", input.Name, value)
			}
		}
	}
	return out, nil
}
