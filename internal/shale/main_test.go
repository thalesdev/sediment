// SPDX-License-Identifier: MIT

package shale

import (
	"flag"
	"os"
	"testing"
)

func TestMain(m *testing.M) {
	flag.Parse()
	if os.Getenv(childDirEnv) == "" {
		skipSync.Store(testing.Short())
	}
	os.Exit(m.Run())
}
