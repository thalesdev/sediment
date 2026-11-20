// SPDX-License-Identifier: MIT

package command

import (
	"bytes"
	"math"
	"strconv"
	"strings"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/internal/xxh3"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/acl"
)

var (
	replyBadBitOffset = resp.Error("ERR bit offset is not an integer or out of range")
	replyBadBit       = resp.Error("ERR bit is not an integer or out of range")
	replyBadNumKeys   = resp.Error("ERR invalid numkeys value")
	replyBadPairs     = resp.Error("ERR wrong number of key-value pairs")
	replyBadCondition = resp.Error("ERR Invalid condition, use IFEQ, IFNE, IFDEQ or IFDNE")
	replyDelexType    = resp.Error("ERR DELEX with a condition needs a key that holds a string")
	replyLCSType      = resp.Error("ERR The specified keys must contain string values")
	replyLCSBoth      = resp.Error("ERR If you want both the length and indexes, please just use IDX.")
	replyBadDigest    = resp.Error("ERR The digest must be exactly 16 hexadecimal characters")
	replyLCSMemory    = resp.Error("ERR Insufficient memory, transient memory for LCS exceeds the limit")
)

// maxLCSCells is the largest table that LCS builds, in cells of 4 bytes.
const maxLCSCells = 1 << 24

// maxBitOffset is the largest offset of SETBIT and GETBIT, which is 2^32 - 1.
const maxBitOffset = 1<<32 - 1

// extraStringCommands returns DIGEST, DELEX, MSETEX, SETBIT, GETBIT, LCS and SUBSTR.
func extraStringCommands() []*Spec {
	str := acl.CatString
	return []*Spec{
		{Name: "digest", Arity: 2, Categories: acl.CatRead | str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: digest},
		{Name: "delex", Arity: -2, Categories: acl.CatWrite | str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: delex},
		{Name: "msetex", Arity: -4, Categories: acl.CatWrite | str | acl.CatSlow, KeyFunc: msetexKeys, Handler: msetex},
		{Name: "setbit", Arity: 4, Categories: acl.CatWrite | str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: setbit},
		{Name: "getbit", Arity: 3, Categories: acl.CatRead | str | acl.CatFast, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: getbit},
		{Name: "lcs", Arity: -3, Categories: acl.CatRead | str | acl.CatSlow, FirstKey: 1, LastKey: 2, KeyStep: 1, Handler: lcs},
		{Name: "substr", Arity: 4, Categories: acl.CatRead | str | acl.CatSlow, FirstKey: 1, LastKey: 1, KeyStep: 1, Handler: getrange},
	}
}

// digestHex returns the XXH3 hash of a value as 16 lower case hex digits, which is
// the form that DIGEST, IFDEQ and IFDNE use.
func digestHex(b []byte) string {
	h := strconv.FormatUint(xxh3.Sum64(b), 16)
	return strings.Repeat("0", 16-len(h)) + h
}

// digest is DIGEST key. It replies with the digest of a string, or null.
func digest(env *Env, _ *Client, args [][]byte) resp.Value {
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	value, _, err := env.load(args[1])
	if isNotFound(err) {
		return resp.Null()
	}
	if err != nil {
		return env.storageError("digest", err)
	}
	return resp.BulkString(digestHex(value))
}

// condition is an IFEQ, IFNE, IFDEQ or IFDNE of SET and DELEX.
type condition struct {
	kind string
	arg  []byte
}

// parseCondition returns the condition of an option name, or false if the name is
// not one.
func parseCondition(name string, arg []byte) (condition, bool) {
	switch strings.ToUpper(name) {
	case "IFEQ", "IFNE", "IFDEQ", "IFDNE":
		return condition{kind: strings.ToUpper(name), arg: arg}, true
	}
	return condition{}, false
}

// validDigest reports whether an IFDEQ or IFDNE condition has a digest of exactly 16
// hexadecimal digits. The other conditions always do.
func (c condition) validDigest() bool {
	if c.kind != "IFDEQ" && c.kind != "IFDNE" {
		return true
	}
	if len(c.arg) != 16 {
		return false
	}
	for _, d := range c.arg {
		if !(d >= '0' && d <= '9' || d >= 'a' && d <= 'f' || d >= 'A' && d <= 'F') {
			return false
		}
	}
	return true
}

// holds reports whether the condition is true for a key. found tells if the key
// exists, and value is its string. IFEQ and IFDEQ need the key, and IFNE and IFDNE
// hold for a key that does not exist.
func (c condition) holds(found bool, value []byte) bool {
	switch c.kind {
	case "IFEQ":
		return found && bytes.Equal(value, c.arg)
	case "IFNE":
		return !found || !bytes.Equal(value, c.arg)
	case "IFDEQ":
		return found && digestHex(value) == strings.ToLower(string(c.arg))
	}
	return !found || digestHex(value) != strings.ToLower(string(c.arg))
}

