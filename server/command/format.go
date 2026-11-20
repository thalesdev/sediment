// SPDX-License-Identifier: MIT

package command

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/thalesdev/sediment/internal/shale"
)

// formatVersion is the version of the layout of the keys and values of the server.
// It changes only when a layout changes in a way that an older server cannot read.
const formatVersion = 1

var errOldLayout = errors.New("the data directory has keys written before the key layout of version 1, and this server cannot read them")

// EnsureFormat checks that the data of the store has the layout that this server
// reads. A store with no key gets the record of the version. A store that has keys
// and no record has the layout of a server that came before, and a store with
// another version is from another server. Both are an error, and EnsureFormat
// changes nothing in that case. It must run before the server takes clients.
func EnsureFormat(s Store) error {
	raw, err := s.Get(systemKey("format"))
	if err == nil {
		if len(raw) != 4 {
			return fmt.Errorf("the format record of the data directory is not valid")
		}
		if v := binary.BigEndian.Uint32(raw); v != formatVersion {
			return fmt.Errorf("the data directory has format %d, and this server reads format %d", v, formatVersion)
		}
		return nil
	}
	if !errors.Is(err, shale.ErrNotFound) {
		return err
	}
	it, err := s.NewIterator(nil)
	if err != nil {
		return err
	}
	it.SeekToFirst()
	nonEmpty := it.Valid()
	if err := it.Err(); err != nil {
		it.Close()
		return err
	}
	it.Close()
	if nonEmpty {
		return errOldLayout
	}
	return s.Put(systemKey("format"), binary.BigEndian.AppendUint32(nil, formatVersion))
}
