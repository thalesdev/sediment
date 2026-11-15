// SPDX-License-Identifier: MIT

// Command sedimentd runs the sediment server: the Shale storage engine behind a
// RESP2 and RESP3 listener.
//
//	sedimentd [-config file] [-dir dir] [-bind address] [-port port] [-aclfile file]
//	sedimentd hash-password
//
// The second form reads a password from standard input, without the final line
// break, and prints its hash for the ACL file. The password never goes in an
// argument, because arguments show in the process list.
//
// The server listens on 127.0.0.1 only, with no TLS and no users, unless the
// settings say otherwise. It refuses to listen on an address that other machines
// reach unless it has TLS or a sign in. SIGINT and SIGTERM stop it after the
// running commands finish. SIGHUP reads the TLS certificate again.
package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server"
	"github.com/thalesdev/sediment/server/acl"
	"github.com/thalesdev/sediment/server/command"
	"github.com/thalesdev/sediment/server/config"
)

// version is the version of the server. The build sets it.
var version = "dev"

// shutdownTimeout is how long the server waits for the clients on a stop.
const shutdownTimeout = 30 * time.Second

// main runs the server and exits with 1 on an error.
func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "sedimentd:", err)
		os.Exit(1)
	}
}

// loadConfig reads the flags and the config file. A flag wins over the file.
func loadConfig(args []string, stderr io.Writer) (cfg config.Config, showVersion bool, err error) {
	fs := flag.NewFlagSet("sedimentd", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("config", "", "file of settings, one 'key value' per line")
	dir := fs.String("dir", "", "directory of the database")
	bind := fs.String("bind", "", "address to listen on")
	port := fs.Int("port", 0, "TCP port")
	aclfile := fs.String("aclfile", "", "file of users")
	ver := fs.Bool("version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return cfg, false, err
	}
	cfg = config.Defaults()
	if *file != "" {
		f, err := os.Open(*file)
		if err != nil {
			return cfg, false, err
		}
		defer f.Close()
		if err := config.Parse(f, &cfg); err != nil {
			return cfg, false, fmt.Errorf("%s: %w", *file, err)
		}
	}
	var setErr error
	fs.Visit(func(f *flag.Flag) {
		var err error
		switch f.Name {
		case "dir":
			err = cfg.Set("dir", []string{*dir})
		case "bind":
			err = cfg.Set("bind", []string{*bind})
		case "port":
			err = cfg.Set("port", []string{strconv.Itoa(*port)})
		case "aclfile":
			err = cfg.Set("aclfile", []string{*aclfile})
		}
		if err != nil && setErr == nil {
			setErr = fmt.Errorf("-%s: %w", f.Name, err)
		}
	})
	if setErr != nil {
		return cfg, false, setErr
	}
	return cfg, *ver, cfg.Validate()
}

// certHolder keeps the certificate of the server, so that a SIGHUP can replace it
// while the server runs.
type certHolder struct {
	cert atomic.Pointer[tls.Certificate]
	cfg  config.Config
}

// load reads the certificate and its key. It keeps the old certificate if it
// fails.
func (h *certHolder) load() error {
	c, err := tls.LoadX509KeyPair(h.cfg.TLSCert, h.cfg.TLSKey)
	if err != nil {
		return err
	}
	h.cert.Store(&c)
	return nil
}

// tlsConfig returns the TLS settings: TLS 1.3 only, and a client certificate if
// a CA file is set.
func (h *certHolder) tlsConfig() (*tls.Config, error) {
	if err := h.load(); err != nil {
		return nil, err
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS13,
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) { return h.cert.Load(), nil },
	}
	if h.cfg.TLSCA != "" {
		pem, err := os.ReadFile(h.cfg.TLSCA)
		if err != nil {
			return nil, err
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("tls-ca-cert-file has no certificate")
		}
		cfg.ClientCAs = pool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	return cfg, nil
}

// newLogger returns the logger of the server, with the level of the config.
func newLogger(w io.Writer, level string) *slog.Logger {
	var l slog.Level
	_ = l.UnmarshalText([]byte(level))
	return slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: l}))
}

// maxPassword is the longest password that hash-password reads, in bytes.
const maxPassword = 1 << 10

