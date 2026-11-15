// SPDX-License-Identifier: MIT

package server

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net"
	"testing"
	"time"
)

type pki struct {
	pool   *x509.CertPool
	server tls.Certificate
	client tls.Certificate
	other  tls.Certificate
}

func newCert(t *testing.T, name string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool, usage x509.ExtKeyUsage) (*x509.Certificate, *ecdsa.PrivateKey, tls.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, big.NewInt(1<<62))
	tmpl := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, BasicConstraintsValid: true, IsCA: isCA,
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	if isCA {
		tmpl.KeyUsage |= x509.KeyUsageCertSign
	} else {
		tmpl.ExtKeyUsage = []x509.ExtKeyUsage{usage}
	}
	signer, signerKey := tmpl, key
	if parent != nil {
		signer, signerKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func newPKI(t *testing.T) pki {
	t.Helper()
	ca, caKey, _ := newCert(t, "ca", nil, nil, true, 0)
	_, _, server := newCert(t, "server", ca, caKey, false, x509.ExtKeyUsageServerAuth)
	_, _, client := newCert(t, "client", ca, caKey, false, x509.ExtKeyUsageClientAuth)
	otherCA, otherKey, _ := newCert(t, "other-ca", nil, nil, true, 0)
	_, _, other := newCert(t, "stranger", otherCA, otherKey, false, x509.ExtKeyUsageClientAuth)
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return pki{pool: pool, server: server, client: client, other: other}
}

func TestTLS(t *testing.T) {
	p := newPKI(t)

	serve := func(t *testing.T, mutual bool, o Options) string {
		t.Helper()
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{p.server}}
		if mutual {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
			cfg.ClientCAs = p.pool
		}
		return startOn(t, o, func(l net.Listener) net.Listener { return TLSListener(l, cfg) })
	}

	t.Run("Should serve over TLS 1.3", func(t *testing.T) {
		addr := serve(t, false, Options{})
		nc, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: p.pool, ServerName: "localhost"})
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		if nc.ConnectionState().Version != tls.VersionTLS13 {
			t.Fatalf("got version %x", nc.ConnectionState().Version)
		}
		c := newClient(t, nc)
		wantSimple(t, c.do("PING"), "PONG")
	})

	t.Run("Should refuse TLS 1.2", func(t *testing.T) {
		addr := serve(t, false, Options{})
		_, err := tls.Dial("tcp", addr, &tls.Config{RootCAs: p.pool, ServerName: "localhost", MaxVersion: tls.VersionTLS12})
		if err == nil {
			t.Fatal("TLS 1.2 was accepted")
		}
	})

	t.Run("Should refuse plain text on a TLS port", func(t *testing.T) {
		addr := serve(t, false, Options{})
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte("PING\r\n"))
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})

	t.Run("Should need a client certificate for mTLS", func(t *testing.T) {
		addr := serve(t, true, Options{})
		cfg := &tls.Config{RootCAs: p.pool, ServerName: "localhost"}
		nc, err := tls.Dial("tcp", addr, cfg)
		if err == nil {
			c := newClient(t, nc)
			_ = nc.SetReadDeadline(time.Now().Add(2 * time.Second))
			c.send("PING")
			if _, err := c.r.ReadValue(); err == nil {
				t.Fatal("a client without a certificate got a reply")
			}
			nc.Close()
		}
		cfg.Certificates = []tls.Certificate{p.other}
		nc, err = tls.Dial("tcp", addr, cfg)
		if err == nil {
			c := newClient(t, nc)
			_ = nc.SetReadDeadline(time.Now().Add(2 * time.Second))
			c.send("PING")
			if _, err := c.r.ReadValue(); err == nil {
				t.Fatal("a client with a certificate of another CA got a reply")
			}
			nc.Close()
		}
		cfg.Certificates = []tls.Certificate{p.client}
		nc, err = tls.Dial("tcp", addr, cfg)
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		wantSimple(t, newClient(t, nc).do("PING"), "PONG")
	})

	t.Run("Should close a client that does not finish the handshake", func(t *testing.T) {
		addr := serve(t, false, Options{HandshakeTimeout: 200 * time.Millisecond})
		c := dial(t, addr)
		_, _ = c.nc.Write([]byte{0x16, 0x03, 0x01})
		if !c.closed() {
			t.Fatal("the connection is open")
		}
	})
}
