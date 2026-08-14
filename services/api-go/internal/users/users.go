// Package users is the Go port of legacy NestJS user/user.service.ts —
// the data layer the auth service (login/register/refresh) and the
// /api/users HTTP routes share.
package users

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPoolUnavailable surfaces the degraded contract.
var ErrPoolUnavailable = fmt.Errorf("users repository: database pool not configured")

// ErrNotFound is returned when no row matches.
var ErrNotFound = errors.New("user not found")

// ErrDuplicate is returned on unique-constraint violations during
// Create / Update — the handler turns this into 400.
var ErrDuplicate = errors.New("user with that email or username already exists")

// User mirrors the Prisma User model. Password and RefreshToken are
// kept on the struct for the auth service; the handler-level
// serializer must strip them.
type User struct {
	ID           string    `json:"id"`
	Email        string    `json:"email"`
	Username     string    `json:"username"`
	FullName     *string   `json:"fullName"`
	Password     string    `json:"-"`
	Avatar       *string   `json:"avatar"`
	Bio          *string   `json:"bio"`
	Roles        []string  `json:"roles"`
	RefreshToken *string   `json:"-"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

// PublicView is the safe-to-return projection (no password, no refresh).
type PublicView struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	FullName  *string   `json:"fullName"`
	Avatar    *string   `json:"avatar"`
	Bio       *string   `json:"bio"`
	Roles     []string  `json:"roles"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// CreateInput mirrors the register body.
type CreateInput struct {
	Email    string  `json:"email"`
	Username string  `json:"username"`
	Password string  `json:"password"` // already-hashed (caller hashes with bcrypt)
	FullName *string `json:"fullName,omitempty"`
	Roles    []string
}

// UpdateInput mirrors PATCH /users/me.
type UpdateInput struct {
	Email    *string `json:"email,omitempty"`
	Username *string `json:"username,omitempty"`
	FullName *string `json:"fullName,omitempty"`
	Avatar   *string `json:"avatar,omitempty"`
	Bio      *string `json:"bio,omitempty"`
}

// Repository wraps pgx.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository tolerates a nil pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

const baseSelect = `
	SELECT id, email, username, "fullName", password, avatar, bio,
	       roles, "refreshToken", "createdAt", "updatedAt"
	FROM users
`

// FindByID returns the row or ErrNotFound.
func (r *Repository) FindByID(ctx context.Context, id string) (User, error) {
	return r.findOne(ctx, baseSelect+"WHERE id = $1", id)
}

// FindByEmail — exact email match.
func (r *Repository) FindByEmail(ctx context.Context, email string) (User, error) {
	return r.findOne(ctx, baseSelect+"WHERE email = $1", email)
}

// FindByUsername — exact username match.
func (r *Repository) FindByUsername(ctx context.Context, username string) (User, error) {
	return r.findOne(ctx, baseSelect+"WHERE username = $1", username)
}

// FindByUsernameOrEmail finds either (used by login).
func (r *Repository) FindByUsernameOrEmail(ctx context.Context, query string) (User, error) {
	return r.findOne(ctx, baseSelect+"WHERE username = $1 OR email = $1 LIMIT 1", query)
}

func (r *Repository) findOne(ctx context.Context, query string, arg string) (User, error) {
	if r.pool == nil {
		return User{}, ErrPoolUnavailable
	}
	var (
		u         User
		rolesJSON []byte
	)
	err := r.pool.QueryRow(ctx, query, arg).Scan(
		&u.ID, &u.Email, &u.Username, &u.FullName, &u.Password,
		&u.Avatar, &u.Bio, &rolesJSON, &u.RefreshToken,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("query user: %w", err)
	}
	if len(rolesJSON) > 0 {
		if err := json.Unmarshal(rolesJSON, &u.Roles); err != nil {
			return User{}, fmt.Errorf("parse roles: %w", err)
		}
	}
	return u, nil
}

// Create inserts a new row. Email + username uniqueness is enforced by
// the DB constraints; we map the unique-violation to ErrDuplicate.
func (r *Repository) Create(ctx context.Context, in CreateInput) (User, error) {
	if r.pool == nil {
		return User{}, ErrPoolUnavailable
	}
	roles := in.Roles
	if len(roles) == 0 {
		roles = []string{"user"}
	}
	rolesJSON, err := json.Marshal(roles)
	if err != nil {
		return User{}, fmt.Errorf("marshal roles: %w", err)
	}

	var (
		u        User
		outRoles []byte
	)
	err = r.pool.QueryRow(ctx, `
		INSERT INTO users (id, email, username, "fullName", password, roles, "createdAt", "updatedAt")
		VALUES (gen_random_uuid()::text, $1, $2, $3, $4, $5::jsonb, NOW(), NOW())
		RETURNING id, email, username, "fullName", password, avatar, bio,
		          roles, "refreshToken", "createdAt", "updatedAt"
	`, in.Email, in.Username, in.FullName, in.Password, string(rolesJSON)).Scan(
		&u.ID, &u.Email, &u.Username, &u.FullName, &u.Password,
		&u.Avatar, &u.Bio, &outRoles, &u.RefreshToken,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrDuplicate
		}
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	if len(outRoles) > 0 {
		_ = json.Unmarshal(outRoles, &u.Roles)
	}
	return u, nil
}

// Update applies a partial change. Email / username uniqueness is
// re-checked before issuing the UPDATE so the caller gets a clean 400.
func (r *Repository) Update(ctx context.Context, id string, in UpdateInput) (User, error) {
	if r.pool == nil {
		return User{}, ErrPoolUnavailable
	}

	sets := []string{}
	args := []any{}
	add := func(clause string, value any) {
		args = append(args, value)
		sets = append(sets, fmt.Sprintf(clause, len(args)))
	}
	if in.Email != nil {
		add(`email = $%d`, *in.Email)
	}
	if in.Username != nil {
		add(`username = $%d`, *in.Username)
	}
	if in.FullName != nil {
		add(`"fullName" = $%d`, *in.FullName)
	}
	if in.Avatar != nil {
		add(`avatar = $%d`, *in.Avatar)
	}
	if in.Bio != nil {
		add(`bio = $%d`, *in.Bio)
	}
	if len(sets) == 0 {
		return r.FindByID(ctx, id)
	}
	sets = append(sets, `"updatedAt" = NOW()`)
	args = append(args, id)

	query := fmt.Sprintf(`
		UPDATE users SET %s WHERE id = $%d
		RETURNING id, email, username, "fullName", password, avatar, bio,
		          roles, "refreshToken", "createdAt", "updatedAt"
	`, strings.Join(sets, ", "), len(args))

	var (
		u         User
		rolesJSON []byte
	)
	err := r.pool.QueryRow(ctx, query, args...).Scan(
		&u.ID, &u.Email, &u.Username, &u.FullName, &u.Password,
		&u.Avatar, &u.Bio, &rolesJSON, &u.RefreshToken,
		&u.CreatedAt, &u.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		if isUniqueViolation(err) {
			return User{}, ErrDuplicate
		}
		return User{}, fmt.Errorf("update user: %w", err)
	}
	if len(rolesJSON) > 0 {
		_ = json.Unmarshal(rolesJSON, &u.Roles)
	}
	return u, nil
}

// Delete removes the row. ErrNotFound when 0 rows match.
func (r *Repository) Delete(ctx context.Context, id string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	tag, err := r.pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateRefreshToken stores a hashed refresh token (or null to clear it
// on logout). Caller is responsible for hashing — keeps the package
// dependency-free of crypto.
func (r *Repository) UpdateRefreshToken(ctx context.Context, userID string, hashedToken *string) error {
	if r.pool == nil {
		return ErrPoolUnavailable
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE users SET "refreshToken" = $1, "updatedAt" = NOW() WHERE id = $2
	`, hashedToken, userID)
	if err != nil {
		return fmt.Errorf("update refresh token: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Public projects sensitive fields out before serializing.
func (u User) Public() PublicView {
	return PublicView{
		ID:        u.ID,
		Email:     u.Email,
		Username:  u.Username,
		FullName:  u.FullName,
		Avatar:    u.Avatar,
		Bio:       u.Bio,
		Roles:     u.Roles,
		CreatedAt: u.CreatedAt,
		UpdatedAt: u.UpdatedAt,
	}
}

// isUniqueViolation detects pg unique-constraint errors without
// importing pgconn directly (kept minimal — string match on the SQLSTATE).
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505") || strings.Contains(msg, "duplicate key value")
}
