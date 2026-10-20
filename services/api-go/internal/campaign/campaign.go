// Package campaign exposes bounded compatibility reads. Versioned content is
// obtained only from its published read port, never from legacy shadow rows.
package campaign

import (
	"context"
	"errors"
	"time"
)

type Campaign struct {
	ID           string     `json:"id"`
	Title        string     `json:"title"`
	Description  *string    `json:"description"`
	Banner       *string    `json:"banner"`
	StartDate    time.Time  `json:"startDate"`
	EndDate      time.Time  `json:"endDate"`
	Status       string     `json:"status"`
	Reward       *string    `json:"reward"`
	Participants int        `json:"participants"`
	Metadata     any        `json:"metadata"`
	CreatedAt    *time.Time `json:"createdAt,omitempty"`
	UpdatedAt    *time.Time `json:"updatedAt,omitempty"`

	// ParticipantCount is the real count from the owning read model. The legacy
	// scalar participants may be editorial only for unimported legacy records.
	// IsParticipating/HasReminder appear only when the request carried a
	// verifiable wallet identity.
	ParticipantCount int   `json:"participantCount"`
	IsParticipating  *bool `json:"isParticipating,omitempty"`
	HasReminder      *bool `json:"hasReminder,omitempty"`
}

var (
	ErrNotFound        = errors.New("campaign not found")
	ErrPoolUnavailable = errors.New("campaign repository: database pool not configured")
	ErrInvalidStatus   = errors.New("campaign status must be upcoming, active, or ended")
	ErrInvalidLimit    = errors.New("campaign limit must be between 1 and 50")
)

const defaultListLimit = 20
const maxListLimit = 50

type campaignReader interface {
	ListAll(context.Context, string, ...int) ([]Campaign, error)
	GetActive(context.Context, time.Time, ...int) ([]Campaign, error)
	FindByID(context.Context, string) (Campaign, error)
	ParticipantCounts(context.Context, []string) (map[string]int, error)
	WalletStates(context.Context, []string, string) (map[string]WalletState, error)
}

type Service struct {
	repo   campaignReader
	compat *publishedCompatibility
}

func NewService(repo *Repository) *Service { return &Service{repo: repo} }

// Service reads include real counts for HTTP and BFF callers alike. Missing
// dependencies are errors; a zero count must represent a successful DB read.
func (s *Service) List(ctx context.Context, status string) ([]Campaign, error) {
	return s.list(ctx, status, defaultListLimit, "")
}
func (s *Service) Active(ctx context.Context, now time.Time) ([]Campaign, error) {
	return s.active(ctx, now, defaultListLimit, "")
}
func (s *Service) FindByID(ctx context.Context, id string) (Campaign, error) {
	return s.byID(ctx, id, "")
}
func (s *Service) list(ctx context.Context, status string, limit int, wallet string) ([]Campaign, error) {
	if s == nil || s.repo == nil {
		return nil, ErrPoolUnavailable
	}
	if err := validStatus(status); err != nil {
		return nil, err
	}
	if _, err := boundedLimit(limit); err != nil {
		return nil, err
	}
	if s.compat != nil {
		return s.compat.list(ctx, s, status, limit, wallet)
	}
	rows, err := s.repo.ListAll(ctx, status, limit)
	if err != nil {
		return nil, err
	}
	return s.enrichForWallet(ctx, rows, wallet)
}
func (s *Service) active(ctx context.Context, now time.Time, limit int, wallet string) ([]Campaign, error) {
	if s == nil || s.repo == nil {
		return nil, ErrPoolUnavailable
	}
	if _, err := boundedLimit(limit); err != nil {
		return nil, err
	}
	if s.compat != nil {
		return s.compat.list(ctx, s, "active", limit, wallet)
	}
	rows, err := s.repo.GetActive(ctx, now, limit)
	if err != nil {
		return nil, err
	}
	return s.enrichForWallet(ctx, rows, wallet)
}
func (s *Service) byID(ctx context.Context, id, wallet string) (Campaign, error) {
	if s == nil || s.repo == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	if s.compat != nil {
		return s.compat.detail(ctx, s, id, wallet)
	}
	row, err := s.repo.FindByID(ctx, id)
	if err != nil {
		return Campaign{}, err
	}
	rows, err := s.enrichForWallet(ctx, []Campaign{row}, wallet)
	if err != nil {
		return Campaign{}, err
	}
	return rows[0], nil
}

// Create is retained for internal callers; the legacy HTTP entry is retired.
func (s *Service) Create(ctx context.Context, in CreateInput) (Campaign, error) {
	if s == nil {
		return Campaign{}, ErrPoolUnavailable
	}
	writer, ok := s.repo.(interface {
		Create(context.Context, CreateInput) (Campaign, error)
	})
	if !ok {
		return Campaign{}, ErrPoolUnavailable
	}
	return writer.Create(ctx, in)
}

func validStatus(status string) error {
	switch status {
	case "", "upcoming", "active", "ended":
		return nil
	}
	return ErrInvalidStatus
}
func boundedLimit(limits ...int) (int, error) {
	if len(limits) == 0 {
		return defaultListLimit, nil
	}
	if len(limits) != 1 || limits[0] < 1 || limits[0] > maxListLimit {
		return 0, ErrInvalidLimit
	}
	return limits[0], nil
}
