// SPDX-License-Identifier: MIT

package server

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server/command"
)

// commandBuckets are the bounds of the buckets of the latency of a command, in
// seconds, from 5 microseconds to 1 second.
var commandBuckets = []float64{5e-6, 1e-5, 2.5e-5, 5e-5, 1e-4, 2.5e-4, 5e-4, 1e-3, 2.5e-3, 5e-3, 1e-2, 5e-2, 1e-1, 1}

// cmdAttrs are the options of the measurements of one command, made once so that a
// command allocates nothing to record itself.
type cmdAttrs struct {
	ok, failed, latency metric.MeasurementOption
}

// instruments holds the instruments of the server.
type instruments struct {
	commands metric.Int64Counter
	latency  metric.Float64Histogram
	attrs    []cmdAttrs
}

// newMetrics makes the instruments of the server with the meter, and registers the
// callback that reports the counts that the server and the storage keep. It returns
// nil if meter is nil.
func newMetrics(meter metric.Meter, specs []*command.Spec, s *Server) (*instruments, error) {
	if meter == nil {
		return nil, nil
	}
	m := &instruments{attrs: make([]cmdAttrs, len(specs))}
	var err error
	if m.commands, err = meter.Int64Counter("sediment.commands", metric.WithUnit("{command}"),
		metric.WithDescription("Commands that ran, by name and result.")); err != nil {
		return nil, err
	}
	if m.latency, err = meter.Float64Histogram("sediment.command.duration", metric.WithUnit("s"),
		metric.WithDescription("Time that a command took to run, by name."),
		metric.WithExplicitBucketBoundaries(commandBuckets...)); err != nil {
		return nil, err
	}
	for _, spec := range specs {
		name := attribute.String("command", spec.Name)
		m.attrs[spec.Slot] = cmdAttrs{
			ok:      metric.WithAttributeSet(attribute.NewSet(name, attribute.String("result", "ok"))),
			failed:  metric.WithAttributeSet(attribute.NewSet(name, attribute.String("result", "error"))),
			latency: metric.WithAttributeSet(attribute.NewSet(name)),
		}
	}
	if err := registerObservables(meter, s); err != nil {
		return nil, err
	}
	return m, nil
}

// record counts one command, or n commands that ran as one write, and the time that
// they took.
func (m *instruments) record(spec *command.Spec, n int64, kind resp.Kind, took time.Duration) {
	a := &m.attrs[spec.Slot]
	if kind == resp.KindError {
		m.commands.Add(context.Background(), n, a.failed)
	} else {
		m.commands.Add(context.Background(), n, a.ok)
	}
	m.latency.Record(context.Background(), took.Seconds(), a.latency)
}

