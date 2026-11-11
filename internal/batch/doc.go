// SPDX-License-Identifier: MIT

// Package batch encodes and decodes a batch of writes. It is the WriteBatch
// format of LevelDB, and it is the payload of a record in the write-ahead log.
//
// A batch starts with a header of 12 bytes: the sequence number of the first
// operation as a little-endian uint64, and the number of operations as a
// little-endian uint32. The operations follow. Each one has a kind of 1 byte,
// the key as a varint length and the bytes, and, for a put only, the value in
// the same way. The operation number i has the sequence number seq + i.
//
// Parse checks a whole batch before it returns, so a caller that applies the
// operations never applies a part of a batch that is not valid.
package batch
