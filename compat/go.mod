module github.com/thalesdev/sediment/compat

go 1.27.4

require (
	github.com/redis/go-redis/v9 v9.23.0
	github.com/syndtr/goleveldb v1.0.0
	github.com/thalesdev/sediment v0.0.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/golang/snappy v1.0.0 // indirect
	go.opentelemetry.io/otel v1.43.0 // indirect
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace github.com/thalesdev/sediment => ../