// registerObservables registers the instruments that are read when the metrics are
// collected: the clients, the counts of the server, and the counts of the storage.
func registerObservables(meter metric.Meter, s *Server) error {
	counter := func(name, unit, desc string) (metric.Int64ObservableCounter, error) {
		return meter.Int64ObservableCounter(name, metric.WithUnit(unit), metric.WithDescription(desc))
	}
	seconds := func(name, desc string) (metric.Float64ObservableCounter, error) {
		return meter.Float64ObservableCounter(name, metric.WithUnit("s"), metric.WithDescription(desc))
	}
	gauge := func(name, unit, desc string) (metric.Int64ObservableGauge, error) {
		return meter.Int64ObservableGauge(name, metric.WithUnit(unit), metric.WithDescription(desc))
	}
	var err error
	var (
		clients, tables, diskBytes, cacheBytes                                         metric.Int64ObservableGauge
		uptime                                                                         metric.Int64ObservableGauge
		conns, rejected, denied, protoErrs, readBytes, setBatches, flushes, flushBytes metric.Int64ObservableCounter
		compactions, compactRead, compactWritten, cacheHits, cacheMisses, stallCount   metric.Int64ObservableCounter
		stallSeconds, flushSeconds, compactSeconds, freezeSeconds                      metric.Float64ObservableCounter
	)
	for _, g := range []struct {
		dst        *metric.Int64ObservableGauge
		name, unit string
		desc       string
	}{
		{&clients, "sediment.clients", "{client}", "Clients that are connected."},
		{&uptime, "sediment.uptime", "s", "Time since the server started."},
		{&tables, "sediment.shale.tables", "{table}", "Table files that the storage reads."},
		{&diskBytes, "sediment.shale.disk", "By", "Size of the table files that the storage reads."},
		{&cacheBytes, "sediment.shale.block_cache.size", "By", "Bytes in the block cache."},
	} {
		if *g.dst, err = gauge(g.name, g.unit, g.desc); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		dst        *metric.Int64ObservableCounter
		name, unit string
		desc       string
	}{
		{&conns, "sediment.connections", "{connection}", "Connections that the server accepted."},
		{&rejected, "sediment.connections.rejected", "{connection}", "Connections that the server refused because it had too many."},
		{&denied, "sediment.commands.denied", "{command}", "Commands that the rules of a user refused."},
		{&protoErrs, "sediment.protocol.errors", "{error}", "Protocol errors of the clients."},
		{&readBytes, "sediment.network.read", "By", "Bytes that the server read from the clients."},
		{&setBatches, "sediment.set.batches", "{batch}", "Groups of SET commands of a pipeline that ran as one write."},
		{&flushes, "sediment.shale.flushes", "{flush}", "Memtables that the storage wrote to tables."},
		{&flushBytes, "sediment.shale.flush.written", "By", "Bytes of the tables that the flushes wrote."},
		{&compactions, "sediment.shale.compactions", "{compaction}", "Compactions that finished."},
		{&compactRead, "sediment.shale.compaction.read", "By", "Bytes of the tables that the compactions read."},
		{&compactWritten, "sediment.shale.compaction.written", "By", "Bytes of the tables that the compactions wrote."},
		{&cacheHits, "sediment.shale.block_cache.hits", "{read}", "Reads of a block that the cache had."},
		{&cacheMisses, "sediment.shale.block_cache.misses", "{read}", "Reads of a block that the cache did not have."},
		{&stallCount, "sediment.shale.write.stalls", "{wait}", "Waits of a write, by reason."},
	} {
		if *c.dst, err = counter(c.name, c.unit, c.desc); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		dst        *metric.Float64ObservableCounter
		name, desc string
	}{
		{&stallSeconds, "sediment.shale.write.stall.time", "Time that the writes waited, by reason."},
		{&flushSeconds, "sediment.shale.flush.time", "Time that the flushes took."},
		{&compactSeconds, "sediment.shale.compaction.time", "Time that the compactions took."},
		{&freezeSeconds, "sediment.shale.freeze.time", "Time that the freezes of the memtable took."},
	} {
		if *c.dst, err = seconds(c.name, c.desc); err != nil {
			return err
		}
	}
	flushReason := metric.WithAttributes(attribute.String("reason", "flush"))
	compactReason := metric.WithAttributes(attribute.String("reason", "compaction"))
	_, err = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		o.ObserveInt64(clients, s.clients.Load())
		o.ObserveInt64(uptime, int64(time.Since(s.started).Seconds()))
		o.ObserveInt64(conns, int64(s.totalConns.Load()))
		o.ObserveInt64(rejected, int64(s.rejectedConns.Load()))
		o.ObserveInt64(denied, int64(s.deniedCmds.Load()))
		o.ObserveInt64(protoErrs, int64(s.protocolErrs.Load()))
		o.ObserveInt64(readBytes, int64(s.readBytes.Load()))
		o.ObserveInt64(setBatches, int64(s.setBatches.Load()))
		st := s.env.Store.Metrics()
		o.ObserveInt64(tables, int64(st.Tables))
		o.ObserveInt64(diskBytes, int64(st.DiskBytes))
		o.ObserveInt64(cacheBytes, int64(st.BlockCacheBytes))
		o.ObserveInt64(flushes, int64(st.Flushes))
		o.ObserveInt64(flushBytes, int64(st.FlushBytes))
		o.ObserveInt64(compactions, int64(st.Compactions))
		o.ObserveInt64(compactRead, int64(st.CompactionBytesRead))
		o.ObserveInt64(compactWritten, int64(st.CompactionBytesWritten))
		o.ObserveInt64(cacheHits, int64(st.BlockCacheHits))
		o.ObserveInt64(cacheMisses, int64(st.BlockCacheMisses))
		o.ObserveInt64(stallCount, int64(st.StallFlushCount), flushReason)
		o.ObserveInt64(stallCount, int64(st.StallCompactionCount), compactReason)
		o.ObserveFloat64(stallSeconds, time.Duration(st.StallFlushNanos).Seconds(), flushReason)
		o.ObserveFloat64(stallSeconds, time.Duration(st.StallCompactionNanos).Seconds(), compactReason)
		o.ObserveFloat64(flushSeconds, time.Duration(st.FlushNanos).Seconds())
		o.ObserveFloat64(compactSeconds, time.Duration(st.CompactionNanos).Seconds())
		o.ObserveFloat64(freezeSeconds, time.Duration(st.FreezeNanos).Seconds())
		return nil
	}, clients, uptime, conns, rejected, denied, protoErrs, readBytes, setBatches, tables, diskBytes, cacheBytes,
		flushes, flushBytes, compactions, compactRead, compactWritten, cacheHits, cacheMisses, stallCount,
		stallSeconds, flushSeconds, compactSeconds, freezeSeconds)
	return err
}
