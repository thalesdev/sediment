// SPDX-License-Identifier: MIT

package server

import (
	"testing"

	"go.opentelemetry.io/otel/metric/noop"
)

func TestNoopMeter(t *testing.T) {
	t.Run("Should run commands with a meter that records nothing", func(t *testing.T) {
		_, addr := startServer(t, Options{Meter: noop.NewMeterProvider().Meter("test")})
		c := dial(t, addr)
		wantSimple(t, c.do("SET", "k", "v"), "OK")
		wantBulk(t, c.do("GET", "k"), "v")
		wantError(t, c.do("INCR", "k"), "ERR")
		c.send("SET", "a", "1")
		c.send("SET", "b", "2")
		wantSimple(t, c.read(), "OK")
		wantSimple(t, c.read(), "OK")
	})
}
