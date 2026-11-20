// SPDX-License-Identifier: MIT

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/thalesdev/sediment/internal/shale"
	"github.com/thalesdev/sediment/resp"
	"github.com/thalesdev/sediment/server"
	"github.com/thalesdev/sediment/server/acl"
	"github.com/thalesdev/sediment/server/config"
)

func quiet() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func runCommand(t *testing.T, nc net.Conn, r *resp.Reader, w *resp.Writer, args ...string) resp.Value {
	t.Helper()
	return call(t, nc, r, w, args...)
}

func TestMetrics(t *testing.T) {
	t.Run("Should count the commands by name and result, and time them", func(t *testing.T) {
		reader := sdkmetric.NewManualReader()
		provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
		srv, err := server.New(server.Options{Store: openStore(t), Meter: provider.Meter("test")})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Shutdown(context.Background()) })
		nc, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		r, w := resp.NewReader(nc, resp.Limits{}), resp.NewWriter(nc)
		runCommand(t, nc, r, w, "SET", "k", "v")
		runCommand(t, nc, r, w, "GET", "k")
		runCommand(t, nc, r, w, "INCR", "k")

		var rm metricdata.ResourceMetrics
		if err := reader.Collect(context.Background(), &rm); err != nil {
			t.Fatal(err)
		}
		counts := map[string]int64{}
		histograms := map[string]uint64{}
		names := map[string]bool{}
		for _, sm := range rm.ScopeMetrics {
			for _, m := range sm.Metrics {
				names[m.Name] = true
				switch data := m.Data.(type) {
				case metricdata.Sum[int64]:
					if m.Name != "sediment.commands" {
						continue
					}
					for _, dp := range data.DataPoints {
						cmd, _ := dp.Attributes.Value("command")
						res, _ := dp.Attributes.Value("result")
						counts[cmd.AsString()+"/"+res.AsString()] += dp.Value
					}
				case metricdata.Histogram[float64]:
					for _, dp := range data.DataPoints {
						cmd, _ := dp.Attributes.Value("command")
						histograms[cmd.AsString()] += dp.Count
					}
				}
			}
		}
		if counts["set/ok"] != 1 || counts["get/ok"] != 1 || counts["incr/error"] != 1 {
			t.Fatalf("got %v", counts)
		}
		if histograms["set"] != 1 || histograms["get"] != 1 {
			t.Fatalf("got %v", histograms)
		}
		for _, want := range []string{"sediment.clients", "sediment.shale.tables", "sediment.shale.flushes", "sediment.connections", "sediment.shale.block_cache.hits"} {
			if !names[want] {
				t.Errorf("no metric %q in %v", want, names)
			}
		}
	})
}

func TestPrometheusEndpoint(t *testing.T) {
	scrape := func(t *testing.T, cfg config.Config, user, pass string) (int, string) {
		t.Helper()
		tel, err := setupTelemetry(context.Background(), cfg, "test")
		if err != nil || tel == nil {
			t.Fatalf("got %v, %v", tel, err)
		}
		t.Cleanup(func() { tel.shutdown(context.Background()) })
		srv, err := server.New(server.Options{Store: openStore(t), Meter: tel.meter})
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Shutdown(context.Background()) })
		nc, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer nc.Close()
		runCommand(t, nc, resp.NewReader(nc, resp.Limits{}), resp.NewWriter(nc), "PING")

		stop, err := startMetrics(cfg, tel.handler, nil, quiet())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { stop(context.Background()) })
		req, _ := http.NewRequest("GET", "http://"+cfg.MetricsAddr+"/metrics", nil)
		if user != "" {
			req.SetBasicAuth(user, pass)
		}
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		body, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(body)
	}
	t.Run("Should serve the metrics in the Prometheus format, with the runtime", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.MetricsAddr = "127.0.0.1:" + portString(freePort(t))
		code, body := scrape(t, cfg, "", "")
		if code != 200 {
			t.Fatalf("got %d: %s", code, body)
		}
		for _, want := range []string{"sediment_commands_total", "sediment_command_duration_seconds_bucket", "sediment_clients", "go_goroutine_count"} {
			if !strings.Contains(body, want) {
				t.Errorf("no %q in the metrics", want)
			}
		}
	})
	t.Run("Should need the user and the password when the config has them", func(t *testing.T) {
		hash, err := acl.HashPassword([]byte("scrape-pw"))
		if err != nil {
			t.Fatal(err)
		}
		cfg := config.Defaults()
		cfg.MetricsAddr = "127.0.0.1:" + portString(freePort(t))
		cfg.MetricsUser, cfg.MetricsPasswordHash = "prom", hash
		if code, _ := scrape(t, cfg, "", ""); code != 401 {
			t.Fatalf("got %d with no sign in", code)
		}
	})
	t.Run("Should accept the right password and refuse a wrong one", func(t *testing.T) {
		hash, _ := acl.HashPassword([]byte("scrape-pw"))
		h := basicAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) }), "prom", hash, quiet())
		for _, c := range []struct {
			user, pass string
			want       int
		}{{"prom", "scrape-pw", 200}, {"prom", "nope", 401}, {"other", "scrape-pw", 401}, {"", "", 401}} {
			req, _ := http.NewRequest("GET", "/metrics", nil)
			req.SetBasicAuth(c.user, c.pass)
			rec := &recorder{header: http.Header{}}
			h.ServeHTTP(rec, req)
			if rec.code != c.want {
				t.Errorf("%q/%q got %d, want %d", c.user, c.pass, rec.code, c.want)
			}
		}
	})
	t.Run("Should refuse a password hash that is not one", func(t *testing.T) {
		cfg := config.Defaults()
		cfg.MetricsAddr = "127.0.0.1:0"
		cfg.MetricsUser, cfg.MetricsPasswordHash = "prom", "plain"
		if _, err := startMetrics(cfg, http.NotFoundHandler(), nil, quiet()); err == nil {
			t.Fatal("a plain password was accepted")
		}
	})
	t.Run("Should make no metrics when the settings ask for none", func(t *testing.T) {
		tel, err := setupTelemetry(context.Background(), config.Defaults(), "test")
		if err != nil || tel != nil {
			t.Fatalf("got %v, %v", tel, err)
		}
	})
}

