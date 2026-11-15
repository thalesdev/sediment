// SPDX-License-Identifier: MIT

package command

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/thalesdev/sediment/internal/shale"
)

// uidBlock is how many collection ids one write of the high mark reserves.
const uidBlock = 4096

// allocUID returns a new collection id. An id is never used twice, and never
// reused after a restart: the store keeps a high mark, and a restart starts above
// it. The ids are taken in blocks, so most calls do not write.
func (e *Env) allocUID() (uint64, error) {
	e.uidMu.Lock()
	defer e.uidMu.Unlock()
	if e.uidNext == 0 || e.uidNext > e.uidLimit {
		var base uint64
		raw, err := e.Store.Get(systemKey("uid"))
		switch {
		case err == nil:
			if len(raw) != 8 {
				return 0, fmt.Errorf("the id record of the data directory is not valid")
			}
			base = binary.BigEndian.Uint64(raw)
		case !errors.Is(err, shale.ErrNotFound):
			return 0, err
		}
		base = max(base, e.uidLimit)
		limit := base + uidBlock
		if err := e.Store.Put(systemKey("uid"), binary.BigEndian.AppendUint64(nil, limit)); err != nil {
			return 0, err
		}
		e.uidNext, e.uidLimit = base+1, limit
	}
	uid := e.uidNext
	e.uidNext++
	return uid, nil
}
