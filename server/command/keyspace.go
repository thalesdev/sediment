// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"math/rand/v2"
	"strconv"
	"strings"

	"github.com/thalesdev/sediment/internal/glob"
	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

const (
	defaultScanCount = 10
	maxScanCount     = 10000
	maxScanCursors   = 32
)

var replyBadCursor = resp.Error("ERR invalid cursor")

// keyspaceCommands returns SCAN, KEYS, DBSIZE, RANDOMKEY and TYPE.
func keyspaceCommands() []*Spec {
	return []*Spec{
		{Name: "scan", Arity: -2, Categories: acl.CatRead | acl.CatKeyspace | acl.CatSlow, ListsKeys: true, Handler: scan},
		{Name: "keys", Arity: 2, Categories: acl.CatRead | acl.CatKeyspace | acl.CatSlow | acl.CatDangerous, ListsKeys: true, Handler: keysCmd},
		{Name: "dbsize", Arity: 1, Categories: acl.CatRead | acl.CatKeyspace | acl.CatSlow, Handler: dbsize},
		{Name: "randomkey", Arity: 1, Categories: acl.CatRead | acl.CatKeyspace | acl.CatSlow, ListsKeys: true, Handler: randomkey},
		{Name: "type", Arity: 2, Categories: acl.CatRead | acl.CatKeyspace | acl.CatFast, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: typeCmd},
	}
}

// scanState holds the cursors that a connection can resume. A cursor is a number
// that stands for the last key that a SCAN looked at. The connection keeps the
// newest maxScanCursors of them, so a client cannot make it hold more.
type scanState struct {
	next  uint64
	order []uint64
	last  map[uint64][]byte
}

// remember stores key and returns the cursor for it. A cursor is never 0.
func (s *scanState) remember(key []byte) uint64 {
	if s.last == nil {
		s.last = make(map[uint64][]byte)
	}
	s.next++
	id := s.next
	s.last[id] = bytes.Clone(key)
	s.order = append(s.order, id)
	for len(s.order) > maxScanCursors {
		delete(s.last, s.order[0])
		s.order = s.order[1:]
	}
	return id
}

// lookup returns the last key of a cursor.
func (s *scanState) lookup(id uint64) ([]byte, bool) {
	key, ok := s.last[id]
	return key, ok
}

// entryOf decodes the current entry of an iterator over the KEY records. It
// returns the user key, which is a slice of the key of the iterator, the record,
// and whether the key is alive at the time now, in milliseconds.
func entryOf(it shale.Iterator, now int64) (user []byte, rec record, live bool, err error) {
	if user, err = userKeyOf(it.Key()); err != nil {
		return nil, record{}, false, err
	}
	if rec, err = decodeRecord(it.Value()); err != nil {
		return nil, record{}, false, err
	}
	return user, rec, rec.deadline == 0 || rec.deadline > now, nil
}

// matchAll tells if a pattern matches every key.
func matchAll(pattern []byte) bool {
	return string(pattern) == "*"
}

// scan is SCAN cursor [MATCH pattern] [COUNT count] [TYPE type]. A call looks at
// up to count keys after the one that the cursor names. It opens an iterator,
// reads one page and closes it, so nothing stays held between calls.
func scan(env *Env, c *Client, args [][]byte) resp.Value {
	cursor, err := strconv.ParseUint(string(args[1]), 10, 64)
	if err != nil {
		return replyBadCursor
	}
	count := defaultScanCount
	pattern := []byte("*")
	wantType := ""
	opts := args[2:]
	if len(opts)%2 != 0 {
		return replySyntax
	}
	for i := 0; i < len(opts); i += 2 {
		switch strings.ToUpper(string(opts[i])) {
		case "MATCH":
			pattern = opts[i+1]
		case "COUNT":
			n, ok := parseInt(opts[i+1])
			if !ok {
				return replyNotInt
			}
			if n < 1 {
				return replySyntax
			}
			if n > maxScanCount {
				count = maxScanCount
			} else {
				count = int(n)
			}
		case "TYPE":
			wantType = strings.ToLower(string(opts[i+1]))
		default:
			return replySyntax
		}
	}
	var resume []byte
	if cursor != 0 {
		var ok bool
		if resume, ok = c.scan.lookup(cursor); !ok {
			return replyBadCursor
		}
	}
	it, err := env.Store.NewIterator(classBounds(classKey))
	if err != nil {
		return env.storageError("scan", err)
	}
	defer it.Close()
	if resume == nil {
		it.SeekToFirst()
	} else {
		it.Seek(resume)
		if it.Valid() && bytes.Equal(it.Key(), resume) {
			it.Next()
		}
	}
	now := env.nowMillis()
	var page []resp.Value
	var last []byte
	total, examined := 0, 0
	for it.Valid() && examined < count {
		user, rec, live, err := entryOf(it, now)
		if err != nil {
			return env.storageError("scan", err)
		}
		if live && (wantType == "" || rec.typ.name() == wantType) && (matchAll(pattern) || glob.Match(pattern, user)) {
			if env.MaxReply > 0 && total+len(user) > env.MaxReply && len(page) > 0 {
				break
			}
			total += len(user)
			page = append(page, resp.Bulk(bytes.Clone(user)))
		}
		last = append(last[:0], it.Key()...)
		examined++
		it.Next()
	}
	if err := it.Err(); err != nil {
		return env.storageError("scan", err)
	}
	next := "0"
	if it.Valid() && last != nil {
		next = strconv.FormatUint(c.scan.remember(last), 10)
	}
	return scanReply(next, page)
}

