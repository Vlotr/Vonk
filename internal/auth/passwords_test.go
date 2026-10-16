package auth

import (
	"context"
	"strings"
	"testing"
)

func TestHashPassword_Argon2idAndVerify(t *testing.T) {
	ctx := context.Background()
	hash, err := HashPassword(ctx, "correct horse battery")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=") || strings.Contains(hash, "correct horse") {
		t.Errorf("hash = %q, want a PHC argon2id string without the password", hash)
	}
	if ok, err := VerifyPassword(ctx, hash, "correct horse battery"); err != nil || !ok {
		t.Errorf("right password: ok=%v err=%v", ok, err)
	}
	if ok, err := VerifyPassword(ctx, hash, "correct horse batterY"); err != nil || ok {
		t.Errorf("wrong password: ok=%v err=%v", ok, err)
	}

	// The same password twice gets two salts, so two different hashes.
	again, _ := HashPassword(ctx, "correct horse battery")
	if again == hash {
		t.Error("two hashes of one password are equal: the salt is not random")
	}
}

func TestVerifyPassword_ReadsParametersFromTheHash(t *testing.T) {
	ctx := context.Background()
	// Made with different parameters than new hashes use: old hashes keep working when they change.
	old := hashParams
	hashParams = argonParams{memory: 8 * 1024, time: 1, threads: 2, keyLen: 32, saltLen: 16}
	hash, err := HashPassword(ctx, "an older password")
	hashParams = old
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(hash, "m=8192,t=1,p=2") {
		t.Fatalf("hash = %q", hash)
	}
	if ok, err := VerifyPassword(ctx, hash, "an older password"); err != nil || !ok {
		t.Errorf("verify with the hash's own parameters: ok=%v err=%v", ok, err)
	}

	for _, bad := range []string{"", "plain", "$argon2i$v=19$m=8192,t=1,p=2$c2FsdA$aGFzaA", "$argon2id$v=19$m=x,t=1,p=1$c2FsdA$aGFzaA",
		"$argon2id$v=19$m=999999999,t=1,p=1$c2FsdA$aGFzaA"} {
		if ok, err := VerifyPassword(ctx, bad, "x"); ok || err == nil {
			t.Errorf("VerifyPassword(%q): ok=%v err=%v, want an error", bad, ok, err)
		}
	}
}

func TestCheckPassword_Length(t *testing.T) {
	for pw, ok := range map[string]bool{
		"":                      false,
		"123456789":             false,
		"1234567890":            true,
		"ééééééééé":             false, // 9 characters, 18 bytes
		"éééééééééé":            true,
		strings.Repeat("a", 256): true,
		strings.Repeat("a", 257): false,
	} {
		if err := CheckPassword(pw); (err == nil) != ok {
			t.Errorf("CheckPassword(%d chars) = %v, want ok=%v", len([]rune(pw)), err, ok)
		}
	}
}
