// SPDX-License-Identifier: MIT

package server

import (
	"path"
	"runtime"
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
	if want("shale") {
		m := s.opts.Store.Metrics()
		b.WriteString("# Shale\r\n")
		line("tables", strconv.Itoa(m.Tables))
		line("disk_bytes", num(m.DiskBytes))
		line("compactions", num(m.Compactions))
		line("compaction_bytes_read", num(m.CompactionBytesRead))
		line("compaction_bytes_written", num(m.CompactionBytesWritten))
		line("flush_bytes", num(m.FlushBytes))
		line("block_cache_hits", num(m.BlockCacheHits))
		line("block_cache_misses", num(m.BlockCacheMisses))
		line("block_cache_bytes", strconv.Itoa(m.BlockCacheBytes))
		b.WriteString("\r\n")
	}
	return b.String()
}

// Config returns the name and value of each setting whose name matches the glob
// pattern, sorted by name.
func (s *Server) Config(pattern string) [][2]string {
	settings := map[string]string{
		"maxclients":         strconv.Itoa(s.opts.MaxClients),
		"timeout":            strconv.Itoa(int(s.opts.IdleTimeout / time.Second)),
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
