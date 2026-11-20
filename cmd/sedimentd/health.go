// SPDX-License-Identifier: MIT

package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/config"
)

// healthEnv names the environment variables that hold the sign in of the health
// check. The password is never a flag, because a flag shows in the process list.
const (
	healthUserEnv     = "SEDIMENT_HEALTHCHECK_USER"
	healthPasswordEnv = "SEDIMENT_HEALTHCHECK_PASSWORD"
)

// healthTarget returns the address that the health check dials: the first bind
// address of the config, or the loopback address if the server listens on all of
// them.
func healthTarget(cfg config.Config) string {
	host := "127.0.0.1"
	if len(cfg.Bind) > 0 {
		host = cfg.Bind[0]
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && ip.IsUnspecified() {
		host = "127.0.0.1"
		if ip.To4() == nil {
			host = "::1"
		}
	}
	return net.JoinHostPort(strings.Trim(host, "[]"), strconv.Itoa(cfg.Port))
}

// healthTLS returns the TLS settings of the health check. It checks the server
// against the CA file of the config, if there is one, and presents the certificate
// of the server as its own if the server asks for client certificates.
func healthTLS(cfg config.Config, serverName string) (*tls.Config, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: serverName}
	if cfg.TLSCA != "" {
		pem, err := os.ReadFile(cfg.TLSCA)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("tls-ca-cert-file has no certificate")
		}
		tc.RootCAs = pool
		cert, err := tls.LoadX509KeyPair(cfg.TLSCert, cfg.TLSKey)
		if err != nil {
			return nil, err
		}
		tc.Certificates = []tls.Certificate{cert}
	}
	return tc, nil
}

// roundTrip sends one command and reads its reply.
func roundTrip(nc net.Conn, r *resp.Reader, w *resp.Writer, args ...string) (resp.Value, error) {
	items := make([]resp.Value, len(args))
	for i, a := range args {
		items[i] = resp.BulkString(a)
	}
	if err := w.WriteValue(resp.Array(items...)); err != nil {
		return resp.Value{}, err
	}
	if err := w.Flush(); err != nil {
		return resp.Value{}, err
	}
	return r.ReadValue()
}

// healthcheck asks the server on the address of the config to PING. It succeeds if
// the server answers PONG, and also if it answers that it needs a sign in, because
// then it is alive and cannot be asked more. It fails for a node that is loading,
// that stopped after an error, or that does not answer. If the environment holds a
// password, it signs in first.
func healthcheck(args []string, stderr io.Writer, getenv func(string) string) error {
	fs := flag.NewFlagSet("sedimentd healthcheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "file of settings, the same as the server")
	bind := fs.String("bind", "", "address of the server")
	port := fs.Int("port", 0, "TCP port of the server")
	serverName := fs.String("servername", "localhost", "name that the TLS certificate of the server must have")
	timeout := fs.Duration("timeout", 3*time.Second, "time to wait for the whole check")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg := config.Defaults()
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := config.Parse(f, &cfg); err != nil {
			return fmt.Errorf("%s: %w", *file, err)
		}
	}
	var setErr error
	fs.Visit(func(f *flag.Flag) {
		var err error
		switch f.Name {
		case "bind":
			err = cfg.Set("bind", []string{*bind})
		case "port":
			err = cfg.Set("port", []string{strconv.Itoa(*port)})
		}
		if err != nil && setErr == nil {
			setErr = fmt.Errorf("-%s: %w", f.Name, err)
		}
	})
	if setErr != nil {
		return setErr
	}

	deadline := time.Now().Add(*timeout)
	dialer := net.Dialer{Deadline: deadline}
	var nc net.Conn
	var err error
	if cfg.TLSEnabled() {
		tc, terr := healthTLS(cfg, *serverName)
		if terr != nil {
			return terr
		}
		nc, err = (&tls.Dialer{NetDialer: &dialer, Config: tc}).Dial("tcp", healthTarget(cfg))
	} else {
		nc, err = dialer.Dial("tcp", healthTarget(cfg))
	}
	if err != nil {
		return err
	}
	defer nc.Close()
	_ = nc.SetDeadline(deadline)
	r, w := resp.NewReader(nc, resp.Limits{MaxBulk: 1 << 20}), resp.NewWriter(nc)

	if pw := getenv(healthPasswordEnv); pw != "" {
		user := getenv(healthUserEnv)
		if user == "" {
			user = "default"
		}
		v, err := roundTrip(nc, r, w, "AUTH", user, pw)
		if err != nil {
			return err
		}
		if v.Kind == resp.KindError {
			return errors.New("the health check could not sign in")
		}
	}
	v, err := roundTrip(nc, r, w, "PING")
	if err != nil {
		return err
	}
	switch {
	case v.Kind == resp.KindSimple && string(v.Bytes) == "PONG":
		return nil
	case v.Kind == resp.KindError && strings.HasPrefix(string(v.Bytes), "NOAUTH"):
		return nil
	case v.Kind == resp.KindError:
		return fmt.Errorf("the server is not ready: %s", firstWord(string(v.Bytes)))
	}
	return errors.New("the server answered something other than PONG")
}

// firstWord returns the first word of s, which is the code of an error reply.
func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}
