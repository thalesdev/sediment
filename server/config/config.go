// SPDX-License-Identifier: MIT

// Package config reads the settings of sedimentd. The settings come from a file
// with one "key value" pair per line, like redis.conf, and a few of them can be
// overridden by flags. A secret never goes in a flag, because a flag shows in
// the process list. An unknown key is an error, so a typo cannot turn a security
// setting off.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"time"
)

// Config is the settings of the server.
type Config struct {
	// Bind lists the addresses to listen on. The default is 127.0.0.1.
	Bind []string

	// Port is the TCP port. The default is 6379.
	Port int

	// Dir is the directory of the database. The default is ./data.
	Dir string

	// Sync makes each write wait for the disk. The default is false.
	Sync bool

	// MemtableSize is the size of the memtable in bytes. The default is 64 MiB.
	// A server writes much faster than the 4 MiB of the engine default, and a
	// small memtable makes the writes wait for the flush.
	MemtableSize int

	// MaxClients is the largest number of connections, or 0 for the default.
	MaxClients int

	// Timeout closes a client that is idle for this long. A zero value is the
	// default of the server. A negative value turns it off.
	Timeout time.Duration

	// ReadTimeout is the time to send the rest of a command.
	ReadTimeout time.Duration

	// WriteTimeout is the time to take a reply.
	WriteTimeout time.Duration

	// PreAuthTimeout closes a client that does not sign in after this long.
	PreAuthTimeout time.Duration

	// MaxBulk is the largest size of a value, in bytes.
	MaxBulk int

	// MaxArgs is the largest number of arguments of a command.
	MaxArgs int

	// MaxKey is the largest size of a key, in bytes.
	MaxKey int

	// TLSCert and TLSKey are the files of the certificate and of its key.
	TLSCert, TLSKey string

	// TLSCA is the file of the certificates that sign the certificates of the
	// clients. If it is set, every client needs a valid certificate.
	TLSCA string

	// ACLFile is the file of the users.
	ACLFile string

	// InsecureBind allows an address that is not loopback with no TLS and no
	// sign in. The default is false.
	InsecureBind bool

	// LogLevel is debug, info, warn or error. The default is info.
	LogLevel string
}

// Defaults returns the default settings: loopback only, port 6379, no TLS, no
// users.
func Defaults() Config {
	return Config{Bind: []string{"127.0.0.1"}, Port: 6379, Dir: "data", MemtableSize: 64 << 20, LogLevel: "info"}
}

// Parse reads a file of "key value" lines into c. A line that is empty or that
// starts with # is skipped. The keys are in lower case.
func Parse(in io.Reader, c *Config) error {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if err := c.Set(strings.ToLower(fields[0]), fields[1:]); err != nil {
			return fmt.Errorf("line %d: %w", n, err)
		}
	}
	return sc.Err()
}

// Set changes the setting with the name key to the words of its value.
func (c *Config) Set(key string, v []string) error {
	if key == "bind" {
		if len(v) == 0 {
			return errors.New("bind needs at least one address")
		}
		c.Bind = v
		return nil
	}
	if len(v) != 1 {
		return fmt.Errorf("%s needs exactly one value", key)
	}
	val := v[0]
	switch key {
	case "port":
		return setInt(&c.Port, key, val, 1, 65535)
	case "dir":
		c.Dir = val
	case "sync":
		return setBool(&c.Sync, key, val)
	case "memtable-size":
		return setInt(&c.MemtableSize, key, val, 64<<10, 1<<34)
	case "maxclients":
		return setInt(&c.MaxClients, key, val, 1, 1<<20)
	case "timeout":
		return setSeconds(&c.Timeout, key, val, true)
	case "read-timeout":
		return setSeconds(&c.ReadTimeout, key, val, false)
	case "write-timeout":
		return setSeconds(&c.WriteTimeout, key, val, false)
	case "preauth-timeout":
		return setSeconds(&c.PreAuthTimeout, key, val, false)
	case "proto-max-bulk-len":
		return setInt(&c.MaxBulk, key, val, 1, 512<<20)
	case "max-args":
		return setInt(&c.MaxArgs, key, val, 1, 1<<20)
	case "max-key-len":
		return setInt(&c.MaxKey, key, val, 1, 512<<20)
	case "tls-cert-file":
		c.TLSCert = val
	case "tls-key-file":
		c.TLSKey = val
	case "tls-ca-cert-file":
		c.TLSCA = val
	case "aclfile":
		c.ACLFile = val
	case "insecure-bind":
		return setBool(&c.InsecureBind, key, val)
	case "loglevel":
		switch val {
		case "debug", "info", "warn", "error":
			c.LogLevel = val
		default:
			return errors.New("loglevel must be debug, info, warn or error")
		}
	default:
		return fmt.Errorf("unknown setting %q", printable(key))
	}
	return nil
}

