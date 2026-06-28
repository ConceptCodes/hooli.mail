package auth

import (
	"context"
	"errors"
	"testing"

	"hooli.mail/server/internal/models"
)

type fakeUserStore struct {
	users map[string]*models.User
}

func (f fakeUserStore) GetUserByEmail(_ context.Context, email string) (*models.User, error) {
	return f.users[email], nil
}

func hashFor(t *testing.T, pw string) string {
	t.Helper()
	h, err := HashPassword(pw)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	return h
}

func TestVerifySuccess(t *testing.T) {
	store := fakeUserStore{users: map[string]*models.User{
		"a@x.com": {Email: "a@x.com", PasswordHash: hashFor(t, "correct")},
	}}
	a := NewAuthenticator(store)

	u, err := a.Verify(context.Background(), "a@x.com", "correct")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if u.Email != "a@x.com" {
		t.Fatalf("returned user = %+v", u)
	}
}

// Unknown user and wrong password must both surface as the same error so a
// caller cannot enumerate accounts.
func TestVerifyUnknownUserAndBadPasswordBothInvalid(t *testing.T) {
	store := fakeUserStore{users: map[string]*models.User{
		"a@x.com": {Email: "a@x.com", PasswordHash: hashFor(t, "correct")},
	}}
	a := NewAuthenticator(store)

	if _, err := a.Verify(context.Background(), "missing@x.com", "whatever"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown user: want ErrInvalidCredentials, got %v", err)
	}
	if _, err := a.Verify(context.Background(), "a@x.com", "wrong"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("bad password: want ErrInvalidCredentials, got %v", err)
	}
}