// scanReply returns the reply of SCAN.
func scanReply(cursor string, page []resp.Value) resp.Value {
	return resp.Array(resp.BulkString(cursor), resp.Array(page...))
}

// keysCmd is KEYS pattern. It looks at every key, so it is slow on a large
// database.
func keysCmd(env *Env, _ *Client, args [][]byte) resp.Value {
	pattern := args[1]
	it, err := env.Store.NewIterator(classBounds(classKey))
	if err != nil {
		return env.storageError("keys", err)
	}
	defer it.Close()
	now := env.nowMillis()
	var out []resp.Value
	total := 0
	for it.SeekToFirst(); it.Valid(); it.Next() {
		user, _, live, err := entryOf(it, now)
		if err != nil {
			return env.storageError("keys", err)
		}
		if !live || !(matchAll(pattern) || glob.Match(pattern, user)) {
			continue
		}
		total += len(user)
		if env.MaxReply > 0 && total > env.MaxReply {
			return replyReplyBig
		}
		out = append(out, resp.Bulk(bytes.Clone(user)))
	}
	if err := it.Err(); err != nil {
		return env.storageError("keys", err)
	}
	return resp.Array(out...)
}

// dbsize is DBSIZE. It counts the keys that are alive, so it is slow on a large
// database.
func dbsize(env *Env, _ *Client, _ [][]byte) resp.Value {
	it, err := env.Store.NewIterator(classBounds(classKey))
	if err != nil {
		return env.storageError("dbsize", err)
	}
	defer it.Close()
	now := env.nowMillis()
	var n int64
	for it.SeekToFirst(); it.Valid(); it.Next() {
		_, _, live, err := entryOf(it, now)
		if err != nil {
			return env.storageError("dbsize", err)
		}
		if live {
			n++
		}
	}
	if err := it.Err(); err != nil {
		return env.storageError("dbsize", err)
	}
	return resp.Integer(n)
}

// randomkey is RANDOMKEY. It picks a random slot and takes the first live key
// after it, and it wraps around once. A slot that follows a large gap is more
// likely, so the choice is not uniform.
func randomkey(env *Env, _ *Client, _ [][]byte) resp.Value {
	point := engineKey(classKey, uint16(rand.IntN(slotCount)))
	now := env.nowMillis()
	end := []byte{classKey + 1}
	for _, opts := range []*shale.IterOptions{{Lower: point, Upper: end}, {Lower: []byte{classKey}, Upper: point}} {
		key, ok, err := firstLive(env, opts, now)
		if err != nil {
			return env.storageError("randomkey", err)
		}
		if ok {
			return resp.Bulk(key)
		}
	}
	return resp.Null()
}

// firstLive returns a copy of the user key of the first key in the range that is
// alive.
func firstLive(env *Env, opts *shale.IterOptions, now int64) ([]byte, bool, error) {
	it, err := env.Store.NewIterator(opts)
	if err != nil {
		return nil, false, err
	}
	defer it.Close()
	for it.SeekToFirst(); it.Valid(); it.Next() {
		user, _, live, err := entryOf(it, now)
		if err != nil {
			return nil, false, err
		}
		if live {
			return bytes.Clone(user), true, nil
		}
	}
	return nil, false, it.Err()
}

// typeCmd is TYPE key.
func typeCmd(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	rec, err := env.loadRecord(args[1])
	if isNotFound(err) {
		return resp.Simple("none")
	}
	if err != nil {
		return env.storageError("type", err)
	}
	return resp.Simple(rec.typ.name())
}
