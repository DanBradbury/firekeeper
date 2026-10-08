package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"

	"golang.org/x/crypto/argon2"
)

// argon2id cost, following the OWASP minimum (19 MiB, 2 passes, 1 lane).
// The parameters are stored in each hash, so raising them later only
// affects new hashes. Tests lower them.
var hashParams = struct {
	time, memory uint32
	threads      uint8
}{time: 2, memory: 19 * 1024, threads: 1}

const (
	saltLen = 16
	keyLen  = 32
	// maxHashMemory bounds the memory a stored hash may demand when verified.
	maxHashMemory = 256 * 1024
)

// hashSlots bounds concurrent argon2 runs, since each needs tens of MiB.
var hashSlots = make(chan struct{}, 4)

// HashPassword returns the PHC-encoded argon2id hash of password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, saltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := hashParams
	key := derive(password, salt, p.time, p.memory, p.threads, keyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, p.memory, p.time, p.threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key)), nil
}

func derive(password string, salt []byte, t, m uint32, p uint8, n uint32) []byte {
	hashSlots <- struct{}{}
	defer func() { <-hashSlots }()
	return argon2.IDKey([]byte(password), salt, t, m, p, n)
}

// VerifyPassword reports whether password matches an encoded hash. A
// malformed hash never matches.
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=..,t=..,p=..", salt, key
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return false
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false
	}
	var m, t uint32
	var p uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &m, &t, &p); err != nil {
		return false
	}
	if m == 0 || m > maxHashMemory || t == 0 || t > 16 || p == 0 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) == 0 {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(want) == 0 {
		return false
	}
	got := derive(password, salt, t, m, p, uint32(len(want)))
	return subtle.ConstantTimeCompare(got, want) == 1
}

var (
	dummyOnce sync.Once
	dummyHash string
)

// burnPassword spends the time of a real verification, so a login for an
// unknown email takes as long as one for a known email.
func burnPassword(password string) {
	dummyOnce.Do(func() { dummyHash, _ = HashPassword("firekeeper-timing-pad") })
	VerifyPassword(dummyHash, password)
}
