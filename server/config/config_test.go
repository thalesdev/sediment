// SPDX-License-Identifier: MIT

package config

import (
	"strings"
	"testing"
	"time"
)

func parse(t *testing.T, text string) (Config, error) {
	t.Helper()
	c := Defaults()
	err := Parse(strings.NewReader(text), &c)
	return c, err
}

func TestParse(t *testing.T) {
	t.Run("Should read keys, comments and blank lines", func(t *testing.T) {
		c, err := parse(t, "# a comment\n\nbind 127.0.0.1 ::1\nport 7000\nDIR /var/lib/sediment\nsync yes\ntimeout 0\nread-timeout 5\nproto-max-bulk-len 1048576\nloglevel debug\n")
		if err != nil {
			t.Fatal(err)
		}
		if len(c.Bind) != 2 || c.Port != 7000 || c.Dir != "/var/lib/sediment" || !c.Sync || c.Timeout >= 0 || c.ReadTimeout != 5*time.Second || c.MaxBulk != 1<<20 || c.LogLevel != "debug" {
			t.Fatalf("got %+v", c)
		}
	})

	t.Run("Should refuse unknown keys and bad values", func(t *testing.T) {
		for _, text := range []string{
			"tls-cert-fil /x\n", "port 0\n", "port 70000\n", "port abc\n", "sync maybe\n", "timeout -1\n", "read-timeout 0\n",
			"loglevel loud\n", "port\n", "port 1 2\n", "bind\n", "maxclients 0\n",
		} {
			if _, err := parse(t, text); err == nil {
				t.Fatalf("%q was accepted", text)
			}
		}
	})
}

func TestValidate(t *testing.T) {
	t.Run("Should need the certificate and the key together", func(t *testing.T) {
		c := Defaults()
		c.TLSCert = "a"
		if c.Validate() == nil {
			t.Fatal("a certificate without a key was accepted")
		}
		c.TLSKey = "b"
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		c = Defaults()
		c.TLSCA = "ca"
		if c.Validate() == nil {
			t.Fatal("a CA without a certificate was accepted")
		}
	})
}

func TestExposure(t *testing.T) {
	cases := []struct {
		bind    []string
		exposed bool
	}{
		{[]string{"127.0.0.1"}, false},
		{[]string{"127.1.2.3", "::1", "localhost", "[::1]"}, false},
		{[]string{"0.0.0.0"}, true},
		{[]string{"::"}, true},
		{[]string{"10.0.0.5"}, true},
		{[]string{"127.0.0.1", "192.168.1.2"}, true},
		{[]string{"db.internal"}, true},
	}
	t.Run("Should tell loopback from the rest", func(t *testing.T) {
		for _, c := range cases {
			cfg := Config{Bind: c.bind}
			if cfg.Exposed() != c.exposed {
				t.Fatalf("%v: got %v", c.bind, cfg.Exposed())
			}
		}
	})

	t.Run("Should refuse an exposed server with no TLS and no sign in", func(t *testing.T) {
		cfg := Config{Bind: []string{"0.0.0.0"}}
		if cfg.CheckExposure(false) == nil {
			t.Fatal("it was accepted")
		}
		if err := cfg.CheckExposure(true); err != nil {
			t.Fatal(err)
		}
		cfg.TLSCert, cfg.TLSKey = "a", "b"
		if err := cfg.CheckExposure(false); err != nil {
			t.Fatal(err)
		}
		cfg = Config{Bind: []string{"0.0.0.0"}, InsecureBind: true}
		if err := cfg.CheckExposure(false); err != nil {
			t.Fatal(err)
		}
		if err := (Config{Bind: []string{"127.0.0.1"}}).CheckExposure(false); err != nil {
			t.Fatal(err)
		}
	})
}

func TestSettings(t *testing.T) {
	t.Run("Should not show files or secrets", func(t *testing.T) {
		c := Defaults()
		c.TLSCert, c.TLSKey, c.ACLFile, c.Dir = "/etc/cert.pem", "/etc/key.pem", "/etc/users.acl", "/secret/dir"
		for k, v := range c.Settings() {
			if strings.Contains(v, "/") {
				t.Fatalf("%s=%s", k, v)
			}
		}
	})
}
