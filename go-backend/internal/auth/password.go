package auth

import (
	"context"
	"errors"
	"fmt"

	"audiobookshelf-compat/internal/db"

	"golang.org/x/crypto/bcrypt"
)

// bcryptCost is the cost upstream hashes users.pash with.
const bcryptCost = 8

// HashPassword returns the bcrypt hash stored in users.pash.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcryptCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(hash), nil
}

// errBadPassword is the text callers show for any failed password check.
var errBadPassword = errors.New("Invalid password")

// checkPassword applies the local login rule (LocalAuthStrategy.js): the root
// user with no stored password logs in with an empty one; every other user
// needs a stored hash and the matching password, so a user created through
// OpenID (no pash) cannot log in locally.
func checkPassword(user db.User, password string) error {
	if db.Val(user.Type) == "root" && db.Val(user.Pash) == "" && password == "" {
		return nil
	}
	if password == "" || db.Val(user.Pash) == "" || bcrypt.CompareHashAndPassword([]byte(*user.Pash), []byte(password)) != nil {
		return errBadPassword
	}
	return nil
}

// ChangePassword sets a new password after checking the current one. Only
// root may use an empty new password (LocalAuthStrategy.changePassword). The
// error texts are shown to the user.
func (s *Service) ChangePassword(ctx context.Context, userID, password, newPassword string) error {
	user, err := s.userByID(ctx, userID)
	if err != nil {
		return err
	}
	if db.Val(user.Type) != "root" && newPassword == "" {
		return errors.New("Invalid new password - Only root can have an empty password")
	}
	if err := checkPassword(user, password); err != nil {
		return err
	}
	user.Pash = db.Ptr("")
	if newPassword != "" {
		hash, err := HashPassword(newPassword)
		if err != nil {
			return errors.New("Hash failed")
		}
		user.Pash = &hash
	}
	user.UpdatedAt = db.Now()
	return s.db.Update(ctx, "users", &user, "pash", "updatedAt")
}