// setInt parses an integer in [lo, hi].
func setInt(dst *int, key, val string, lo, hi int) error {
	n, err := strconv.Atoi(val)
	if err != nil || n < lo || n > hi {
		return fmt.Errorf("%s must be a number from %d to %d", key, lo, hi)
	}
	*dst = n
	return nil
}

// setBool parses yes or no.
func setBool(dst *bool, key, val string) error {
	switch strings.ToLower(val) {
	case "yes":
		*dst = true
	case "no":
		*dst = false
	default:
		return fmt.Errorf("%s must be yes or no", key)
	}
	return nil
}

// setSeconds parses a number of seconds. If zeroOff is true, 0 means that the
// timeout is off, and the stored value is negative.
func setSeconds(dst *time.Duration, key, val string, zeroOff bool) error {
	n, err := strconv.Atoi(val)
	if err != nil || n < 0 || n > 86400*30 {
		return fmt.Errorf("%s must be a number of seconds from 0 to 2592000", key)
	}
	if n == 0 && zeroOff {
		*dst = -1
		return nil
	}
	if n == 0 {
		return fmt.Errorf("%s must be at least 1 second", key)
	}
	*dst = time.Duration(n) * time.Second
	return nil
}

// printable returns s cut to 64 bytes, with each byte that is not printable
// ASCII replaced by a question mark.
func printable(s string) string {
	if len(s) > 64 {
		s = s[:64]
	}
	b := []byte(s)
	for i, c := range b {
		if c < 0x20 || c > 0x7e {
			b[i] = '?'
		}
	}
	return string(b)
}

// TLSEnabled reports whether the server serves TLS.
func (c Config) TLSEnabled() bool {
	return c.TLSCert != ""
}

// Validate checks the settings that depend on each other.
func (c Config) Validate() error {
	if (c.TLSCert == "") != (c.TLSKey == "") {
		return errors.New("tls-cert-file and tls-key-file must be set together")
	}
	if c.TLSCA != "" && c.TLSCert == "" {
		return errors.New("tls-ca-cert-file needs tls-cert-file and tls-key-file")
	}
	for _, b := range c.Bind {
		if b == "" {
			return errors.New("an empty bind address")
		}
	}
	return nil
}

// Exposed reports whether any bind address can be reached from another machine.
// Only 127.0.0.0/8, ::1 and the name localhost are not exposed.
func (c Config) Exposed() bool {
	for _, b := range c.Bind {
		if strings.EqualFold(b, "localhost") {
			continue
		}
		if ip := net.ParseIP(strings.Trim(b, "[]")); ip != nil && ip.IsLoopback() {
			continue
		}
		return true
	}
	return false
}

// CheckExposure returns an error if the server would listen on an address that
// other machines reach, with no TLS and no sign in, unless InsecureBind is set.
// signInRequired tells whether a client must sign in.
func (c Config) CheckExposure(signInRequired bool) error {
	if !c.Exposed() || c.TLSEnabled() || signInRequired || c.InsecureBind {
		return nil
	}
	return errors.New("refusing to listen on " + strings.Join(c.Bind, " ") + " with no TLS and no sign in: " +
		"set tls-cert-file and tls-key-file, or an aclfile where the default user is off, " +
		"or insecure-bind yes if the network is private")
}

// Settings returns the settings that CONFIG GET can show. They hold no file
// names and no secret.
func (c Config) Settings() map[string]string {
	yes := func(b bool) string {
		if b {
			return "yes"
		}
		return "no"
	}
	return map[string]string{
		"bind":          strings.Join(c.Bind, " "),
		"port":          strconv.Itoa(c.Port),
		"sync":          yes(c.Sync),
		"tls":           yes(c.TLSEnabled()),
		"tls-auth":      yes(c.TLSCA != ""),
		"acl":           yes(c.ACLFile != ""),
		"max-args":      strconv.Itoa(c.MaxArgs),
		"loglevel":      c.LogLevel,
		"insecure-bind": yes(c.InsecureBind),
	}
}