// delex is DELEX key [IFEQ value | IFNE value | IFDEQ digest | IFDNE digest]. With no
// condition it is DEL. With one, it deletes the key only if its string passes the
// condition, and it replies 1 or 0.
func delex(env *Env, cl *Client, args [][]byte) resp.Value {
	key := args[1]
	var cond condition
	switch len(args) {
	case 2:
	case 4:
		var ok bool
		if cond, ok = parseCondition(string(args[2]), args[3]); !ok {
			return replyBadCondition
		}
	case 3:
		if _, ok := parseCondition(string(args[2]), nil); !ok {
			return replyBadCondition
		}
		return WrongArgs("delex")
	default:
		return WrongArgs("delex")
	}
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	rec, err := env.loadRecord(key)
	if err != nil {
		if isNotFound(err) {
			return resp.Integer(0)
		}
		return env.storageError("delex", err)
	}
	if cond.kind != "" {
		if rec.typ != typeString {
			return replyDelexType
		}
		if !cond.validDigest() {
			return replyBadDigest
		}
		if !cond.holds(true, rec.body) {
			return resp.Integer(0)
		}
	}
	var batch shale.Batch
	batchDelete(&batch, key, rec)
	if err := env.Store.Write(&batch); err != nil {
		return env.storageError("delex", err)
	}
	return resp.Integer(1)
}

// msetex is MSETEX numkeys key value [key value ...] [NX | XX] [EX s | PX ms | EXAT s |
// PXAT ms | KEEPTTL]. It sets all the keys with one expiry in one write, if the
// condition holds for all of them, and it replies 1 or 0. The options can come in any
// order.
func msetex(env *Env, cl *Client, args [][]byte) resp.Value {
	n, ok := parseInt(args[1])
	if !ok || n <= 0 || n > math.MaxInt32 {
		return replyBadNumKeys
	}
	rest := args[2:]
	if int64(len(rest)) < 2*n {
		return replyBadPairs
	}
	count := toInt(n)
	pairs, opts := rest[:2*count], rest[2*count:]
	var nx, xx, keepTTL, hasExpiry bool
	var deadline int64
	for i := 0; i < len(opts); i++ {
		name := strings.ToUpper(string(opts[i]))
		switch name {
		case "NX":
			if nx || xx {
				return replySyntax
			}
			nx = true
		case "XX":
			if nx || xx {
				return replySyntax
			}
			xx = true
		case "KEEPTTL":
			if hasExpiry || keepTTL {
				return replySyntax
			}
			keepTTL = true
		case "EX", "PX", "EXAT", "PXAT":
			if hasExpiry || keepTTL || i+1 >= len(opts) {
				return replySyntax
			}
			i++
			v, ok := parseInt(opts[i])
			if !ok {
				return replyNotInt
			}
			d, ok := setDeadline(env, name, v)
			if !ok {
				return resp.Error("ERR invalid expire time in 'msetex' command")
			}
			deadline, hasExpiry = d, true
		default:
			return replySyntax
		}
	}
	for i := range count {
		if bad, ok := env.checkKey(pairs[2*i]); !ok {
			return bad
		}
	}
	stripes := cl.lockKeys(env, count, func(i int) []byte { return pairs[2*i] })
	defer cl.unlockStripes(env, stripes)
	deadlines := make([]int64, count)
	for i := range count {
		rec, err := env.loadRecord(pairs[2*i])
		found := err == nil
		if err != nil && !isNotFound(err) {
			return env.storageError("msetex", err)
		}
		if (nx && found) || (xx && !found) {
			return resp.Integer(0)
		}
		deadlines[i] = deadline
		if keepTTL && found {
			deadlines[i] = rec.deadline
		}
	}
	var batch shale.Batch
	for i := range count {
		key := pairs[2*i]
		if rec, err := env.loadRecord(key); err == nil && rec.typ != typeString {
			batchDelete(&batch, key, rec)
		}
		batchPut(&batch, key, pairs[2*i+1], deadlines[i])
	}
	if err := env.Store.Write(&batch); err != nil {
		return env.storageError("msetex", err)
	}
	return resp.Integer(1)
}

// parseBitOffset returns the offset of SETBIT and GETBIT, or false if it is not a
// number from 0 to 2^32 - 1.
func parseBitOffset(b []byte) (int64, bool) {
	n, ok := parseInt(b)
	return n, ok && n >= 0 && n <= maxBitOffset
}

// setbit is SETBIT key offset value. It sets a bit of a string, which grows with zero
// bytes, and replies with the old value of the bit. Bit 0 is the most significant bit
// of the first byte.
func setbit(env *Env, cl *Client, args [][]byte) resp.Value {
	offset, ok := parseBitOffset(args[2])
	if !ok {
		return replyBadBitOffset
	}
	var bit byte
	switch string(args[3]) {
	case "0":
	case "1":
		bit = 1
	default:
		return replyBadBit
	}
	key := args[1]
	if bad, ok := env.checkKey(key); !ok {
		return bad
	}
	index := offset >> 3
	if index >= int64(maxStringBytes) {
		return replyValueBig
	}
	stripe := cl.lock(env, key)
	defer cl.unlock(env, stripe)
	old, deadline, err := env.load(key)
	if err != nil && !isNotFound(err) {
		return env.storageError("setbit", err)
	}
	value := old
	if int64(len(value)) <= index {
		value = make([]byte, index+1)
		copy(value, old)
	} else {
		value = bytes.Clone(old)
	}
	mask := byte(1) << (7 - uint(offset&7))
	was := value[index]&mask != 0
	if bit == 1 {
		value[index] |= mask
	} else {
		value[index] &^= mask
	}
	if err := env.save(key, value, deadline); err != nil {
		return env.storageError("setbit", err)
	}
	if was {
		return resp.Integer(1)
	}
	return resp.Integer(0)
}

