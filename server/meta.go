// SPDX-License-Identifier: MIT

package server

import (
	"math"
	"path"
	"runtime"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Info returns the text of INFO for a section, or for all of them if section is
// empty. The names of the sections are server, clients, stats and shale.
func (s *Server) Info(section string) string {
	var b strings.Builder
	want := func(name string) bool {
		return section == "" || section == "all" || section == "everything" || section == name
	}
	line := func(k, v string) {
		b.WriteString(k)
		b.WriteByte(':')
		b.WriteString(v)
		b.WriteString("\r\n")
	}
	num := func(n uint64) string { return strconv.FormatUint(n, 10) }
	if want("server") {
		b.WriteString("# Server\r\n")
		line("sediment_version", s.opts.Version)
		line("redis_version", "7.2.0")
		line("os", runtime.GOOS+" "+runtime.GOARCH)
		line("go_version", runtime.Version())
		line("uptime_in_seconds", num(uint64(time.Since(s.started).Seconds())))
		line("loading", "0")
		health := "ok"
		if s.env.Health != nil && s.env.Health() != nil {
			health = "failed"
		}
		line("storage", health)
		b.WriteString("\r\n")
	}
	if want("clients") {
		b.WriteString("# Clients\r\n")
		line("connected_clients", strconv.FormatInt(s.clients.Load(), 10))
		line("maxclients", strconv.Itoa(s.opts.MaxClients))
		b.WriteString("\r\n")
	}
	if want("stats") {
		b.WriteString("# Stats\r\n")
		line("total_connections_received", num(s.totalConns.Load()))
		line("rejected_connections", num(s.rejectedConns.Load()))
		line("total_commands_processed", num(s.totalCommands.Load()))
		line("protocol_errors", num(s.protocolErrs.Load()))
		line("denied_commands", num(s.deniedCmds.Load()))
		line("read_calls", num(s.readCalls.Load()))
		line("read_bytes", num(s.readBytes.Load()))
		line("set_batches", num(s.setBatches.Load()))
		line("set_batched_commands", num(s.setBatched.Load()))
		b.WriteString("\r\n")
	}
	if want("persistence") {
		st := s.env.Backup.State()
		b.WriteString("# Persistence\r\n")
		line("bgsave_in_progress", map[bool]string{true: "1", false: "0"}[st.Running])
		line("lastsave", strconv.FormatInt(st.LastSave, 10))
		status := "ok"
		if st.LastErr != nil {
			status = "err"
		}
		line("last_bgsave_status", status)
		line("last_bgsave_time_sec", strconv.FormatInt(int64(st.LastDuration.Seconds()), 10))
		line("last_bgsave_bytes", num(st.LastBytes))
		line("last_bgsave_name", st.LastName)
		line("backup_dir_set", map[bool]string{true: "1", false: "0"}[s.opts.BackupDir != ""])
		b.WriteString("\r\n")
	}
	if want("shale") {
		m := s.opts.Store.Metrics()
		b.WriteString("# Shale\r\n")
		line("tables", strconv.Itoa(m.Tables))
		line("disk_bytes", num(m.DiskBytes))
		line("compactions", num(m.Compactions))
		line("compaction_bytes_read", num(m.CompactionBytesRead))
		line("compaction_bytes_written", num(m.CompactionBytesWritten))
		line("flush_bytes", num(m.FlushBytes))
		line("write_stalls_flush", num(m.StallFlushCount))
		line("write_stalls_flush_ms", strconv.FormatInt(m.StallFlushNanos/1e6, 10))
		line("write_stalls_compaction", num(m.StallCompactionCount))
		line("write_stalls_compaction_ms", strconv.FormatInt(m.StallCompactionNanos/1e6, 10))
		line("freezes", num(m.FreezeCount))
		line("freeze_ms", strconv.FormatInt(m.FreezeNanos/1e6, 10))
		line("freeze_max_ms", strconv.FormatInt(m.FreezeMaxNanos/1e6, 10))
		line("flushes", num(m.Flushes))
		line("flush_ms", strconv.FormatInt(m.FlushNanos/1e6, 10))
		line("compaction_ms", strconv.FormatInt(m.CompactionNanos/1e6, 10))
		line("block_cache_hits", num(m.BlockCacheHits))
		line("block_cache_misses", num(m.BlockCacheMisses))
		line("block_cache_bytes", strconv.Itoa(m.BlockCacheBytes))
		b.WriteString("\r\n")
	}
	if want("runtime") {
		b.WriteString("# Runtime\r\n")
		for _, kv := range runtimeInfo() {
			line(kv[0], kv[1])
		}
		b.WriteString("\r\n")
	}
	return b.String()
}

// histogramQuantile returns the upper bound of the bucket that holds the quantile q of
// the histogram, or 0 if it has no value. The unit is the one of the histogram.
func histogramQuantile(h *metrics.Float64Histogram, q float64) float64 {
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := uint64(math.Ceil(q * float64(total)))
	var seen uint64
	for i, c := range h.Counts {
		seen += c
		if seen >= target && c > 0 {
			if hi := h.Buckets[i+1]; !math.IsInf(hi, 1) {
				return hi
			}
			return h.Buckets[i]
		}
	}
	return 0
}

// runtimeInfo returns what the Go runtime knows about the delays that a request can
// have: how long a goroutine waited to run, and how long the garbage collector
// stopped the program. The times are since the start of the process.
func runtimeInfo() [][2]string {
	samples := []metrics.Sample{
		{Name: "/sched/latencies:seconds"},
		{Name: "/sched/pauses/total/gc:seconds"},
		{Name: "/gc/cycles/total:gc-cycles"},
		{Name: "/sched/goroutines:goroutines"},
	}
	metrics.Read(samples)
	out := [][2]string{{"gomaxprocs", strconv.Itoa(runtime.GOMAXPROCS(0))}}
	if v := samples[3].Value; v.Kind() == metrics.KindUint64 {
		out = append(out, [2]string{"goroutines", strconv.FormatUint(v.Uint64(), 10)})
	}
	if v := samples[2].Value; v.Kind() == metrics.KindUint64 {
		out = append(out, [2]string{"gc_cycles", strconv.FormatUint(v.Uint64(), 10)})
	}
	ms := func(sec float64) string { return strconv.FormatFloat(sec*1e3, 'f', 3, 64) }
	for _, h := range []struct {
		prefix string
		sample metrics.Sample
	}{{"sched_latency", samples[0]}, {"gc_pause", samples[1]}} {
		if h.sample.Value.Kind() != metrics.KindFloat64Histogram {
			continue
		}
		hist := h.sample.Value.Float64Histogram()
		out = append(out,
			[2]string{h.prefix + "_p50_ms", ms(histogramQuantile(hist, 0.50))},
			[2]string{h.prefix + "_p99_ms", ms(histogramQuantile(hist, 0.99))},
			[2]string{h.prefix + "_p999_ms", ms(histogramQuantile(hist, 0.999))},
			[2]string{h.prefix + "_max_ms", ms(histogramQuantile(hist, 1))},
		)
	}
	return out
}

// Config returns the name and value of each setting whose name matches the glob
// pattern, sorted by name.
func (s *Server) Config(pattern string) [][2]string {
	settings := map[string]string{
		"maxclients":         strconv.Itoa(s.opts.MaxClients),
		"timeout":            strconv.Itoa(int(s.opts.IdleTimeout / time.Second)),
		"sweep-interval":     strconv.Itoa(int(s.opts.SweepInterval / time.Second)),
		"backup-dir":         s.opts.BackupDir,
		"databases":          "1",
		"save":               "",
		"appendonly":         "no",
		"proto-max-bulk-len": strconv.Itoa(s.opts.Limits.MaxBulk),
		"max-key-len":        strconv.Itoa(s.opts.MaxKey),
	}
	for k, v := range s.opts.Settings {
		settings[strings.ToLower(k)] = v
	}
	names := make([]string, 0, len(settings))
	for name := range settings {
		if ok, err := path.Match(pattern, name); err == nil && ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	out := make([][2]string, len(names))
	for i, name := range names {
		out[i] = [2]string{name, settings[name]}
	}
	return out
}
