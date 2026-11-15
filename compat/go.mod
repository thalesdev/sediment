module github.com/thalesdev/sediment/compat

go 1.27.4

require (
	github.com/syndtr/goleveldb v1.0.0
	github.com/thalesdev/sediment v0.0.0
)

require (
	github.com/cespare/xxhash/v2 v2.2.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/golang/snappy v1.0.0 // indirect
	github.com/redis/go-redis/v9 v9.7.0 // indirect
)

replace github.com/thalesdev/sediment => ../
