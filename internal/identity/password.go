// Package identity preserves the existing Node authentication contracts.
package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"golang.org/x/crypto/scrypt"
)

// HashPassword generates the salt:hash format used by Node's scryptSync.
func HashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate password salt: %w", err)
	}
	saltText := hex.EncodeToString(salt)
	key, err := scrypt.Key([]byte(password), []byte(saltText), 16384, 8, 1, 64)
	if err != nil {
		return "", fmt.Errorf("derive password: %w", err)
	}
	return saltText + ":" + hex.EncodeToString(key), nil
}

// VerifyPassword uses the hexadecimal salt as text, just like Node (not decoded bytes).
func VerifyPassword(password, stored string) bool {
	parts := strings.Split(stored, ":")
	if len(parts) != 2 || parts[0] == "" || len(parts[1]) != 128 {
		return false
	}
	expected, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	key, err := scrypt.Key([]byte(password), []byte(parts[0]), 16384, 8, 1, 64)
	return err == nil && subtle.ConstantTimeCompare(key, expected) == 1
}

// NewToken returns a random 256-bit bearer token and its database representation.
func NewToken() (string, string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate session token: %w", err)
	}
	token := hex.EncodeToString(b)
	return token, TokenHash(token), nil
}

// TokenHash is compatible with Node's SHA-256 hexadecimal session storage.
func TokenHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
