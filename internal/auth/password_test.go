package auth

import (
	"strings"
	"testing"
)

func TestPasswordHashVerifiesAndRejectsWrongPassword(t *testing.T) {
	hash, err := HashPassword("temporary-secret")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if hash == "temporary-secret" {
		t.Fatal("expected password hash to differ from password")
	}
	if !strings.HasPrefix(hash, bcryptHashPrefix) {
		t.Fatalf("expected bcrypt hash prefix, got %q", hash)
	}
	if !VerifyPassword(hash, "temporary-secret") {
		t.Fatal("expected hash to verify correct password")
	}
	if VerifyPassword(hash, "wrong") {
		t.Fatal("expected hash to reject wrong password")
	}
}

func TestVerifyPasswordSupportsLegacyDemoPasswords(t *testing.T) {
	if !VerifyPassword("demo", "demo") {
		t.Fatal("expected legacy demo password to verify")
	}
	if VerifyPassword("demo", "wrong") {
		t.Fatal("expected legacy demo password to reject wrong password")
	}
	if VerifyPassword("temporary", "temporary") {
		t.Fatal("expected unknown plaintext password to be rejected")
	}
}

func TestVerifyPasswordSupportsBoundedLegacySHA256Hashes(t *testing.T) {
	hash, err := legacySHA256Hash("temporary-secret")
	if err != nil {
		t.Fatalf("legacy hash password: %v", err)
	}
	if !VerifyPassword(hash, "temporary-secret") {
		t.Fatal("expected bounded legacy sha256 hash to verify")
	}
	parts := strings.Split(hash, "$")
	parts[1] = "300001"
	if VerifyPassword(strings.Join(parts, "$"), "temporary-secret") {
		t.Fatal("expected excessive legacy sha256 iterations to be rejected")
	}
	parts[1] = "not-a-number"
	if VerifyPassword(strings.Join(parts, "$"), "temporary-secret") {
		t.Fatal("expected malformed legacy iterations to be rejected")
	}
	parts[1] = "1"
	parts[2] = "not base64"
	if VerifyPassword(strings.Join(parts, "$"), "temporary-secret") {
		t.Fatal("expected malformed salt to be rejected")
	}
	parts[2] = "c2FsdA"
	parts[3] = "not base64"
	if VerifyPassword(strings.Join(parts, "$"), "temporary-secret") {
		t.Fatal("expected malformed digest to be rejected")
	}
	if VerifyPassword("sha256$1$c2FsdA", "temporary-secret") {
		t.Fatal("expected malformed legacy hash shape to be rejected")
	}
	if VerifyPassword(hash, "") {
		t.Fatal("expected empty password to be rejected")
	}
}

func TestHashPasswordRejectsBlankPassword(t *testing.T) {
	if _, err := HashPassword("   "); err == nil {
		t.Fatal("expected blank password error")
	}
}
