// SPDX-License-Identifier: MIT

package server

import (
	"log/slog"
	"time"

	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// Options sets the parameters of a Server. A field that is zero takes its
// default.
type Options struct {
	// Store is the storage. It is required.
	Store command.Store

	// Access holds the users. If it is nil the server has no sign in and every
	// client can run every command, so the caller must only allow it on a
	// private address.
	Access command.Access

	// Limits bound what a client can send. The default is resp.DefaultLimits.
	Limits resp.Limits

	// PreAuthLimits bound what a client can send before it signs in. The
	// default is small enough for a sign in and not for a value.
	PreAuthLimits resp.Limits

	// MaxClients is the largest number of open connections. The default is
	// 10000.
	MaxClients int

	// MaxKey is the largest size of a key, in bytes. The default is 64 KiB.
	MaxKey int

	// MaxReply is the largest total size of the values in one reply. The
	// default is 64 MiB.
	MaxReply int

	// Now returns the time that key expiry uses. The default is time.Now. Tests
	// set it to move the clock.
	Now func() time.Time

	// SweepInterval is the time between two passes of the sweeper, which removes
	// the elements of the hashes and sets that no key holds any more. The default
	// is 1 minute. A negative value turns the sweeper off.
	SweepInterval time.Duration

	// BackupDir is the directory where BGSAVE puts its checkpoints. If it is empty,
	// BGSAVE is an error.
	BackupDir string

	// MaxTxCommands is the largest number of commands that a client can queue
	// between MULTI and EXEC. The default is 10000.
	MaxTxCommands int

	// MaxTxBytes is the largest total size of the arguments of the commands that a
	// client queues between MULTI and EXEC. The default is 32 MiB.
	MaxTxBytes int

	// MaxWatch is the largest number of keys that a client can watch. The default
	// is 10000.
	MaxWatch int

	// IdleTimeout closes a connection that sends nothing for this long. The
	// default is 10 minutes. A negative value turns it off.
	IdleTimeout time.Duration

	// PreAuthTimeout closes a connection that has not signed in after this long
	// without a command. The default is 30 seconds.
	PreAuthTimeout time.Duration

	// ReadTimeout is the time that a client has to send the rest of a command
	// after its first byte. The default is 30 seconds.
	ReadTimeout time.Duration

	// WriteTimeout is the time that a client has to take what the server sends
	// it. The default is 30 seconds.
	WriteTimeout time.Duration

	// HandshakeTimeout is the time that a client has for the TLS handshake. The
	// default is 10 seconds.
	HandshakeTimeout time.Duration

	// AuthDelay is the wait after the first failed sign in of a connection. It
	// doubles with each failure. The default is 100 milliseconds.
	AuthDelay time.Duration

	// AuthDelayMax is the longest wait after a failed sign in. The default is 2
	// seconds.
	AuthDelayMax time.Duration

	// MaxAuthFailures is the number of failed sign ins after which the server
	// closes the connection. The default is 5.
	MaxAuthFailures int

	// Meter makes the metrics of the server: the commands by name and result, their
	// latency, the clients, and the counts of the storage. It can be nil, and then the
	// server keeps no metric and pays nothing. Only the OpenTelemetry API is
	// imported here, so the caller links the SDK that it wants.
	Meter metric.Meter

	// Tracer makes a span for a command that the sample picks: its name, the number
	// of its keys (never a key), its result and the size of its reply. It can be nil,
	// and then the server makes no span and pays nothing. Only the OpenTelemetry API
	// is imported here.
	Tracer trace.Tracer

	// TraceSampleRate is the part of the commands that get a span, from 0 to 1. The
	// server picks them itself, before it asks the tracer for a span, so a command
	// that is not picked costs one random number. The default is 0.
	TraceSampleRate float64

	// CertUser makes a client that presents a certificate sign in as the user that
	// is named like the common name of the certificate, if that user has the rule
	// certonly. It needs TLS with client certificates and a users file. The default is
	// false.
	CertUser bool

	// SlowLogThreshold is the time that a command must take to enter the slow log
	// and the log of the server. The default is 10 ms, and a negative value turns
	// both off.
	SlowLogThreshold time.Duration

	// SlowLogMaxLen is the number of entries that the slow log keeps. The default
	// is 128.
	SlowLogMaxLen int

	// AuthFailLimit is the number of failed sign ins from one address, within
	// AuthFailWindow, after which the server refuses the sign ins of that address
	// without checking the password. The default is 20, and a negative value turns
	// the limit off.
	AuthFailLimit int

	// AuthFailWindow is how long a failed sign in counts, and how long an address
	// stays refused. The default is 60 seconds.
	AuthFailWindow time.Duration

	// Version is the version that the server reports. The default is "dev".
	Version string

	// Settings are the values that CONFIG GET reports, by name. The server adds
	// its own limits to them. It must not hold secrets.
	Settings map[string]string

	// Log gets the events of the server. The default is slog.Default.
	Log *slog.Logger
}

const (
	defaultMaxClients       = 10000
	defaultMaxKey           = 64 << 10
	defaultMaxReply         = 64 << 20
	defaultIdleTimeout      = 10 * time.Minute
	defaultPreAuthTimeout   = 30 * time.Second
	defaultReadTimeout      = 30 * time.Second
	defaultWriteTimeout     = 30 * time.Second
	defaultHandshakeTimeout = 10 * time.Second
	defaultAuthDelay        = 100 * time.Millisecond
	defaultAuthDelayMax     = 2 * time.Second
	defaultMaxAuthFailures  = 5
	defaultSlowLogThreshold = 10 * time.Millisecond
	defaultSlowLogMaxLen    = 128
	defaultAuthFailWindow   = time.Minute
	defaultAuthFailLimit    = 20
	defaultSweepInterval    = time.Minute
	defaultMaxTxCommands    = 10000
	defaultMaxTxBytes       = 32 << 20
	defaultMaxWatch         = 10000
)

// preAuthLimits are the default limits of a client that has not signed in. They
// fit a sign in and nothing else.
var preAuthLimits = resp.Limits{
	MaxBulk:    1 << 10,
	MaxArgs:    8,
	MaxInline:  2 << 10,
	MaxDepth:   4,
	MaxNodes:   32,
	MaxRequest: 4 << 10,
}

// withDefaults returns o with the default in each field that is not set.
func (o Options) withDefaults() Options {
	o.Limits = o.Limits.WithDefaults()
	if o.PreAuthLimits == (resp.Limits{}) {
		o.PreAuthLimits = preAuthLimits
	}
	o.PreAuthLimits = o.PreAuthLimits.WithDefaults()
	if o.SlowLogThreshold == 0 {
		o.SlowLogThreshold = defaultSlowLogThreshold
	}
	if o.AuthFailLimit == 0 {
		o.AuthFailLimit = defaultAuthFailLimit
	}
	if o.AuthFailWindow <= 0 {
		o.AuthFailWindow = defaultAuthFailWindow
	}
	if o.SlowLogMaxLen <= 0 {
		o.SlowLogMaxLen = defaultSlowLogMaxLen
	}
	if o.MaxClients <= 0 {
		o.MaxClients = defaultMaxClients
	}
	if o.MaxKey <= 0 {
		o.MaxKey = defaultMaxKey
	}
	if o.MaxReply <= 0 {
		o.MaxReply = defaultMaxReply
	}
	if o.MaxTxCommands <= 0 {
		o.MaxTxCommands = defaultMaxTxCommands
	}
	if o.MaxTxBytes <= 0 {
		o.MaxTxBytes = defaultMaxTxBytes
	}
	if o.MaxWatch <= 0 {
		o.MaxWatch = defaultMaxWatch
	}
	if o.SweepInterval == 0 {
		o.SweepInterval = defaultSweepInterval
	}
	if o.IdleTimeout == 0 {
		o.IdleTimeout = defaultIdleTimeout
	}
	if o.PreAuthTimeout <= 0 {
		o.PreAuthTimeout = defaultPreAuthTimeout
	}
	if o.ReadTimeout <= 0 {
		o.ReadTimeout = defaultReadTimeout
	}
	if o.WriteTimeout <= 0 {
		o.WriteTimeout = defaultWriteTimeout
	}
	if o.HandshakeTimeout <= 0 {
		o.HandshakeTimeout = defaultHandshakeTimeout
	}
	if o.AuthDelay <= 0 {
		o.AuthDelay = defaultAuthDelay
	}
	if o.AuthDelayMax <= 0 {
		o.AuthDelayMax = defaultAuthDelayMax
	}
	if o.MaxAuthFailures <= 0 {
		o.MaxAuthFailures = defaultMaxAuthFailures
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	return o
}