type recorder struct {
	header http.Header
	code   int
}

func (r *recorder) Header() http.Header         { return r.header }
func (r *recorder) Write(b []byte) (int, error) { return len(b), nil }
func (r *recorder) WriteHeader(code int)        { r.code = code }

func portString(p int) string {
	return strconv.Itoa(p)
}

func TestTracing(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	start := func(t *testing.T, rate float64, tracer bool) (net.Conn, *resp.Reader, *resp.Writer, *bytes.Buffer) {
		t.Helper()
		var logs bytes.Buffer
		o := server.Options{Store: openStore(t), TraceSampleRate: rate, SlowLogThreshold: time.Nanosecond, Log: slog.New(slog.NewTextHandler(&logs, nil))}
		if tracer {
			o.Tracer = provider.Tracer("test")
		}
		srv, err := server.New(o)
		if err != nil {
			t.Fatal(err)
		}
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		go srv.Serve(l)
		t.Cleanup(func() { srv.Shutdown(context.Background()) })
		nc, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { nc.Close() })
		return nc, resp.NewReader(nc, resp.Limits{}), resp.NewWriter(nc), &logs
	}
	attr := func(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
		for _, kv := range s.Attributes() {
			if string(kv.Key) == key {
				return kv.Value, true
			}
		}
		return attribute.Value{}, false
	}
	t.Run("Should make a span for each command with its name, key count, result and reply size, and no key", func(t *testing.T) {
		recorder.Reset()
		nc, r, w, logs := start(t, 1, true)
		runCommand(t, nc, r, w, "SET", "secret-key", "secret-value")
		runCommand(t, nc, r, w, "MGET", "secret-key", "other")
		runCommand(t, nc, r, w, "INCR", "secret-key")
		spans := recorder.Ended()
		if len(spans) != 3 {
			t.Fatalf("got %d spans", len(spans))
		}
		keys := map[string]int64{"set": 1, "mget": 2, "incr": 1}
		for _, s := range spans {
			if n, _ := attr(s, "sediment.keys"); n.AsInt64() != keys[s.Name()] {
				t.Errorf("%s has %d keys", s.Name(), n.AsInt64())
			}
			for _, kv := range s.Attributes() {
				if strings.Contains(kv.Value.Emit(), "secret") {
					t.Errorf("%s has %q in %s", s.Name(), kv.Value.Emit(), kv.Key)
				}
			}
		}
		if res, _ := attr(spans[2], "sediment.result"); res.AsString() != "error" {
			t.Fatalf("got %q", res.AsString())
		}
		if !strings.Contains(logs.String(), "trace_id="+spans[0].SpanContext().TraceID().String()) {
			t.Fatalf("the slow command log has no trace id:\n%s", logs.String())
		}
	})
	t.Run("Should make no span at rate 0 and none without a tracer", func(t *testing.T) {
		recorder.Reset()
		nc, r, w, _ := start(t, 0, true)
		runCommand(t, nc, r, w, "SET", "k", "v")
		nc2, r2, w2, _ := start(t, 1, false)
		runCommand(t, nc2, r2, w2, "SET", "k", "v")
		if n := len(recorder.Ended()); n != 0 {
			t.Fatalf("got %d spans", n)
		}
	})
	t.Run("Should make a span for a flush and for a compaction", func(t *testing.T) {
		recorder.Reset()
		db, err := shale.Open(t.TempDir(), shale.Options{MemtableSize: 4 << 10, Tracer: provider.Tracer("test")})
		if err != nil {
			t.Fatal(err)
		}
		for i := range 2000 {
			if err := db.Put([]byte(fmt.Sprintf("key-%06d", i)), bytes.Repeat([]byte("v"), 100)); err != nil {
				t.Fatal(err)
			}
		}
		if err := db.WaitIdle(); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		names := map[string]int{}
		for _, s := range recorder.Ended() {
			names[s.Name()]++
		}
		if names["shale.flush"] == 0 || names["shale.compaction"] == 0 {
			t.Fatalf("got %v", names)
		}
	})
}
