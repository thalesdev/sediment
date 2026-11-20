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

	t.Run("Should read the log format and refuse a format that is not text or json", func(t *testing.T) {
		c, err := parse(t, "log-format json\n")
		if err != nil || c.LogFormat != "json" {
			t.Fatalf("got %+v, %v", c, err)
		}
		if c, _ = parse(t, ""); c.LogFormat != "text" {
			t.Fatalf("the default format is %q", c.LogFormat)
		}
		if _, err := parse(t, "log-format xml\n"); err == nil {
			t.Fatal("xml was accepted")
		}
	})

	t.Run("Should read the settings of the slow log and of the sign in limit", func(t *testing.T) {
		c, err := parse(t, "slowlog-log-slower-than 500\nslowlog-max-len 10\nauth-fail-limit 0\nauth-fail-window 30\n")
		if err != nil || c.SlowLogSlowerThan != 500 || c.SlowLogMaxLen != 10 || c.AuthFailLimit != 0 || c.AuthFailWindow != 30*time.Second {
			t.Fatalf("got %+v, %v", c, err)
		}
		if c, _ = parse(t, "slowlog-log-slower-than -1\n"); c.SlowLogSlowerThan != -1 {
			t.Fatalf("got %d", c.SlowLogSlowerThan)
		}
		for _, text := range []string{"slowlog-log-slower-than -2\n", "slowlog-max-len 0\n", "auth-fail-limit -1\n", "auth-fail-window 0\n"} {
			if _, err := parse(t, text); err == nil {
				t.Fatalf("%q was accepted", text)
			}
		}
	})

	t.Run("Should need mTLS and a users file for tls-auth-clients-user", func(t *testing.T) {
		c, err := parse(t, "tls-cert-file a\ntls-key-file b\ntls-ca-cert-file c\naclfile d\ntls-auth-clients-user yes\n")
		if err != nil || !c.TLSAuthClientsUser || c.Validate() != nil {
			t.Fatalf("got %+v, %v", c, err)
		}
		c, _ = parse(t, "tls-cert-file a\ntls-key-file b\ntls-auth-clients-user yes\n")
		if c.Validate() == nil {
			t.Fatal("accepted with no CA and no users file")
		}
	})

	t.Run("Should read the settings of the metrics", func(t *testing.T) {
		c, err := parse(t, "metrics-addr 127.0.0.1:9121\notlp-metrics yes\notlp-interval 15\n")
		if err != nil || c.MetricsAddr != "127.0.0.1:9121" || !c.OTLPMetrics || c.OTLPInterval != 15*time.Second || c.Validate() != nil {
			t.Fatalf("got %+v, %v", c, err)
		}
		if _, err := parse(t, "metrics-addr nonsense\n"); err == nil {
			t.Fatal("an address with no port was accepted")
		}
	})

	t.Run("Should need TLS and a sign in for a metrics address that other machines reach", func(t *testing.T) {
		c, _ := parse(t, "metrics-addr 0.0.0.0:9121\n")
		if c.Validate() == nil {
			t.Fatal("accepted with no TLS and no sign in")
		}
		c, _ = parse(t, "metrics-addr 0.0.0.0:9121\ntls-cert-file a\ntls-key-file b\n")
		if c.Validate() == nil {
			t.Fatal("accepted with no sign in")
		}
		c, _ = parse(t, "metrics-addr 0.0.0.0:9121\ntls-cert-file a\ntls-key-file b\nmetrics-user u\nmetrics-password-hash h\n")
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		for _, addr := range []string{"127.0.0.1:1", "[::1]:1", "localhost:1"} {
			if c, _ = parse(t, "metrics-addr "+addr+"\n"); c.MetricsExposed() || c.Validate() != nil {
				t.Fatalf("%s is exposed", addr)
			}
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

func TestDefaults(t *testing.T) {
	t.Run("Should use a memtable of 64 MiB", func(t *testing.T) {
		if got := Defaults().MemtableSize; got != 64<<20 {
			t.Fatalf("got %d", got)
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
