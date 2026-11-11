module github.com/thalesdev/sediment/compat

go 1.25.6

require (
	github.com/syndtr/goleveldb v1.0.0
	github.com/thalesdev/sediment v0.0.0
)

require github.com/golang/snappy v0.0.0-20180518054509-2e65f85255db // indirect

replace github.com/thalesdev/sediment => ../
