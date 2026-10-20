package web3events

import "github.com/jackc/pgx/v5"

type attemptResultRow struct {
	pgx.Row
	actionID string
}

func (r *attemptResultRow) Scan(dest ...any) error {
	return r.Row.Scan(append(dest, &r.actionID)...)
}

func scanAttemptTxRow(expectedAction string, row pgx.Row) (TransactionRow, error) {
	result := &attemptResultRow{Row: row}
	out, err := scanTxRow(result)
	if err != nil {
		return TransactionRow{}, err
	}
	// Two previously absent reports can allocate different actions before the
	// unique transaction key chooses a winner. Roll back the loser's whole
	// transaction, including its unused action and counter, instead of silently
	// retaining the winner's action while committing an orphan allocation.
	if result.actionID != expectedAction {
		return TransactionRow{}, ErrActionAttemptBound
	}
	return out, nil
}
