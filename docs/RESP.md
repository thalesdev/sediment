# RESP in sediment

What `sedimentd` speaks of the Redis protocol, and what it does not yet. A check
means that it works and has a test.

## Versions

A connection starts in RESP2, as in Redis. `HELLO 3` switches it to RESP3 and
`HELLO 2` switches it back. One parser reads both, and the writer picks the bytes
for the version of the connection. The commands do not know the version.

## Pipelining

The client decides. A pipeline is only a client that writes many commands and reads
the replies after, which come back in the same order. The server needs no option. If
the commands of a connection are already in its buffer, a run of plain `SET key value`
commands is written to the log as one record, and a command that is not a plain `SET`
ends the run. A client that sends one command and waits for its reply, which is the
usual way, gets no batch and loses nothing. To use the full speed of the server for
many writes, use the pipeline of the client library.

## Types

| Type | Prefix | Read | Write in RESP3 | Write in RESP2 |
|---|---|---|---|---|
| Simple string | `+` | [x] | [x] | [x] |
| Simple error | `-` | [x] | [x] | [x] |
| Integer | `:` | [x] | [x] | [x] |
| Bulk string | `$` | [x] | [x] | [x] |
| Array | `*` | [x] | [x] | [x] |
| Null | `_` | [x] | [x] | as `$-1` |
| Boolean | `#` | [x] | [x] | as integer |
| Double | `,` | [x] | [x] | as bulk string |
| Big number | `(` | [x] | [x] | as bulk string |
| Bulk error | `!` | [x] | [x] | as simple error |
| Verbatim string | `=` | [x] | [x] | as bulk string |
| Map | `%` | [x] | [x] | as flat array |
| Set | `~` | [x] | [x] | as array |
| Push | `>` | [x] | [x] | as array |
| Attribute | `\|` | [ ] refused | [ ] | [ ] |
| Streamed strings and aggregates (`$?`, `*?`) | | [ ] refused | [ ] | [ ] |

A client sends a command as an array of bulk strings, or as an inline command (a
line of words, with quotes). An inline command that starts with `POST` or `Host:`
closes the connection, so that a web page cannot talk to the server.

## Commands

