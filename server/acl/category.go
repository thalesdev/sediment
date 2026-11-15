// SPDX-License-Identifier: MIT

package acl

import "strings"

// Category is a set of command groups, as a bit mask. Every command is in some
// of them, and a user can be allowed or denied a whole category.
type Category uint16

const (
	// CatRead is for commands that read data.
	CatRead Category = 1 << iota

	// CatWrite is for commands that change data.
	CatWrite

	// CatAdmin is for commands that manage the server.
	CatAdmin

	// CatDangerous is for commands that can expose or break the server.
	CatDangerous

	// CatConnection is for commands that manage the connection.
	CatConnection

	// CatKeyspace is for commands that work on keys without regard to the value.
	CatKeyspace

	// CatString is for commands on string values.
	CatString

	// CatFast is for commands that run in constant or near constant time.
	CatFast

	// CatSlow is for commands that are not fast.
	CatSlow

	// CatAll is the mask of every category.
	CatAll = CatRead | CatWrite | CatAdmin | CatDangerous | CatConnection | CatKeyspace | CatString | CatFast | CatSlow
)

var categoryNames = []struct {
	name string
	cat  Category
}{
	{"read", CatRead}, {"write", CatWrite}, {"admin", CatAdmin}, {"dangerous", CatDangerous},
	{"connection", CatConnection}, {"keyspace", CatKeyspace}, {"string", CatString},
	{"fast", CatFast}, {"slow", CatSlow}, {"all", CatAll},
}

// ParseCategory returns the category that has the name, without the @ sign and
// in any case.
func ParseCategory(name string) (Category, bool) {
	name = strings.ToLower(name)
	for _, c := range categoryNames {
		if c.name == name {
			return c.cat, true
		}
	}
	return 0, false
}

// Names returns the names of the single categories in c, with the @ sign, in a
// fixed order.
func (c Category) Names() []string {
	var out []string
	for _, n := range categoryNames {
		if n.cat != CatAll && c&n.cat != 0 {
			out = append(out, "@"+n.name)
		}
	}
	return out
}
