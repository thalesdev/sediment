// SPDX-License-Identifier: MIT

package acl

import (
	"os"
	"strings"
	"testing"
)

func keys(k ...string) [][]byte {
	out := make([][]byte, len(k))
	for i, s := range k {
		out[i] = []byte(s)
	}
	return out
}

func userOf(t *testing.T, line string) *User {
	t.Helper()
	users, _, err := Parse(strings.NewReader(line+"\n"), ParseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return users["u"]
}

func TestKeyPatterns(t *testing.T) {
	t.Run("Should let a user touch only the keys that a pattern matches", func(t *testing.T) {
		u := userOf(t, "user u on nopass ~app:* +@all")
		if !u.allowed("get", "", CatRead, keys("app:1")) {
			t.Fatal("app:1 refused")
		}
		if u.allowed("get", "", CatRead, keys("other")) {
			t.Fatal("other allowed")
		}
		if u.allowed("mget", "", CatRead, keys("app:1", "other")) {
			t.Fatal("a command is allowed if one of its keys is not")
		}
	})
	t.Run("Should split read and write with %R~ and %W~", func(t *testing.T) {
		u := userOf(t, "user u on nopass %R~in:* %W~out:* +@all")
		if !u.allowed("get", "", CatRead, keys("in:1")) || u.allowed("set", "", CatWrite, keys("in:1")) {
			t.Fatal("wrong decision for in:*")
		}
		if !u.allowed("set", "", CatWrite, keys("out:1")) || u.allowed("get", "", CatRead, keys("out:1")) {
			t.Fatal("wrong decision for out:*")
		}
	})
	t.Run("Should give a user with no key pattern no key at all", func(t *testing.T) {
		u := userOf(t, "user u on nopass +@all")
		if u.allowed("get", "", CatRead, keys("a")) {
			t.Fatal("a user with no pattern read a key")
		}
		if !u.allowed("ping", "", CatFast, nil) {
			t.Fatal("a command with no key was refused")
		}
	})
	t.Run("Should reset the patterns with resetkeys and accept allkeys", func(t *testing.T) {
		u := userOf(t, "user u on nopass ~a:* resetkeys allkeys +@all")
		if !u.allowed("get", "", CatRead, keys("zzz")) {
			t.Fatal("allkeys refused a key")
		}
		u = userOf(t, "user u on nopass allkeys resetkeys +@all")
		if u.allowed("get", "", CatRead, keys("zzz")) {
			t.Fatal("resetkeys kept a pattern")
		}
	})
	t.Run("Should say who can list the keys", func(t *testing.T) {
		if userOf(t, "user u on nopass ~a:* +@all").allKeys(false) {
			t.Fatal("a pattern user can list every key")
		}
		if !userOf(t, "user u on nopass %R~* +@all").allKeys(false) {
			t.Fatal("a read all user cannot list keys")
		}
		if userOf(t, "user u on nopass %W~* +@all").allKeys(false) {
			t.Fatal("a write only user can list keys")
		}
		if !userOf(t, "user u on nopass %W~* +@all").allKeys(true) || userOf(t, "user u on nopass %R~* +@all").allKeys(true) {
			t.Fatal("wrong answer for the right to write every key")
		}
	})
}

func TestSubcommandsAndSelectors(t *testing.T) {
	t.Run("Should allow one subcommand and no other", func(t *testing.T) {
		u := userOf(t, "user u on nopass +config|get")
		if !u.allowed("config", "get", CatAdmin, nil) || u.allowed("config", "set", CatAdmin, nil) || u.allowed("config", "", CatAdmin, nil) {
			t.Fatal("wrong decisions")
		}
	})
	t.Run("Should deny one subcommand of an allowed command", func(t *testing.T) {
		u := userOf(t, "user u on nopass +client -client|kill")
		if !u.allowed("client", "list", CatSlow, nil) || u.allowed("client", "kill", CatSlow, nil) {
			t.Fatal("wrong decisions")
		}
	})
	t.Run("Should allow what a selector allows, next to the rules of the line", func(t *testing.T) {
		u := userOf(t, "user u on nopass ~a:* +get (~b:* +set) (%R~c:* +get)")
		if !u.allowed("get", "", CatRead, keys("a:1")) {
			t.Fatal("line refused")
		}
		if !u.allowed("set", "", CatWrite, keys("b:1")) || u.allowed("set", "", CatWrite, keys("a:1")) {
			t.Fatal("selector one is wrong")
		}
		if !u.allowed("get", "", CatRead, keys("c:1")) || u.allowed("get", "", CatRead, keys("d:1")) {
			t.Fatal("selector two is wrong")
		}
	})
	t.Run("Should read a selector that is one token, and refuse one that is not closed", func(t *testing.T) {
		u := userOf(t, "user u on nopass (~b:*) ")
		if len(u.selectors) != 1 {
			t.Fatalf("got %d selectors", len(u.selectors))
		}
		if _, _, err := Parse(strings.NewReader("user u on nopass (~b:* +get\n"), ParseOptions{}); err == nil {
			t.Fatal("an open selector was accepted")
		}
	})
	t.Run("Should write the user back so that parsing it gives the same user", func(t *testing.T) {
		line := "user u on nopass %R~in:* ~app:* +get -@dangerous +config|get (~b:* +set)"
		again := userOf(t, userOf(t, line).describe())
		if got, want := again.describe(), userOf(t, line).describe(); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
		if !strings.Contains(again.describe(), "(~b:* +set)") || !strings.Contains(again.describe(), "%R~in:*") {
			t.Fatalf("got %q", again.describe())
		}
	})
	t.Run("Should check a known subcommand when the server tells which exist", func(t *testing.T) {
		o := ParseOptions{KnownSub: func(c, s string) bool { return c == "config" && s == "get" }}
		if _, _, err := Parse(strings.NewReader("user u on nopass +config|get\n"), o); err != nil {
			t.Fatal(err)
		}
		if _, _, err := Parse(strings.NewReader("user u on nopass +config|nope\n"), o); err == nil {
			t.Fatal("an unknown subcommand was accepted")
		}
	})
}

func TestManage(t *testing.T) {
	file := "user default off\nuser a on nopass ~* +@all\n"
	t.Run("Should change a user whole or not at all", func(t *testing.T) {
		path := writeFile(t, file, 0o600)
		r, _, err := Load(path, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.SetUser("a", []string{"-@all", "+get", "wat"}); err == nil {
			t.Fatal("a bad rule was accepted")
		}
		if !r.Allowed("a", "set", "", CatWrite, keys("k")) {
			t.Fatal("a failed change touched the user")
		}
		if _, err := r.SetUser("a", []string{"-@all", "+get"}); err != nil {
			t.Fatal(err)
		}
		if r.Allowed("a", "set", "", CatWrite, keys("k")) || !r.Allowed("a", "get", "", CatRead, keys("k")) {
			t.Fatal("the change did not apply")
		}
	})
	t.Run("Should make a user, hash a plain password and warn", func(t *testing.T) {
		r, _, _ := Load(writeFile(t, file, 0o600), ParseOptions{})
		warnings, err := r.SetUser("b", []string{"on", ">secret", "~*", "+@all"})
		if err != nil || len(warnings) != 1 {
			t.Fatalf("got %v, %v", warnings, err)
		}
		if !r.Authenticate("b", []byte("secret")) || r.Authenticate("b", []byte("nope")) {
			t.Fatal("wrong sign in")
		}
		for _, l := range r.List() {
			if strings.Contains(l, "secret") {
				t.Fatalf("a password is in %q", l)
			}
		}
	})
	t.Run("Should refuse a bad name and remove a user but never default", func(t *testing.T) {
		r, _, _ := Load(writeFile(t, file, 0o600), ParseOptions{})
		if _, err := r.SetUser("a b", nil); err == nil {
			t.Fatal("a name with a space was accepted")
		}
		if _, err := r.DelUser([]string{"default"}); err == nil {
			t.Fatal("default was removed")
		}
		if n, err := r.DelUser([]string{"a", "nobody"}); err != nil || n != 1 {
			t.Fatalf("got %d, %v", n, err)
		}
		if r.Active("a") {
			t.Fatal("a is still there")
		}
	})
	t.Run("Should save the users to the file with mode 0600 and load them back", func(t *testing.T) {
		path := writeFile(t, file, 0o600)
		r, _, _ := Load(path, ParseOptions{})
		if _, err := r.SetUser("c", []string{"on", "nopass", "%R~in:*", "+get"}); err != nil {
			t.Fatal(err)
		}
		if err := r.Save(); err != nil {
			t.Fatal(err)
		}
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0o600 {
			t.Fatalf("got %v, %v", st, err)
		}
		again, _, err := Load(path, ParseOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if !again.Allowed("c", "get", "", CatRead, keys("in:1")) || again.Allowed("c", "get", "", CatRead, keys("x")) {
			t.Fatal("the saved user is not the same")
		}
	})
	t.Run("Should show a user with Info", func(t *testing.T) {
		r, _, _ := Load(writeFile(t, "user default off\nuser a on nopass %R~in:* +get (~b:* +set)\n", 0o600), ParseOptions{})
		info, ok := r.Info("a")
		if !ok || info.Keys != "%R~in:*" || info.Commands != "+get" || len(info.Selectors) != 1 || info.Selectors[0].Keys != "~b:*" {
			t.Fatalf("got %+v, %v", info, ok)
		}
		if _, ok := r.Info("zz"); ok {
			t.Fatal("a user that does not exist")
		}
	})
}

func TestCertOnly(t *testing.T) {
	t.Run("Should not let a certonly user sign in with a password", func(t *testing.T) {
		r, _, _ := Load(writeFile(t, "user default off\nuser alice on certonly ~* +@all\n", 0o600), ParseOptions{})
		if r.Authenticate("alice", []byte("")) || r.Authenticate("alice", []byte("x")) {
			t.Fatal("a certonly user signed in with a password")
		}
		if !r.CertUser("alice") || r.CertUser("default") || r.CertUser("nobody") {
			t.Fatal("wrong CertUser")
		}
	})
	t.Run("Should drop certonly when a password is set", func(t *testing.T) {
		r, _, _ := Load(writeFile(t, "user default off\nuser alice on certonly ~* +@all\n", 0o600), ParseOptions{})
		if _, err := r.SetUser("alice", []string{">pw"}); err != nil {
			t.Fatal(err)
		}
		if r.CertUser("alice") || !r.Authenticate("alice", []byte("pw")) {
			t.Fatal("certonly stayed")
		}
	})
}

func TestDenyLog(t *testing.T) {
	t.Run("Should keep the newest entries and reset", func(t *testing.T) {
		r, _, _ := Load(writeFile(t, "user default off\n", 0o600), ParseOptions{})
		for range maxLogEntries + 10 {
			r.Record("command", "toplevel", "get", "u", "id=1")
		}
		if got := len(r.LogEntries(-1)); got != maxLogEntries {
			t.Fatalf("got %d entries", got)
		}
		if first := r.LogEntries(1)[0]; first.ID != maxLogEntries+9 {
			t.Fatalf("the newest entry has id %d", first.ID)
		}
		r.LogReset()
		if len(r.LogEntries(-1)) != 0 {
			t.Fatal("the log was not reset")
		}
	})
}

func FuzzApplyTokens(f *testing.F) {
	f.Add("~a:* %R~b +get (~c +set) resetkeys")
	f.Add("(")
	f.Add("%RW~x +config|get -@all")
	f.Fuzz(func(t *testing.T, text string) {
		u := &User{name: "u"}
		_, err := u.applyTokens(strings.Fields(text), ParseOptions{})
		if err != nil {
			return
		}
		again := &User{name: "u"}
		if _, err := again.applyTokens(strings.Fields(strings.TrimPrefix(u.describe(), "user u")), ParseOptions{}); err != nil {
			t.Fatalf("describe of %q does not parse: %v", text, err)
		}
	})
}