| Command | State | Notes |
|---|---|---|
| `GET` | [x] | |
| `SET` | [x] | `NX`, `XX`, `GET`, `EX`, `PX`, `EXAT`, `PXAT` and `KEEPTTL`. A plain `SET` removes the TTL. |
| `DEL` | [x] | Many keys are one atomic write. |
| `EXISTS` | [x] | |
| `MGET` | [x] | The reply has a size limit. |
| `MSET` | [x] | One atomic write. |
| `PING`, `ECHO`, `QUIT` | [x] | |
| `SELECT` | [x] | Only database 0. |
| `HELLO` | [x] | `AUTH` and `SETNAME` options. |
| `AUTH` | [x] | `AUTH password` and `AUTH user password`. |
| `CLIENT` | [x] | `ID`, `SETNAME`, `GETNAME`, `SETINFO`, `INFO`. |
| `CLIENT LIST`, `KILL`, `PAUSE`, `TRACKING` | [ ] | |
| `COMMAND` | [x] | `COUNT`, `LIST`, `INFO`, `DOCS` (empty). |
| `INFO` | [x] | Sections `server`, `clients`, `stats`, `shale`, `runtime`. |
| `CONFIG GET` | [x] | Read only, no file names. |
| `CONFIG SET`, `REWRITE` | [ ] | On purpose. See [SECURITY.md](../SECURITY.md). |
| `ACL WHOAMI`, `USERS`, `LIST`, `LOAD`, `CAT` | [x] | Only for users with `@admin`. |
| `ACL SETUSER`, `DELUSER`, `SAVE`, `GETUSER`, `LOG` | [ ] | ACL phase 2. |
| `INCR`, `DECR`, `INCRBY`, `DECRBY` | [x] | Atomic. Overflow is an error. |
| `APPEND`, `STRLEN` | [x] | A string is limited to 16 MiB. |
| `SETNX`, `GETSET`, `GETDEL` | [x] | Atomic. |
| `SETRANGE`, `GETRANGE` | [ ] | |
| `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT` | [x] | With `NX`, `XX`, `GT` and `LT`. A time in the past deletes the key. |
| `TTL`, `PTTL`, `EXPIRETIME`, `PEXPIRETIME`, `PERSIST` | [x] | |
| `SCAN` | [x] | `MATCH`, `COUNT` and `TYPE`. A call looks at up to `COUNT` keys, and `COUNT` is at most 10000. A cursor resumes after the last key that the call looked at, it belongs to the connection, and a connection keeps its 32 newest cursors. An old or foreign cursor is `ERR invalid cursor`. A key that stays in the database for the whole scan is returned. Expired keys are hidden. |
| `KEYS` | [x] | `@dangerous` and slow: it reads every key that the pattern can match. The reply has a size limit. |
| `DBSIZE` | [x] | Slow: it counts the live keys, so it reads all of them. |
| `RANDOMKEY` | [x] | The first live key after a random point, so a key after a large gap is more likely. |
| `TYPE` | [x] | `string` or `none`. |
| `MULTI`, `EXEC`, `WATCH` | [ ] | Need snapshots that live across commands. A snapshot pins the table files, so the server does not hold one between commands. |
| `SUBSCRIBE`, `PUBLISH` | [ ] | |
| Lists, hashes, sets, sorted sets, streams | [ ] | Not planned for now. |
| `EVAL`, `FUNCTION`, `MODULE`, `DEBUG`, `REPLICAOF`, `SLAVEOF` | never | The source of most Redis CVEs. |

## ACL

The users are in a file with the line format of the Redis ACL file.

| Feature | State |
|---|---|
| `on`, `off`, `nopass`, `resetpass`, `reset` | [x] |
| Password as `>plain` (hashed when loaded, with a warning) or `$pbkdf2-sha256$...` | [x] |
| Password as a plain SHA-256 (`#hash`, as in the Redis file) | [ ] The file is refused: it is cheap to guess from. |
| `sedimentd hash-password` makes the `$pbkdf2-sha256$` hash from a password on standard input | [x] |
| `+@category`, `-@category`, `+command`, `-command`, `allcommands`, `nocommands` | [x] |
| `~*` and `allkeys` | [x] |
| Key patterns (`~prefix:*`, `%R~`, `%W~`) | [ ] The file is refused, it is not ignored. |
| Rules for subcommands (`+config\|get`) | [ ] The file is refused. |
| `ACL SETUSER` at run time, `ACL SAVE`, `ACL LOG`, selectors | [ ] |
| Channel patterns | [ ] Accepted, there are no channels yet. |
| User from the CN of a client certificate | [ ] |

## Limits

All of them have a setting in the file of `sedimentd`. A client that has not signed
in gets the smaller pre-sign-in limits.

| Limit | Default | Before sign in |
|---|---|---|
| Bulk string | 16 MiB | 1 KiB |
| Arguments of a command | 1024 | 8 |
| Bytes of one command | 32 MiB | 4 KiB |
| Inline line | 64 KiB | 2 KiB |
| Key | 64 KiB | |
| Reply of `MGET`, `KEYS` and a page of `SCAN` | 64 MiB | |
| `COUNT` of `SCAN` | 10000 | |
| Nesting of a value | 32 | 4 |
| Clients | 10000 | |
| Idle client | 10 min | 30 s |
| Time to send the rest of a command | 30 s | |
| Time to take a reply | 30 s | |
| TLS handshake | 10 s | |
| Failed sign ins before the close | 5, each one waits longer | |
| Cache of data blocks (`block-cache-size`) | 64 MiB | |
| Memtable (`memtable-size`) | 64 MiB, at most 1 GiB | |
