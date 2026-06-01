package auth

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/d11nn/woms/internal/domain"
)

func TestCreateAndVerifyToken(t *testing.T) {
	token, err := CreateToken("secret", Claims{
		Subject: "user-1",
		Role:    domain.RoleScheduler,
		LineID:  "A",
	}, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken returned error: %v", err)
	}

	claims, err := VerifyToken("secret", token)
	if err != nil {
		t.Fatalf("VerifyToken returned error: %v", err)
	}
	if claims.Subject != "user-1" || claims.Role != domain.RoleScheduler || claims.LineID != "A" {
		t.Fatalf("unexpected claims: %+v", claims)
	}
}

func TestVerifyTokenRejectsTampering(t *testing.T) {
	token, err := CreateToken("secret", Claims{Subject: "user-1", Role: domain.RoleSales}, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken returned error: %v", err)
	}

	_, err = VerifyToken("other-secret", token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestVerifyTokenRejectsExpiredToken(t *testing.T) {
	token, err := CreateToken("secret", Claims{Subject: "user-1", Role: domain.RoleSales}, -time.Hour)
	if err != nil {
		t.Fatalf("CreateToken returned error: %v", err)
	}

	_, err = VerifyToken("secret", token)
	if !errors.Is(err, ErrExpiredToken) {
		t.Fatalf("expected ErrExpiredToken, got %v", err)
	}
}

func TestVerifyTokenRejectsInvalidRole(t *testing.T) {
	token, err := CreateToken("secret", Claims{Subject: "user-1", Role: domain.Role("owner")}, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken returned error: %v", err)
	}

	_, err = VerifyToken("secret", token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestCreateAndVerifyTokenRejectMalformedInputs(t *testing.T) {
	if _, err := CreateToken("", Claims{Subject: "user-1", Role: domain.RoleSales}, time.Hour); err == nil {
		t.Fatal("expected empty secret error")
	}
	for _, token := range []string{"", "one.two", "one.two.three.four"} {
		if _, err := VerifyToken("secret", token); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("expected invalid token for %q, got %v", token, err)
		}
	}
	unsigned := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString([]byte(`{`))
	if _, err := VerifyToken("secret", unsigned+"."+sign("secret", unsigned)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid JSON token error, got %v", err)
	}
	claimsJSON, _ := json.Marshal(Claims{Role: domain.RoleSales, Expires: time.Now().Add(time.Hour).Unix()})
	missingSubject := strings.Join([]string{
		base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)),
		base64.RawURLEncoding.EncodeToString(claimsJSON),
	}, ".")
	if _, err := VerifyToken("secret", missingSubject+"."+sign("secret", missingSubject)); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected missing subject error, got %v", err)
	}
	badBody := "header.%%%." + sign("secret", "header.%%%")
	if _, err := VerifyToken("secret", badBody); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected invalid base64 body error, got %v", err)
	}
}

func TestVerifyTokenRejectsSchedulerWithoutLine(t *testing.T) {
	token, err := CreateToken("secret", Claims{Subject: "user-1", Role: domain.RoleScheduler}, time.Hour)
	if err != nil {
		t.Fatalf("CreateToken returned error: %v", err)
	}

	_, err = VerifyToken("secret", token)
	if !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expected ErrInvalidToken, got %v", err)
	}
}

func TestBearerToken(t *testing.T) {
	tests := []struct {
		name    string
		header  string
		want    string
		wantErr bool
	}{
		{name: "empty", header: "", wantErr: true},
		{name: "malformed", header: "Bearer", wantErr: true},
		{name: "lowercase", header: "bearer token-1", wantErr: true},
		{name: "valid", header: "Bearer token-1", want: "token-1"},
		{name: "extra space trims token", header: "Bearer   token-1  ", want: "token-1"},
		{name: "non bearer", header: "Basic token-1", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BearerToken(tt.header)
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidToken) {
					t.Fatalf("BearerToken error = %v, want ErrInvalidToken", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("BearerToken returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("BearerToken = %q, want %q", got, tt.want)
			}
		})
	}
}
