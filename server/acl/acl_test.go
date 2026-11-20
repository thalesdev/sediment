// SPDX-License-Identifier: MIT

package acl

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func parse(t *testing.T, text string) (map[string]*User, []string, error) {
	t.Helper()
	return Parse(strings.NewReader(text), ParseOptions{KnownCommand: func(n string) bool {
		return n == "get" || n == "set" || n == "del" || n == "config" || n == "info"
	}})
}

func writeFile(t *testing.T, text string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.acl")
	if err := os.WriteFile(path, []byte(text), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPassword(t *testing.T) {
	t.Run("Should hash with a random salt and verify", func(t *testing.T) {
		a, err := HashPassword([]byte("secret"))
		if err != nil {
			t.Fatal(err)
		}
		b, _ := HashPassword([]byte("secret"))
		if a == b {
			t.Fatal("two hashes of the same password are equal")
		}
		p, err := parsePassword(a)
		if err != nil {
			t.Fatal(err)
		}
		if !p.verify([]byte("secret")) || p.verify([]byte("Secret")) || p.verify(nil) {
			t.Fatal("verify is wrong")
		}
		if p.String() != a {
			t.Fatalf("got %q, want %q", p.String(), a)
		}
	})

	t.Run("Should refuse a Redis SHA-256 hash and say how to make a good one", func(t *testing.T) {
		sum := sha256.Sum256([]byte("old"))
		_, err := parsePassword("#" + hex.EncodeToString(sum[:]))
		if err == nil || !strings.Contains(err.Error(), "sedimentd hash-password") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should refuse malformed hashes", func(t *testing.T) {
		good, _ := HashPassword([]byte("x"))
		for _, bad := range []string{
			"#abc", "#" + strings.Repeat("z", 64), "plain", "$pbkdf2-sha256$", "$pbkdf2-sha256$i=1$a$b",
			"$pbkdf2-sha256$i=0$" + strings.Split(good, "$")[3] + "$" + strings.Split(good, "$")[4],
			"$pbkdf2-sha256$i=99999999999$" + strings.Split(good, "$")[3] + "$" + strings.Split(good, "$")[4],
			"$pbkdf2-sha256$i=x$a$b", "$pbkdf2-sha256$i=1000$AAAA$AAAA",
		} {
			if _, err := parsePassword(bad); err == nil {
				t.Fatalf("%q was accepted", bad)
			}
		}
	})
}

func TestParse(t *testing.T) {
	t.Run("Should read users and rules", func(t *testing.T) {
		users, warnings, err := parse(t, "# comment\n\nuser app on >pw ~* +@read +set -@dangerous\nuser off1 off nopass +@all\n")
		if err != nil || len(warnings) != 1 {
			t.Fatalf("err %v, warnings %v", err, warnings)
		}
		if !users["app"].enabled || users["off1"].enabled || users["default"].enabled {
			t.Fatal("wrong on and off state")
		}
		if !strings.Contains(warnings[0], "plain password") {
			t.Fatalf("got %q", warnings[0])
		}
	})

	t.Run("Should let the last matching rule win", func(t *testing.T) {
		users, _, err := parse(t, "user u on nopass +@all -@dangerous +config\n")
		if err != nil {
			t.Fatal(err)
		}
		u := users["u"]
		if !u.allowed("get", "", CatRead|CatFast, nil) || u.allowed("info", "", CatSlow|CatDangerous, nil) || !u.allowed("config", "", CatAdmin|CatDangerous, nil) {
			t.Fatal("wrong decisions")
		}
	})

	t.Run("Should let a user with no rule run nothing", func(t *testing.T) {
		users, _, _ := parse(t, "user u on nopass\n")
		if users["u"].allowed("get", "", CatRead, nil) {
			t.Fatal("a user with no rule can run a command")
		}
	})

	t.Run("Should not let an off user run anything", func(t *testing.T) {
		users, _, _ := parse(t, "user u off nopass +@all\n")
		if users["u"].allowed("get", "", CatRead, nil) {
			t.Fatal("an off user can run a command")
		}
	})

	t.Run("Should fail closed on what it does not enforce", func(t *testing.T) {
		for _, line := range []string{
			"user u on nopass %X~a +@all", "user u on nopass ~", "user u on nopass %R~", "user u on nopass (~a +get",
			"user u on nopass (>pw)", "user u on nopass +get|x|y", "user u on nopass -nosuchcommand", "user u on nopass +@nosuchcategory",
			"user u on nopass wat", "user u on #zz", "user", "hello", "user u on nopass +@all\nuser u off",
		} {
			if _, _, err := parse(t, line+"\n"); err == nil {
				t.Fatalf("%q was accepted", line)
			}
		}
	})

	t.Run("Should accept ~* and channel rules", func(t *testing.T) {
		if _, _, err := parse(t, "user u on nopass allkeys ~* &* allchannels +@all\n"); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("Should refuse a user with a SHA-256 hash", func(t *testing.T) {
		sum := sha256.Sum256([]byte("old"))
		_, _, err := parse(t, "user u on #"+hex.EncodeToString(sum[:])+" +@all\n")
		if err == nil || !strings.Contains(err.Error(), "line 1") || !strings.Contains(err.Error(), "sedimentd hash-password") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should refuse a line that is too long", func(t *testing.T) {
		if _, _, err := parse(t, "user u on "+strings.Repeat("a", maxLine+10)+"\n"); err == nil {
			t.Fatal("a long line was accepted")
		}
	})
}

func TestRegistry(t *testing.T) {
	hash, _ := HashPassword([]byte("s3cret"))

	t.Run("Should authenticate by user and password", func(t *testing.T) {
		path := writeFile(t, "user app on "+hash+" +@all\nuser dead off "+hash+" +@all\nuser open on nopass +@read\n", 0o600)
		r, _, err := Load(path, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !r.Authenticate("app", []byte("s3cret")) || r.Authenticate("app", []byte("wrong")) {
			t.Fatal("wrong decision for app")
		}
		if r.Authenticate("dead", []byte("s3cret")) || r.Authenticate("nobody", []byte("s3cret")) || r.Authenticate("default", []byte("s3cret")) {
			t.Fatal("signed in as a user that cannot")
		}
		if !r.Authenticate("open", []byte("anything")) {
			t.Fatal("nopass user was refused")
		}
		if !r.RequireAuth() {
			t.Fatal("the default user is off, a sign in is required")
		}
	})

	t.Run("Should not require a sign in when default is on with no password", func(t *testing.T) {
		path := writeFile(t, "user default on nopass +@all\n", 0o600)
		r, _, err := Load(path, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if r.RequireAuth() {
			t.Fatal("a sign in is required")
		}
	})

	t.Run("Should refuse a file that others can read", func(t *testing.T) {
		path := writeFile(t, "user default on nopass +@all\n", 0o644)
		if _, _, err := Load(path, ParseOptions{}); err == nil || !strings.Contains(err.Error(), "chmod 600") {
			t.Fatalf("got %v", err)
		}
	})

	t.Run("Should keep the old users when a reload fails", func(t *testing.T) {
		path := writeFile(t, "user app on nopass +@all\n", 0o600)
		r, _, err := Load(path, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("user app on nopass wat +@all\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reload(); err == nil {
			t.Fatal("the bad file was loaded")
		}
		if !r.Authenticate("app", nil) {
			t.Fatal("the users changed")
		}
		if err := os.WriteFile(path, []byte("user other on nopass +@all\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Reload(); err != nil {
			t.Fatal(err)
		}
		if r.Authenticate("app", nil) || !r.Authenticate("other", nil) {
			t.Fatal("the users did not change")
		}
	})

	t.Run("Should list users without a plain password", func(t *testing.T) {
		path := writeFile(t, "user app on >plain +@read\n", 0o600)
		r, _, _ := Load(path, ParseOptions{})
		list := strings.Join(r.List(), "\n")
		if strings.Contains(list, "plain") || !strings.Contains(list, "$pbkdf2-sha256$") || !strings.Contains(list, "user app on") {
			t.Fatalf("got %q", list)
		}
		if got := r.Users(); len(got) != 2 || got[0] != "app" || got[1] != "default" {
			t.Fatalf("got %v", got)
		}
	})

	t.Run("Should have no reload without a file", func(t *testing.T) {
		if _, err := (&Registry{}).Reload(); err == nil {
			t.Fatal("reload worked")
		}
	})
}

func TestCategory(t *testing.T) {
	t.Run("Should parse names in any case and list them", func(t *testing.T) {
		if c, ok := ParseCategory("READ"); !ok || c != CatRead {
			t.Fatal("READ")
		}
		if _, ok := ParseCategory("nope"); ok {
			t.Fatal("nope")
		}
		if got := (CatRead | CatFast).Names(); len(got) != 2 || got[0] != "@read" || got[1] != "@fast" {
			t.Fatalf("got %v", got)
		}
	})
}

func FuzzParse(f *testing.F) {
	f.Add("user a on nopass +@all\n")
	f.Add("user a off >pw ~* -@dangerous +get\n")
	f.Add("# c\n\nuser default on nopass\n")
	f.Add("user a on #" + strings.Repeat("a", 64) + "\n")
	f.Fuzz(func(t *testing.T, text string) {
		users, _, err := Parse(strings.NewReader(text), ParseOptions{})
		if err != nil {
			return
		}
		if _, ok := users["default"]; !ok {
			t.Fatal("no default user")
		}
		for _, u := range users {
			_ = u.describe()
		}
	})
}
