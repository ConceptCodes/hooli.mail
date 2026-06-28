// Package auth owns credential verification for Hooli Mail. It sits between
// the protocol servers (SMTP AUTH PLAIN, IMAP LOGIN) and the user store: both
// used to perform the same get-user-then-compare dance inline. That logic now
// lives behind Authenticator.Verify, so rate limiting, lockout or audit hooks
// have exactly one home.
package auth

import (
	"context"
	"errors"
	"fmt"

	"hooli.mail/server/internal/models"

	"golang.org/x/crypto/bcrypt"
)

// ErrInvalidCredentials is returned when a username is unknown or the password
// does not match. The two cases are deliberately indistinguishable to callers.
var ErrInvalidCredentials = errors.New("invalid credentials")

// UserStore is the narrow lookup the Authenticator needs from a Store. Any
// mailstore.Store satisfies it.
type UserStore interface {
	GetUserByEmail(ctx context.Context, email string) (*models.User, error)
}

// Authenticator verifies a username/password pair against a UserStore. The
// bcrypt compare and the not-found/wrong-password collapse into a single
// ErrInvalidCredentials here, so neither protocol server has to encode the
// lookup sequence.
type Authenticator struct {
	users UserStore
}

func NewAuthenticator(users UserStore) *Authenticator {
	return &Authenticator{users: users}
}

// Verify returns the authenticated User, or ErrInvalidCredentials if the
// account is unknown or the password is wrong. A wrapped error is returned for
// genuine lookup failures.
func (a *Authenticator) Verify(ctx context.Context, email, password string) (*models.User, error) {
	u, err := a.users.GetUserByEmail(ctx, email)
	if err != nil {
		return nil, fmt.Errorf("lookup user: %w", err)
	}
	if u == nil || VerifyPassword(password, u.PasswordHash) != nil {
		return nil, ErrInvalidCredentials
	}
	return u, nil
}

// HashPassword bcrypts a plaintext password for storage.
func HashPassword(password string) (string, error) {
	bytes, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(bytes), nil
}

// VerifyPassword compares a plaintext password against a stored hash. A
// mismatch returns ErrInvalidCredentials; the bcrypt detail is not leaked.
func VerifyPassword(password, hash string) error {
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)); err != nil {
		return ErrInvalidCredentials
	}
	return nil
}
