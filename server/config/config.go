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

	// BlockCacheSize is the size in bytes of the cache of data blocks of the tables. The
	// default is 64 MiB.
	BlockCacheSize int

	// MaxClients is the largest number of connections, or 0 for the default.
	MaxClients int

	// SlowLogSlowerThan is the time in microseconds that a command must take to enter
	// the slow log. 0 logs every command and a negative value turns the log off.
	// The default is 10000.
	SlowLogSlowerThan int

	// SlowLogMaxLen is the number of entries of the slow log. The default is 128.
	SlowLogMaxLen int

	// AuthFailLimit is the number of failed sign ins from one address, in the
	// window, after which the server refuses the sign ins of that address. 0 turns
	// the limit off. The default is 20.
	AuthFailLimit int

	// AuthFailWindow is the time that the failures of an address count for, and the
	// time that the address stays refused. The default is 60 seconds.
	AuthFailWindow time.Duration

	// Timeout closes a client that is idle for this long. A zero value is the
	// default of the server. A negative value turns it off.
	Timeout time.Duration

	// SweepInterval is the time between two passes of the sweeper that removes the
	// elements of hashes and sets that no key holds. A zero value is the default of
	// the server, 1 minute. A negative value turns it off.
	SweepInterval time.Duration

	// BackupDir is the directory where BGSAVE puts its checkpoints. If it is empty,
	// BGSAVE is an error.
	BackupDir string

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

	// LogFormat is text or json. The default is text.
	LogFormat string
}

// Defaults returns the default settings: loopback only, port 6379, no TLS, no
// users.
func Defaults() Config {
	return Config{Bind: []string{"127.0.0.1"}, Port: 6379, Dir: "data", MemtableSize: 64 << 20, BlockCacheSize: 64 << 20, LogLevel: "info", LogFormat: "text", SlowLogSlowerThan: 10000, SlowLogMaxLen: 128, AuthFailLimit: 20, AuthFailWindow: time.Minute}
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
		return setInt(&c.MemtableSize, key, val, 64<<10, 1<<30)
	case "block-cache-size":
		return setInt(&c.BlockCacheSize, key, val, 1<<20, 1<<34)
	case "slowlog-log-slower-than":
		n, err := strconv.Atoi(val)
		if err != nil || n < -1 || n > 1<<30 {
			return errors.New("slowlog-log-slower-than must be -1 or a number of microseconds")
		}
		c.SlowLogSlowerThan = n
	case "slowlog-max-len":
		return setInt(&c.SlowLogMaxLen, key, val, 1, 1<<20)
	case "auth-fail-limit":
		return setInt(&c.AuthFailLimit, key, val, 0, 1<<20)
	case "auth-fail-window":
		return setSeconds(&c.AuthFailWindow, key, val, false)
	case "maxclients":
		return setInt(&c.MaxClients, key, val, 1, 1<<20)
	case "timeout":
		return setSeconds(&c.Timeout, key, val, true)
	case "sweep-interval":
		return setSeconds(&c.SweepInterval, key, val, true)
	case "backup-dir":
		c.BackupDir = val
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
	case "log-format":
		switch val {
		case "text", "json":
			c.LogFormat = val
		default:
			return errors.New("log-format must be text or json")
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
		"log-format":    c.LogFormat,
		"insecure-bind": yes(c.InsecureBind),
	}
}
