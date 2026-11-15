// SPDX-License-Identifier: MIT

package acl

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"runtime"
	"strconv"
	"strings"
)

const (
	// Iterations is the number of rounds of PBKDF2 for a new password hash.
	Iterations = 100_000

	// saltSize is the size of the salt of a password hash, in bytes.
	saltSize = 16

	// hashSize is the size of the key that PBKDF2 derives, in bytes.
	hashSize = 32

	// maxIterations is the largest number of rounds that a hash in a file can
	// ask for, so that a file cannot make every sign in very slow.
	maxIterations = 10_000_000

	// pbkdf2Prefix starts the text of a PBKDF2 password hash.
	pbkdf2Prefix = "$pbkdf2-sha256$"
)

// slots limits the number of password hashes that run at the same time. A
// PBKDF2 hash costs CPU on purpose, and without a limit many clients that try to
// sign in could take all the processors.
var slots = make(chan struct{}, max(1, runtime.GOMAXPROCS(0)))

// password is the stored form of one password of a user.
type password struct {
	legacy     bool
	iterations int
	salt       []byte
	sum        []byte
}

// HashPassword returns the text of a PBKDF2-HMAC-SHA256 hash of pw with a new
// random salt. It looks like $pbkdf2-sha256$i=100000$<salt>$<hash>, with the salt
// and the hash in unpadded base64.
func HashPassword(pw []byte) (string, error) {
	salt := make([]byte, saltSize)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	p := password{iterations: Iterations, salt: salt}
	sum, err := p.derive(pw)
	if err != nil {
		return "", err
	}
	p.sum = sum
	return p.String(), nil
}

// parsePassword reads a stored password. The text is a PBKDF2 hash or, to import
// a Redis file, a SHA-256 in hexadecimal that starts with #.
func parsePassword(text string) (password, error) {
	if strings.HasPrefix(text, "#") {
		sum, err := hex.DecodeString(text[1:])
		if err != nil || len(sum) != sha256.Size {
			return password{}, errors.New("a # password must be a SHA-256 in 64 hexadecimal digits")
		}
		return password{legacy: true, sum: sum}, nil
	}
	if !strings.HasPrefix(text, pbkdf2Prefix) {
		return password{}, errors.New("unknown password hash")
	}
	parts := strings.Split(strings.TrimPrefix(text, pbkdf2Prefix), "$")
	if len(parts) != 3 || !strings.HasPrefix(parts[0], "i=") {
		return password{}, errors.New("malformed PBKDF2 password hash")
	}
	iter, err := strconv.Atoi(parts[0][2:])
	if err != nil || iter < 1 || iter > maxIterations {
		return password{}, errors.New("invalid number of PBKDF2 iterations")
	}
	salt, err1 := base64.RawStdEncoding.DecodeString(parts[1])
	sum, err2 := base64.RawStdEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || len(salt) < saltSize || len(sum) != hashSize {
		return password{}, errors.New("malformed PBKDF2 password hash")
	}
	return password{iterations: iter, salt: salt, sum: sum}, nil
}

// String returns the text of the stored password.
func (p password) String() string {
	if p.legacy {
		return "#" + hex.EncodeToString(p.sum)
	}
	return pbkdf2Prefix + "i=" + strconv.Itoa(p.iterations) + "$" +
		base64.RawStdEncoding.EncodeToString(p.salt) + "$" + base64.RawStdEncoding.EncodeToString(p.sum)
}

// derive returns the hash of pw with the salt and the rounds of p.
func (p password) derive(pw []byte) ([]byte, error) {
	slots <- struct{}{}
	defer func() { <-slots }()
	return pbkdf2.Key(sha256.New, string(pw), p.salt, p.iterations, hashSize)
}

// verify reports whether pw is the password. It compares in constant time.
func (p password) verify(pw []byte) bool {
	if p.legacy {
		sum := sha256.Sum256(pw)
		return subtle.ConstantTimeCompare(sum[:], p.sum) == 1
	}
	sum, err := p.derive(pw)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(sum, p.sum) == 1
}

// dummy is a password that nobody has. A sign in of a user that does not exist
// or that is off checks against it, so that the time of the reply does not tell
// whether the user exists.
var dummy = func() password {
	p := password{iterations: Iterations, salt: make([]byte, saltSize), sum: make([]byte, hashSize)}
	return p
}()
