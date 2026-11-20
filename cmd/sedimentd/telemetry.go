// SPDX-License-Identifier: MIT

package main

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/runtime"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/thalesdev/sediment/server/acl"
	"github.com/thalesdev/sediment/server/config"
)

// telemetry holds the metrics of the process: the meter that the server records
// with, and the HTTP handler of the Prometheus endpoint, if there is one.
type telemetry struct {
	meter    metric.Meter
	tracer   trace.Tracer
	handler  http.Handler
	shutdown func(context.Context) error
}

// newResource describes this process in the metrics.
func newResource(version string) (*resource.Resource, error) {
	return resource.Merge(resource.Default(), resource.NewSchemaless(
		attribute.String("service.name", "sedimentd"),
		attribute.String("service.version", version),
	))
}

// setupTelemetry makes the metrics that the settings ask for: a Prometheus
// endpoint, an OTLP export, or both. It returns nil if the settings ask for none,
// and then the server records nothing and pays nothing. The metrics of the Go
// runtime go with the others.
func setupTelemetry(ctx context.Context, cfg config.Config, version string) (*telemetry, error) {
	if cfg.MetricsAddr == "" && !cfg.OTLPMetrics && !cfg.OTLPTraces {
		return nil, nil
	}
	res, err := newResource(version)
	if err != nil {
		return nil, err
	}
	opts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	t := &telemetry{}
	var stops []func(context.Context) error
	if cfg.OTLPTraces {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res), sdktrace.WithSampler(sdktrace.AlwaysSample()))
		t.tracer = tp.Tracer("github.com/thalesdev/sediment")
		stops = append(stops, tp.Shutdown)
	}
	if cfg.MetricsAddr == "" && !cfg.OTLPMetrics {
		t.shutdown = joinShutdown(stops)
		return t, nil
	}
	if cfg.MetricsAddr != "" {
		registry := prometheus.NewRegistry()
		exp, err := otelprom.New(otelprom.WithRegisterer(registry))
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdkmetric.WithReader(exp))
		t.handler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	}
	if cfg.OTLPMetrics {
		exp, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return nil, err
		}
		opts = append(opts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp, sdkmetric.WithInterval(cfg.OTLPInterval))))
	}
	provider := sdkmetric.NewMeterProvider(opts...)
	if err := runtime.Start(runtime.WithMeterProvider(provider)); err != nil {
		return nil, err
	}
	t.meter = provider.Meter("github.com/thalesdev/sediment")
	t.shutdown = joinShutdown(append(stops, provider.Shutdown))
	return t, nil
}

// joinShutdown returns a function that runs every stop and returns their errors
// joined.
func joinShutdown(stops []func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) error {
		var errs []error
		for _, stop := range stops {
			errs = append(errs, stop(ctx))
		}
		return errors.Join(errs...)
	}
}

// basicAuth wraps next so that it needs the user and the password of the config.
// It compares the user in constant time, and checks the password against the hash.
func basicAuth(next http.Handler, user, hash string, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, pw, ok := r.BasicAuth()
		match := subtle.ConstantTimeCompare([]byte(u), []byte(user)) == 1
		good, err := acl.CheckHash(hash, []byte(pw))
		if !ok || !match || err != nil || !good {
			log.Warn("metrics sign in failed", "addr", r.RemoteAddr)
			time.Sleep(100 * time.Millisecond)
			w.Header().Set("WWW-Authenticate", `Basic realm="sedimentd"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// metricsServer returns the HTTP server of the metrics endpoint, which serves only
// /metrics. It needs a sign in if the config has a user.
func metricsServer(cfg config.Config, handler http.Handler, log *slog.Logger) *http.Server {
	if cfg.MetricsUser != "" {
		handler = basicAuth(handler, cfg.MetricsUser, cfg.MetricsPasswordHash, log)
	}
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", handler)
	return &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       time.Minute,
		MaxHeaderBytes:    16 << 10,
	}
}

// startMetrics listens on the address of the metrics endpoint and serves it in the
// background, with TLS if tlsCfg is not nil. It returns the function that stops it.
func startMetrics(cfg config.Config, handler http.Handler, tlsCfg *tls.Config, log *slog.Logger) (func(context.Context) error, error) {
	if cfg.MetricsUser != "" {
		if _, err := acl.CheckHash(cfg.MetricsPasswordHash, nil); err != nil {
			return nil, errors.New("metrics-password-hash is not a hash that sedimentd hash-password wrote")
		}
	}
	l, err := net.Listen("tcp", cfg.MetricsAddr)
	if err != nil {
		return nil, err
	}
	if tlsCfg != nil {
		l = tls.NewListener(l, tlsCfg)
	}
	srv := metricsServer(cfg, handler, log)
	go func() {
		if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("metrics endpoint stopped", "err", err)
		}
	}()
	log.Info("metrics endpoint", "addr", l.Addr().String(), "tls", tlsCfg != nil, "auth", cfg.MetricsUser != "")
	return srv.Shutdown, nil
}
