package executionregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	// ErrUnavailable: the authority could not be read (database error, missing tables).
	ErrUnavailable = errors.New("executionregistry: authority unavailable")
	// ErrNoActiveRevision: the tables exist but nothing has been activated.
	ErrNoActiveRevision = errors.New("executionregistry: no active revision")
	// ErrActivationConflict: the CAS expected version is stale (EXR02).
	ErrActivationConflict = errors.New("executionregistry: activation conflict")
	// ErrNotSealed: the revision is not a sealed snapshot, or its digest no longer matches (EXR03).
	ErrNotSealed = errors.New("executionregistry: revision is not a sealed snapshot")
	// ErrUnknownRevision / ErrUnknownBinding classify historical lookups.
	ErrUnknownRevision = errors.New("executionregistry: unknown revision")
	ErrUnknownBinding  = errors.New("executionregistry: unknown binding")
)

// Repository is the only writer of the registry tables (module boundary rule).
type Repository struct{ pool *pgxpool.Pool }

func NewRepository(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func unavailable(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrNoActiveRevision) || errors.Is(err, ErrUnknownRevision) || errors.Is(err, ErrUnknownBinding) {
		return err
	}
	return fmt.Errorf("%w: %v", ErrUnavailable, err)
}

// Stage inserts and seals one revision in a single transaction (§4.3). The
// triggers refuse members from any other transaction and an unsealed commit.
func (r *Repository) Stage(ctx context.Context, s Snapshot, actor, reason string) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, ErrUnavailable
	}
	if err := ValidateSnapshot(s, BuiltinManifest()); err != nil {
		return 0, err
	}
	summary, err := json.Marshal(s.SourceSummary)
	if err != nil {
		return 0, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return 0, unavailable(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var revision int64
	if err := tx.QueryRow(ctx, `
		INSERT INTO execution_registry_snapshots (schema_version, manifest_digest, declarations_digest, source_summary, staged_by, reason)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING revision
	`, s.SchemaVersion, s.ManifestDigest, s.DeclarationsDigest, summary, actor, reason).Scan(&revision); err != nil {
		return 0, fmt.Errorf("executionregistry: stage header: %w", err)
	}
	for _, e := range s.Entries {
		if _, err := tx.Exec(ctx, `
			INSERT INTO execution_registry_entries (revision, chain_id, name, address, deployment_file, deployment_timestamp, abi_hash,
			  interfaces, code_hash, code_block_number, code_block_hash, entry_digest)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		`, revision, e.ChainID, e.Name, e.Address, e.DeploymentFile, e.DeploymentTimestamp, e.ABIHash, e.Interfaces, e.CodeHash,
			int64(e.CodeBlockNumber), e.CodeBlockHash, e.EntryDigest); err != nil {
			return 0, fmt.Errorf("executionregistry: stage entry %s: %w", e.Name, err)
		}
	}
	for _, m := range s.Markets {
		if _, err := tx.Exec(ctx, `
			INSERT INTO execution_registry_markets (revision, chain_id, symbol, position_manager, index_token, collateral_token,
			  index_decimals, collateral_decimals, price_precision, components, market_digest)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		`, revision, m.ChainID, m.Symbol, m.PositionManager, m.IndexToken, m.CollateralToken, m.IndexDecimals, m.CollateralDecimals,
			m.PricePrecision, m.Components, m.MarketDigest); err != nil {
			return 0, fmt.Errorf("executionregistry: stage market %s: %w", m.Symbol, err)
		}
	}
	for _, b := range s.Bindings {
		roles, _ := json.Marshal(b.Roles)
		if _, err := tx.Exec(ctx, `
			INSERT INTO execution_registry_bindings (revision, chain_id, operation, market, status, interface_id, target_name, roles, binding_digest)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		`, revision, b.ChainID, b.Operation, b.Market, b.Status, b.InterfaceID, b.Target, roles, b.BindingDigest); err != nil {
			return 0, fmt.Errorf("executionregistry: stage binding %s/%s: %w", b.Operation, b.Market, err)
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE execution_registry_snapshots SET sealed = true, content_digest = $2 WHERE revision = $1`,
		revision, ContentDigest(s)); err != nil {
		return 0, fmt.Errorf("executionregistry: seal: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("executionregistry: commit stage: %w", err)
	}
	return revision, nil
}

// Activate moves the pointer through the SQL function (CAS + history).
func (r *Repository) Activate(ctx context.Context, expected, revision int64, actor, reason string, observations []CodeObservation) (int64, error) {
	if r == nil || r.pool == nil {
		return 0, ErrUnavailable
	}
	snapshot, err := r.Revision(ctx, revision)
	if errors.Is(err, ErrUnknownRevision) {
		return 0, ErrNotSealed
	}
	if err != nil {
		return 0, err
	}
	if err := ValidateSnapshot(snapshot, BuiltinManifest()); err != nil {
		return 0, snapshotValidationError(err)
	}
	if observations == nil {
		observations = []CodeObservation{}
	}
	obs, err := json.Marshal(observations)
	if err != nil {
		return 0, err
	}
	var version int64
	err = r.pool.QueryRow(ctx, `SELECT public.execution_registry_activate($1, $2, $3, $4, $5)`, expected, revision, actor, reason, obs).Scan(&version)
	var pgErr *pgconn.PgError
	switch {
	case err == nil:
		return version, nil
	case errors.As(err, &pgErr) && pgErr.Code == "EXR02":
		return 0, ErrActivationConflict
	case errors.As(err, &pgErr) && pgErr.Code == "EXR03":
		return 0, ErrNotSealed
	default:
		return 0, fmt.Errorf("executionregistry: activate: %w", err)
	}
}

// Active is the pointer and the full content of the revision it names, read
// in one read-only REPEATABLE READ transaction (§4.3): sealed content cannot
// change, so the result describes exactly the revision it reports.
type Active struct {
	Revision          int64
	ActivationVersion int64
	Snapshot          Snapshot
}

func (r *Repository) readOnly(ctx context.Context, fn func(pgx.Tx) error) error {
	if r == nil || r.pool == nil {
		return ErrUnavailable
	}
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return unavailable(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return unavailable(err)
	}
	return unavailable(tx.Commit(ctx))
}

// ActiveRevision reads the pointer and then that revision's content.
func (r *Repository) ActiveRevision(ctx context.Context) (Active, error) {
	return r.activeWith(ctx, nil)
}

// activeWith lets a test run code between the pointer read and the member
// reads (the read-consistency case).
func (r *Repository) activeWith(ctx context.Context, between func()) (Active, error) {
	var out Active
	err := r.readOnly(ctx, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `SELECT active_revision, activation_version FROM execution_registry_pointer WHERE id = 1`).Scan(&out.Revision, &out.ActivationVersion)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoActiveRevision
		}
		if err != nil {
			return err
		}
		if between != nil {
			between()
		}
		out.Snapshot, err = loadSnapshot(ctx, tx, out.Revision)
		return err
	})
	return out, err
}

// Revision loads a sealed historical revision (ErrUnknownRevision otherwise).
func (r *Repository) Revision(ctx context.Context, revision int64) (Snapshot, error) {
	var out Snapshot
	err := r.readOnly(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = loadSnapshot(ctx, tx, revision)
		return err
	})
	return out, err
}

func loadSnapshot(ctx context.Context, tx pgx.Tx, revision int64) (Snapshot, error) {
	var s Snapshot
	var sealed bool
	var content *string
	var summary []byte
	err := tx.QueryRow(ctx, `SELECT schema_version, manifest_digest, declarations_digest, content_digest, sealed, source_summary
		FROM execution_registry_snapshots WHERE revision = $1`, revision).Scan(&s.SchemaVersion, &s.ManifestDigest, &s.DeclarationsDigest, &content, &sealed, &summary)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !sealed) {
		return Snapshot{}, ErrUnknownRevision
	}
	if err != nil {
		return Snapshot{}, err
	}
	s.ContentDigest = *content
	_ = json.Unmarshal(summary, &s.SourceSummary)
	rows, err := tx.Query(ctx, `SELECT chain_id, name, address, deployment_file, deployment_timestamp, abi_hash, interfaces, code_hash,
		code_block_number, code_block_hash, entry_digest FROM execution_registry_entries WHERE revision = $1 ORDER BY chain_id, name`, revision)
	if err != nil {
		return Snapshot{}, err
	}
	for rows.Next() {
		var e Entry
		var block int64
		if err := rows.Scan(&e.ChainID, &e.Name, &e.Address, &e.DeploymentFile, &e.DeploymentTimestamp, &e.ABIHash, &e.Interfaces, &e.CodeHash,
			&block, &e.CodeBlockHash, &e.EntryDigest); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		e.CodeBlockNumber = uint64(block)
		s.Entries = append(s.Entries, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}
	rows, err = tx.Query(ctx, `SELECT chain_id, symbol, position_manager, index_token, collateral_token, index_decimals, collateral_decimals,
		price_precision, components, market_digest FROM execution_registry_markets WHERE revision = $1 ORDER BY chain_id, symbol`, revision)
	if err != nil {
		return Snapshot{}, err
	}
	for rows.Next() {
		var m Market
		var idx, col, prec int16
		if err := rows.Scan(&m.ChainID, &m.Symbol, &m.PositionManager, &m.IndexToken, &m.CollateralToken, &idx, &col, &prec, &m.Components, &m.MarketDigest); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		m.IndexDecimals, m.CollateralDecimals, m.PricePrecision = int(idx), int(col), int(prec)
		s.Markets = append(s.Markets, m)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}
	rows, err = tx.Query(ctx, `SELECT chain_id, operation, market, status, interface_id, target_name, roles, binding_digest
		FROM execution_registry_bindings WHERE revision = $1 ORDER BY chain_id, operation, market`, revision)
	if err != nil {
		return Snapshot{}, err
	}
	for rows.Next() {
		var b Binding
		var roles []byte
		if err := rows.Scan(&b.ChainID, &b.Operation, &b.Market, &b.Status, &b.InterfaceID, &b.Target, &roles, &b.BindingDigest); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		if err := json.Unmarshal(roles, &b.Roles); err != nil {
			rows.Close()
			return Snapshot{}, err
		}
		s.Bindings = append(s.Bindings, b)
	}
	rows.Close()
	return s, rows.Err()
}

// HistoricalBinding resolves a claimed (revision, chain, bindingDigest) to
// the binding and the addresses it bound, from sealed history only.
type ResolvedBinding struct {
	Revision      int64
	Binding       Binding
	TargetAddress string
	RoleAddresses map[string]string
}

func (r *Repository) HistoricalBinding(ctx context.Context, revision int64, chainID int, digest string) (ResolvedBinding, error) {
	s, err := r.Revision(ctx, revision)
	if err != nil {
		return ResolvedBinding{}, err
	}
	return resolve(s, revision, chainID, digest)
}

func resolve(s Snapshot, revision int64, chainID int, digest string) (ResolvedBinding, error) {
	for _, b := range s.Bindings {
		if b.BindingDigest == digest {
			if b.ChainID != chainID {
				return ResolvedBinding{Revision: revision, Binding: b}, errChainMismatch
			}
			entries := s.entriesByChain(chainID)
			out := ResolvedBinding{Revision: revision, Binding: b, TargetAddress: entries[b.Target].Address, RoleAddresses: map[string]string{}}
			for role, name := range b.Roles {
				out.RoleAddresses[role] = entries[name].Address
			}
			return out, nil
		}
	}
	return ResolvedBinding{}, ErrUnknownBinding
}

var errChainMismatch = errors.New("executionregistry: binding belongs to another chain")

// Status is the operator view (CLI status and the startup audit).
type Status struct {
	Active            *Active
	HistoryVersions   []int64
	Consistent        bool
	ConsistencyDetail string
}

// Audit verifies pointer = last history row and gap-free versions (§4.3).
func (r *Repository) Audit(ctx context.Context) (Status, error) {
	var st Status
	err := r.readOnly(ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `SELECT activation_version FROM execution_registry_activations ORDER BY activation_version`)
		if err != nil {
			return err
		}
		for rows.Next() {
			var v int64
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return err
			}
			st.HistoryVersions = append(st.HistoryVersions, v)
		}
		rows.Close()
		var revision, version int64
		err = tx.QueryRow(ctx, `SELECT active_revision, activation_version FROM execution_registry_pointer WHERE id = 1`).Scan(&revision, &version)
		if errors.Is(err, pgx.ErrNoRows) {
			st.Consistent = len(st.HistoryVersions) == 0
			if !st.Consistent {
				st.ConsistencyDetail = "history without a pointer"
			}
			return nil
		}
		if err != nil {
			return err
		}
		st.Active = &Active{Revision: revision, ActivationVersion: version}
		var lastRevision int64
		if err := tx.QueryRow(ctx, `SELECT revision FROM execution_registry_activations WHERE activation_version = $1`, version).Scan(&lastRevision); err != nil {
			st.ConsistencyDetail = "pointer version has no history row"
			return nil
		}
		gapFree := true
		for i, v := range st.HistoryVersions {
			if v != int64(i+1) {
				gapFree = false
			}
		}
		st.Consistent = gapFree && len(st.HistoryVersions) > 0 && st.HistoryVersions[len(st.HistoryVersions)-1] == version && lastRevision == revision
		if !st.Consistent {
			st.ConsistencyDetail = "pointer does not equal the last activation, or versions have gaps"
		}
		return nil
	})
	return st, err
}

// Probe reports whether the schema this module needs exists (startup).
func (r *Repository) Probe(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return ErrUnavailable
	}
	var ok bool
	err := r.pool.QueryRow(ctx, `SELECT to_regclass('public.execution_registry_pointer') IS NOT NULL
		AND to_regclass('public.execution_registry_bindings') IS NOT NULL
		AND current_setting('server_encoding') = 'UTF8'
		AND EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = 'web3_transactions' AND column_name = 'execution_context_claim_digest')`).Scan(&ok)
	if err != nil {
		return unavailable(err)
	}
	if !ok {
		return fmt.Errorf("%w: execution registry requires its migrated schema and a UTF8 database", ErrUnavailable)
	}
	return nil
}

// Diff lists entry/market/binding digests that differ between two snapshots.
func Diff(from, to Snapshot) []string {
	index := func(s Snapshot) map[string]string {
		out := map[string]string{}
		for _, e := range s.Entries {
			out[fmt.Sprintf("entry %d %s", e.ChainID, e.Name)] = e.EntryDigest + " " + e.Address
		}
		for _, m := range s.Markets {
			out[fmt.Sprintf("market %d %s", m.ChainID, m.Symbol)] = m.MarketDigest
		}
		for _, b := range s.Bindings {
			out[fmt.Sprintf("binding %d %s %s", b.ChainID, b.Operation, b.Market)] = b.BindingDigest + " " + b.Status
		}
		return out
	}
	a, b := index(from), index(to)
	var lines []string
	for k, v := range b {
		if old, ok := a[k]; !ok {
			lines = append(lines, "+ "+k+" "+v)
		} else if old != v {
			lines = append(lines, "~ "+k+" "+old+" -> "+v)
		}
	}
	for k, v := range a {
		if _, ok := b[k]; !ok {
			lines = append(lines, "- "+k+" "+v)
		}
	}
	sort.Strings(lines)
	return lines
}

func lower(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
