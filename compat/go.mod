module github.com/thalesdev/sediment/compat

go 1.27.4

require (
	github.com/syndtr/goleveldb v1.0.0
	github.com/thalesdev/sediment v0.0.0
)

require github.com/golang/snappy v1.0.0 // indirect

replace github.com/thalesdev/sediment => ../
