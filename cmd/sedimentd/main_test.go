// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/thalesdev/sediment/resp"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "sedimentd")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	if err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

type process struct {
	cmd *exec.Cmd
	log *bytes.Buffer
}

func start(t *testing.T, bin string, args ...string) *process {
	t.Helper()
	p := &process{cmd: exec.Command(bin, args...), log: &bytes.Buffer{}}
	p.cmd.Stderr = p.log
	if err := p.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = p.cmd.Process.Kill()
		_, _ = p.cmd.Process.Wait()
	})
	return p
}

func connect(t *testing.T, port int) (net.Conn, *resp.Reader, *resp.Writer) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		nc, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		if err == nil {
			t.Cleanup(func() { nc.Close() })
			return nc, resp.NewReader(nc, resp.Limits{}), resp.NewWriter(nc)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server did not start: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func call(t *testing.T, nc net.Conn, r *resp.Reader, w *resp.Writer, args ...string) resp.Value {
	t.Helper()
	items := make([]resp.Value, len(args))
	for i, a := range args {
		items[i] = resp.BulkString(a)
	}
	_ = w.WriteValue(resp.Array(items...))
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	_ = nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	v, err := r.ReadValue()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestProcess(t *testing.T) {
	if testing.Short() {
		t.Skip("starts a process")
	}
	bin := buildBinary(t)

	t.Run("Should not lose an acknowledged write after kill -9", func(t *testing.T) {
		dir := t.TempDir()
		port := freePort(t)
		p := start(t, bin, "-dir", dir, "-port", fmt.Sprint(port))
		nc, r, w := connect(t, port)
		for i := range 500 {
			if v := call(t, nc, r, w, "SET", fmt.Sprintf("k%d", i), fmt.Sprintf("v%d", i)); string(v.Bytes) != "OK" {
				t.Fatalf("set %d: %+v", i, v)
			}
		}
		if v := call(t, nc, r, w, "MSET", "a", "1", "b", "2"); string(v.Bytes) != "OK" {
			t.Fatalf("mset: %+v", v)
		}
		if err := p.cmd.Process.Signal(syscall.SIGKILL); err != nil {
			t.Fatal(err)
		}
		_, _ = p.cmd.Process.Wait()

		start(t, bin, "-dir", dir, "-port", fmt.Sprint(port))
		nc, r, w = connect(t, port)
		for i := range 500 {
			v := call(t, nc, r, w, "GET", fmt.Sprintf("k%d", i))
			if string(v.Bytes) != fmt.Sprintf("v%d", i) {
				t.Fatalf("get %d: %+v", i, v)
			}
		}
		if v := call(t, nc, r, w, "MGET", "a", "b"); len(v.Elems) != 2 || string(v.Elems[1].Bytes) != "2" {
			t.Fatalf("mget: %+v", v)
		}
	})

	t.Run("Should stop cleanly on SIGTERM", func(t *testing.T) {
		port := freePort(t)
		p := start(t, bin, "-dir", t.TempDir(), "-port", fmt.Sprint(port))
		nc, r, w := connect(t, port)
		call(t, nc, r, w, "SET", "k", "v")
		if err := p.cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- p.cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("exit: %v\n%s", err, p.log)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("the server did not stop")
		}
		if !strings.Contains(p.log.String(), "stopping") {
			t.Fatalf("log: %s", p.log)
		}
	})

	t.Run("Should refuse a public address with no TLS and no users", func(t *testing.T) {
		out, err := exec.Command(bin, "-dir", t.TempDir(), "-bind", "0.0.0.0", "-port", fmt.Sprint(freePort(t))).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "refusing to listen") {
			t.Fatalf("err %v\n%s", err, out)
		}
	})

	t.Run("Should start on a public address with a sign in", func(t *testing.T) {
		dir := t.TempDir()
		acl := filepath.Join(dir, "users.acl")
		if err := os.WriteFile(acl, []byte("user default off\nuser app on nopass +@all\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		port := freePort(t)
		start(t, bin, "-dir", filepath.Join(dir, "db"), "-bind", "0.0.0.0", "-port", fmt.Sprint(port), "-aclfile", acl)
		nc, r, w := connect(t, port)
		if v := call(t, nc, r, w, "GET", "k"); !strings.HasPrefix(string(v.Bytes), "NOAUTH") {
			t.Fatalf("got %+v", v)
		}
		if v := call(t, nc, r, w, "AUTH", "app", "x"); string(v.Bytes) != "OK" {
			t.Fatalf("got %+v", v)
		}
	})

	t.Run("Should refuse an ACL file that others can read", func(t *testing.T) {
		dir := t.TempDir()
		acl := filepath.Join(dir, "users.acl")
		if err := os.WriteFile(acl, []byte("user default on nopass +@all\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, "-dir", filepath.Join(dir, "db"), "-port", fmt.Sprint(freePort(t)), "-aclfile", acl).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "chmod 600") {
			t.Fatalf("err %v\n%s", err, out)
		}
	})

	t.Run("Should print the version", func(t *testing.T) {
		out, err := exec.Command(bin, "-version").CombinedOutput()
		if err != nil || !strings.HasPrefix(string(out), "sedimentd") {
			t.Fatalf("err %v\n%s", err, out)
		}
	})

	t.Run("Should refuse a bad setting", func(t *testing.T) {
		cfg := filepath.Join(t.TempDir(), "sediment.conf")
		if err := os.WriteFile(cfg, []byte("port 6379\ntls-cert-fil x\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := exec.Command(bin, "-config", cfg).CombinedOutput()
		if err == nil || !strings.Contains(string(out), "unknown setting") {
			t.Fatalf("err %v\n%s", err, out)
		}
	})

	t.Run("Should serve TLS 1.3 on a public address", func(t *testing.T) {
		dir := t.TempDir()
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "localhost"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
			DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")},
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
		if err != nil {
			t.Fatal(err)
		}
		keyDER, _ := x509.MarshalECPrivateKey(key)
		certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
			t.Fatal(err)
		}
		port := freePort(t)
		conf := filepath.Join(dir, "sediment.conf")
		text := fmt.Sprintf("bind 0.0.0.0\nport %d\ndir %s\ntls-cert-file %s\ntls-key-file %s\n", port, filepath.Join(dir, "db"), certPath, keyPath)
		if err := os.WriteFile(conf, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
		start(t, bin, "-config", conf)
		pool := x509.NewCertPool()
		pool.AddCert(mustParse(t, der))
		var nc *tls.Conn
		deadline := time.Now().Add(10 * time.Second)
		for {
			nc, err = tls.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port), &tls.Config{RootCAs: pool, ServerName: "localhost"})
			if err == nil {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal(err)
			}
			time.Sleep(20 * time.Millisecond)
		}
		defer nc.Close()
		if nc.ConnectionState().Version != tls.VersionTLS13 {
			t.Fatalf("version %x", nc.ConnectionState().Version)
		}
		if v := call(t, nc, resp.NewReader(nc, resp.Limits{}), resp.NewWriter(nc), "PING"); string(v.Bytes) != "PONG" {
			t.Fatalf("got %+v", v)
		}
	})
}

func mustParse(t *testing.T, der []byte) *x509.Certificate {
	t.Helper()
	c, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return c
}
