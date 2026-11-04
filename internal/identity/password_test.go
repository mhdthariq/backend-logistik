package identity

import (
	"encoding/hex"
	"testing"
)

func TestNodePasswordFixture(t *testing.T) {
	// Generated with Node crypto.scryptSync(password, salt, 64), default options.
	const stored = "0123456789abcdef0123456789abcdef:89cc89d1502800f8cc622854eba0182539499a8bbf19bbca35a0e37d476c5892e35e4c210d644ccca706b68a3ec118cafd60947a66a8580e4a3b655e43ceee34"
	for _, tt := range []struct {
		name, password, hash string
		want                 bool
	}{
		{"Node fixture", "pässword-123", stored, true},
		{"wrong password", "incorrect", stored, false},
		{"empty hash", "password", "", false},
		{"missing salt", "password", ":abcdef", false},
		{"invalid hash", "password", "salt:" + string(make([]byte, 128)), false},
		{"short hash", "password", "salt:abcd", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyPassword(tt.password, tt.hash); got != tt.want {
				t.Fatalf("verify = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestPasswordAndTokenRoundTrip(t *testing.T) {
	first, err := HashPassword("secret-123")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("secret-123")
	if err != nil {
		t.Fatal(err)
	}
	if first == second || !VerifyPassword("secret-123", first) {
		t.Fatal("password hashes must be salted and verifiable")
	}
	if TokenHash("node-session-fixture") != "24f059a552cf0b093d939b2eaeca557909ec0418690e117148956cbc4afcb68c" {
		t.Fatal("session hash differs from Node")
	}
	token, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 64 || hash != TokenHash(token) {
		t.Fatal("invalid session token")
	}
	if _, err := hex.DecodeString(token); err != nil {
		t.Fatal(err)
	}
}