// hashPassword reads a password from in and writes its hash to out.
func hashPassword(in io.Reader, out io.Writer) error {
	pw, err := io.ReadAll(io.LimitReader(in, maxPassword+1))
	if err != nil {
		return err
	}
	pw = bytes.TrimSuffix(bytes.TrimSuffix(pw, []byte("\n")), []byte("\r"))
	if len(pw) == 0 {
		return errors.New("the password is empty")
	}
	if len(pw) > maxPassword {
		return fmt.Errorf("the password is longer than %d bytes", maxPassword)
	}
	hash, err := acl.HashPassword(pw)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, hash)
	return err
}

// run starts the server and blocks until it stops.
func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) == 1 && args[0] == "hash-password" {
		return hashPassword(stdin, stdout)
	}
	cfg, showVersion, err := loadConfig(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if showVersion {
		fmt.Fprintln(stderr, "sedimentd", version)
		return nil
	}
	log := newLogger(stderr, cfg.LogLevel)

	var access command.Access
	if cfg.ACLFile != "" {
		known := command.NewRegistry()
		reg, warnings, err := acl.Load(cfg.ACLFile, acl.ParseOptions{KnownCommand: func(n string) bool { return known.Lookup([]byte(n)) != nil }})
		if err != nil {
			return fmt.Errorf("aclfile: %w", err)
		}
		for _, w := range warnings {
			log.Warn("acl", "warning", w)
		}
		access = reg
	}
	if err := cfg.CheckExposure(access != nil && access.RequireAuth()); err != nil {
		return err
	}
	if cfg.Exposed() && !cfg.TLSEnabled() {
		log.Warn("listening on a network address without TLS, the traffic can be read and changed")
	}

	var tlsCfg *tls.Config
	if cfg.TLSEnabled() {
		holder := &certHolder{cfg: cfg}
		if tlsCfg, err = holder.tlsConfig(); err != nil {
			return fmt.Errorf("tls: %w", err)
		}
		hup := make(chan os.Signal, 1)
		signal.Notify(hup, syscall.SIGHUP)
		defer signal.Stop(hup)
		go func() {
			for range hup {
				if err := holder.load(); err != nil {
					log.Error("tls certificate not reloaded, the old one stays", "err", err)
				} else {
					log.Info("tls certificate reloaded")
				}
			}
		}()
	}

	if err := os.MkdirAll(cfg.Dir, 0o700); err != nil {
		return err
	}
	db, err := shale.Open(cfg.Dir, shale.Options{Sync: cfg.Sync, MemtableSize: cfg.MemtableSize, BlockCacheSize: cfg.BlockCacheSize})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Error("close database", "err", err)
		}
	}()
	if rec := db.Recovery(); rec.ManifestDroppedBytes > 0 {
		log.Warn("the MANIFEST had damaged bytes, table files were moved to lost/", "tables", rec.KeptTables)
	}

	srv, err := server.New(server.Options{
		Store:          db,
		Access:         access,
		Limits:         resp.Limits{MaxBulk: cfg.MaxBulk, MaxArgs: cfg.MaxArgs},
		MaxClients:     cfg.MaxClients,
		MaxKey:         cfg.MaxKey,
		IdleTimeout:    cfg.Timeout,
		ReadTimeout:    cfg.ReadTimeout,
		WriteTimeout:   cfg.WriteTimeout,
		PreAuthTimeout: cfg.PreAuthTimeout,
		Version:        version,
		Settings:       cfg.Settings(),
		Log:            log,
	})
	if err != nil {
		return err
	}

	var listeners []net.Listener
	for _, b := range cfg.Bind {
		l, err := net.Listen("tcp", net.JoinHostPort(b, strconv.Itoa(cfg.Port)))
		if err != nil {
			for _, open := range listeners {
				_ = open.Close()
			}
			return err
		}
		listeners = append(listeners, l)
		log.Info("listening", "addr", l.Addr().String(), "tls", tlsCfg != nil, "mtls", cfg.TLSCA != "", "users", access != nil, "version", version)
		if tlsCfg != nil {
			listeners[len(listeners)-1] = server.TLSListener(l, tlsCfg)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	failed := make(chan error, len(listeners))
	var wg sync.WaitGroup
	for _, l := range listeners {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := srv.Serve(l); err != nil && !errors.Is(err, server.ErrServerClosed) {
				failed <- err
			}
		}()
	}
	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("stopping")
	case serveErr = <-failed:
	}
	sctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil {
		log.Warn("some clients were closed on the stop", "err", err)
	}
	wg.Wait()
	return serveErr
}