// getbit is GETBIT key offset. It replies with a bit of a string, and 0 for a bit
// past its end.
func getbit(env *Env, _ *Client, args [][]byte) resp.Value {
	offset, ok := parseBitOffset(args[2])
	if !ok {
		return replyBadBitOffset
	}
	if bad, ok := env.checkKey(args[1]); !ok {
		return bad
	}
	value, _, err := env.load(args[1])
	if isNotFound(err) {
		return resp.Integer(0)
	}
	if err != nil {
		return env.storageError("getbit", err)
	}
	index := offset >> 3
	if index >= int64(len(value)) {
		return resp.Integer(0)
	}
	if value[index]&(byte(1)<<(7-uint(offset&7))) != 0 {
		return resp.Integer(1)
	}
	return resp.Integer(0)
}

// lcs is LCS key1 key2 [LEN] [IDX] [MINMATCHLEN n] [WITHMATCHLEN]. It finds the longest
// common subsequence of two strings. With no option it replies with the subsequence,
// with LEN its length, and with IDX the ranges of each match in both strings, the last
// match first.
func lcs(env *Env, _ *Client, args [][]byte) resp.Value {
	var wantLen, wantIdx, withMatchLen bool
	var minMatch int64
	for i := 3; i < len(args); i++ {
		switch strings.ToUpper(string(args[i])) {
		case "IDX":
			wantIdx = true
		case "LEN":
			wantLen = true
		case "WITHMATCHLEN":
			withMatchLen = true
		case "MINMATCHLEN":
			if i+1 >= len(args) {
				return replySyntax
			}
			i++
			v, ok := parseInt(args[i])
			if !ok {
				return replyNotInt
			}
			minMatch = max(v, 0)
		default:
			return replySyntax
		}
	}
	if wantLen && wantIdx {
		return replyLCSBoth
	}
	var strs [2][]byte
	for i := range 2 {
		if bad, ok := env.checkKey(args[1+i]); !ok {
			return bad
		}
		rec, err := env.loadRecord(args[1+i])
		switch {
		case err == nil && rec.typ != typeString:
			return replyLCSType
		case err == nil:
			strs[i] = rec.body
		case !isNotFound(err):
			return env.storageError("lcs", err)
		}
	}
	a, b := strs[0], strs[1]
	if (len(a)+1)*(len(b)+1) > maxLCSCells {
		return replyLCSMemory
	}
	width := len(b) + 1
	table := make([]uint32, (len(a)+1)*width)
	for i := 1; i <= len(a); i++ {
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				table[i*width+j] = table[(i-1)*width+j-1] + 1
			} else {
				table[i*width+j] = max(table[(i-1)*width+j], table[i*width+j-1])
			}
		}
	}
	total := int(table[len(a)*width+len(b)])
	if wantLen {
		return resp.Integer(int64(total))
	}
	result := make([]byte, total)
	var matches []resp.Value
	idx := total
	i, j := len(a), len(b)
	arangeStart, arangeEnd, brangeStart, brangeEnd := len(a), 0, 0, 0
	for i > 0 && j > 0 {
		emit := false
		if a[i-1] == b[j-1] {
			result[idx-1] = a[i-1]
			if arangeStart == len(a) {
				arangeStart, arangeEnd, brangeStart, brangeEnd = i-1, i-1, j-1, j-1
			} else if arangeStart == i && brangeStart == j {
				arangeStart--
				brangeStart--
			} else {
				emit = true
			}
			if arangeStart == 0 || brangeStart == 0 {
				emit = true
			}
			idx--
			i--
			j--
		} else {
			if table[(i-1)*width+j] > table[i*width+j-1] {
				i--
			} else {
				j--
			}
			if arangeStart != len(a) {
				emit = true
			}
		}
		if emit {
			length := int64(arangeEnd - arangeStart + 1)
			if wantIdx && (minMatch == 0 || length >= minMatch) {
				match := []resp.Value{
					resp.Array(resp.Integer(int64(arangeStart)), resp.Integer(int64(arangeEnd))),
					resp.Array(resp.Integer(int64(brangeStart)), resp.Integer(int64(brangeEnd))),
				}
				if withMatchLen {
					match = append(match, resp.Integer(length))
				}
				matches = append(matches, resp.Array(match...))
			}
			arangeStart = len(a)
		}
	}
	if wantIdx {
		return resp.Map(
			resp.BulkString("matches"), resp.Array(matches...),
			resp.BulkString("len"), resp.Integer(int64(total)),
		)
	}
	return resp.Bulk(result)
}
