package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password rules.
const (
	MinPasswordLength = 10
	MaxPasswordLength = 256
)

// CheckPassword returns a message for the person choosing a password when
// it is too short or too long (counted in characters), or nil.
func CheckPassword(password string) error {
	n := utf8.RuneCountInString(password)
	if n < MinPasswordLength {
		return fmt.Errorf("The password needs at least %d characters.", MinPasswordLength)
	}
	if n > MaxPasswordLength {
		return fmt.Errorf("The password can have at most %d characters.", MaxPasswordLength)
	}
	return nil
}

// argonParams are the argon2id cost settings. They are written into every
// hash, so a hash made with older settings can still be checked.
type argonParams struct {
	memory  uint32 // KiB
	time    uint32 // passes
	threads uint8
	keyLen  uint32
	saltLen uint32
}

// hashParams are used for new hashes: OWASP's recommendation for argon2id
// (19 MiB, two passes, one thread), about 20 ms on a laptop.
var hashParams = argonParams{memory: 19 * 1024, time: 2, threads: 1, keyLen: 32, saltLen: 16}

// maxMemory is the most memory a stored hash may ask for (64 MiB); a hash
// asking more is refused rather than computed.
const maxMemory = 64 * 1024

// hashSlots limits how many hashes are computed at once: each takes
// 19 MiB, so many logins at the same moment cannot run the server out of memory.
var hashSlots = make(chan struct{}, 4)

func withHashSlot(ctx context.Context, f func()) error {
	select {
	case hashSlots <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-hashSlots }()
	f()
	return nil
}

// HashPassword returns the argon2id hash of password in PHC form:
// $argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash> (base64 without padding).
func HashPassword(ctx context.Context, password string) (string, error) {
	p := hashParams
	salt := make([]byte, p.saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	var key []byte
	if err := withHashSlot(ctx, func() { key = argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen) }); err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches hash. It returns an
// error only for a hash it cannot read.
func VerifyPassword(ctx context.Context, hash, password string) (bool, error) {
	p, salt, want, err := parseHash(hash)
	if err != nil {
		return false, err
	}
	var got []byte
	if err := withHashSlot(ctx, func() { got = argon2.IDKey([]byte(password), salt, p.time, p.memory, p.threads, p.keyLen) }); err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

var errBadHash = errors.New("password hash is not in the argon2id format")

func parseHash(hash string) (argonParams, []byte, []byte, error) {
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	parts := strings.Split(hash, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, errBadHash
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return argonParams{}, nil, nil, errBadHash
	}
	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &p.memory, &p.time, &p.threads); err != nil {
		return argonParams{}, nil, nil, errBadHash
	}
	if p.memory == 0 || p.memory > maxMemory || p.time == 0 || p.time > 16 || p.threads == 0 {
		return argonParams{}, nil, nil, errBadHash
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, errBadHash
	}
	key, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) == 0 {
		return argonParams{}, nil, nil, errBadHash
	}
	p.keyLen, p.saltLen = uint32(len(key)), uint32(len(salt))
	return p, salt, key, nil
}

// dummyHash is checked against when a login names an unknown address, so
// the answer takes as long as for a known one and does not tell which
// addresses have an account.
var dummyHash = sync.OnceValue(func() string {
	hash, err := HashPassword(context.Background(), "no account has this password")
	if err != nil {
		panic(err) // only crypto/rand failing; nothing else works then either
	}
	return hash
})
