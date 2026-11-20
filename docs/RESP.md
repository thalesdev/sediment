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
| `SET` | [x] | `NX`, `XX`, `IFEQ`, `IFNE`, `IFDEQ`, `IFDNE`, `GET`, `EX`, `PX`, `EXAT`, `PXAT` and `KEEPTTL`. A plain `SET` removes the TTL. |
| `DEL` | [x] | Many keys are one atomic write. |
| `DIGEST` | [x] | The XXH3 64-bit hash of a string as 16 hex digits, as Redis 8.4. |
| `DELEX` | [x] | `DEL` with `IFEQ`, `IFNE`, `IFDEQ` and `IFDNE`. Replies 1 or 0. |
| `MSETEX` | [x] | Many keys with one expiry in one write, with `NX`, `XX` or `KEEPTTL`. Replies 1 or 0. |
| `SETBIT`, `GETBIT` | [x] | Offsets up to 2^32 - 1, within the limit of the size of a string. |
| `LCS` | [x] | `LEN`, `IDX`, `MINMATCHLEN` and `WITHMATCHLEN`. A table of more than 16 million cells is refused. |
| `SUBSTR` | [x] | The same as `GETRANGE`. |
| `COPY` | [x] | Any type, with its expiry. `REPLACE`. `DB` accepts only 0. A collection of up to a million elements. |
| `TOUCH`, `TIME` | [x] | `TOUCH` counts the keys that exist. |
| `RESET` | [x] | Drops the transaction, the watched keys, the name and the protocol, and signs the client out. |
| `OBJECT` | [x] | `ENCODING`, `REFCOUNT` and `HELP`. `FREQ` and `IDLETIME` answer an error, because the server does not track them. |
| `FLUSHALL`, `FLUSHDB` | [x] | The same command, because there is only database 0. `ASYNC` and `SYNC` are accepted and do nothing. It is one atomic write with a range delete for each class of key, and it keeps the format record. It takes every lock, makes every `WATCH` fail, and needs `@dangerous` and the right to write every key (`~*`). It is refused inside `MULTI`. |
| `EXISTS` | [x] | |
| `MGET` | [x] | The reply has a size limit. |
| `MSET` | [x] | One atomic write. |
| `PING`, `ECHO`, `QUIT` | [x] | `PING` answers `-MISCONF` if the storage stopped after an error, and `-LOADING` while the node opens its data. `sedimentd healthcheck` uses it. |
| `SELECT` | [x] | Only database 0. |
| `HELLO` | [x] | `AUTH` and `SETNAME` options. |
| `AUTH` | [x] | `AUTH password` and `AUTH user password`. |
| `CLIENT` | [x] | `ID`, `SETNAME`, `GETNAME`, `SETINFO`, `INFO`, `LIST` (with `ID` and `TYPE normal`) and `KILL` (the old form with an address, and `ID`, `ADDR`, `LADDR`, `USER`, `SKIPME`). `LIST` and `KILL` need `@admin` and `@dangerous`. |
| `CLIENT PAUSE`, `TRACKING` | [ ] | |
| `SLOWLOG` | [x] | `GET [count]`, `LEN`, `RESET`. An entry has the name of the command and the number of its arguments, never an argument. See `slowlog-log-slower-than` and `slowlog-max-len`. |
| `COMMAND` | [x] | `COUNT`, `LIST`, `INFO`, `DOCS` (empty), `GETKEYS` and `GETKEYSANDFLAGS`. |
| `INFO` | [x] | Sections `server` (with `loading` and `storage`), `clients`, `stats`, `cpu`, `persistence`, `shale`, `runtime`. While the node opens its data, `INFO` shows `loading:1` and every other command gets a `LOADING` error. |
| `CONFIG GET` | [x] | Read only, no file names. |
| `CONFIG SET`, `REWRITE` | [ ] | On purpose. See [SECURITY.md](../SECURITY.md). |
| `ACL WHOAMI`, `USERS`, `LIST`, `LOAD`, `CAT`, `SETUSER`, `DELUSER`, `GETUSER`, `SAVE`, `LOG` | [x] | Only for users with `@admin`. `SETUSER` changes a user whole or not at all. `DELUSER` never removes `default`. `SAVE` writes the file with mode 600. `LOG` keeps 128 entries and never a key. A change of the users closes the connections of a user that is gone or off. |
| `INCR`, `DECR`, `INCRBY`, `DECRBY` | [x] | Atomic. Overflow is an error. |
| `APPEND`, `STRLEN` | [x] | A string is limited to 16 MiB. |
| `SETNX`, `GETSET`, `GETDEL` | [x] | Atomic. |
| `SETEX`, `PSETEX`, `MSETNX`, `GETEX` | [x] | `MSETNX` sets all the keys or none. `GETEX` takes `EX`, `PX`, `EXAT`, `PXAT` and `PERSIST`. |
| `SETRANGE`, `GETRANGE`, `INCRBYFLOAT` | [x] | `SETRANGE` pads with zero bytes, and a string is limited to 16 MiB. |
| `UNLINK`, `RENAME`, `RENAMENX` | [x] | `UNLINK` is `DEL`. `RENAME` keeps the time of the key, and it is one write. |
| `EXPIRE`, `PEXPIRE`, `EXPIREAT`, `PEXPIREAT` | [x] | With `NX`, `XX`, `GT` and `LT`. A time in the past deletes the key. |
| `TTL`, `PTTL`, `EXPIRETIME`, `PEXPIRETIME`, `PERSIST` | [x] | |
| `SCAN` | [x] | `MATCH`, `COUNT` and `TYPE`. A call looks at up to `COUNT` keys, and `COUNT` is at most 10000. A cursor resumes after the last key that the call looked at, it belongs to the connection, and a connection keeps its 32 newest cursors. An old or foreign cursor is `ERR invalid cursor`. A key that stays in the database for the whole scan is returned. Expired keys are hidden. |
| `KEYS` | [x] | `@dangerous` and slow: it reads every key that the pattern can match. The reply has a size limit. |
| `DBSIZE` | [x] | Slow: it counts the live keys, so it reads all of them. |
| `RANDOMKEY` | [x] | The first live key after a random point, so a key after a large gap is more likely. |
| `TYPE` | [x] | `string`, `hash`, `set` or `none`. |
| `MULTI`, `EXEC`, `DISCARD`, `WATCH`, `UNWATCH` | [x] | `EXEC` runs the queued commands in order and locks their keys, so no other client writes them in between. An error while a command is queued, a command that is not allowed, or a full queue makes `EXEC` reply `EXECABORT`. A command that fails at `EXEC` does not stop the others. A failed `WATCH` gives a null reply. `AUTH` and `HELLO` are refused after `MULTI`. A reader that takes no lock can see a transaction half applied. There are limits on the commands, the bytes and the watched keys. |
| `BGSAVE`, `LASTSAVE` | [x] | `BGSAVE` makes a copy of the data in a new directory of `backup-dir`, one at a time. The files are hard links. `@admin` and `@dangerous`. |
| `SUBSCRIBE`, `PUBLISH` | [ ] | After v1.0. |
| `HSET`, `HMSET`, `HSETNX`, `HGET`, `HMGET`, `HDEL`, `HEXISTS`, `HLEN`, `HSTRLEN` | [x] | A hash with no field goes away. Each write is one batch. |
| `HINCRBY`, `HINCRBYFLOAT` | [x] | |
| `HGETALL`, `HKEYS`, `HVALS` | [x] | One snapshot. The reply is a map in RESP3, and it has a size limit. The fields come in byte order. |
| `HSCAN` | [x] | `MATCH` and `COUNT`. A scan ends if the hash is deleted and made again. |
| `SADD`, `SREM`, `SPOP`, `SISMEMBER`, `SMISMEMBER`, `SCARD`, `SMEMBERS`, `SRANDMEMBER`, `SSCAN` | [x] | A set with no member goes away. `SMEMBERS` is a set in RESP3. `SPOP` and `SRANDMEMBER` read the set in one pass, and `SRANDMEMBER` with a negative count reads sets of up to 100000 members. |
| `SMOVE` | [x] | One atomic write. A source that becomes empty goes away. |
| `SINTERCARD`, `SUNIONCARD`, `SDIFFCARD` | [x] | `LIMIT` stops the count. `APPROX` of `SUNIONCARD` is accepted and does nothing. |
| `SUNION`, `SINTER`, `SDIFF`, `SUNIONSTORE`, `SINTERSTORE`, `SDIFFSTORE` | [x] | All the sets are read as of one snapshot. The `STORE` versions replace the destination in one write. |
| Lists, sorted sets, streams | [ ] | After v1.0. |
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
| Key patterns (`~prefix:*`, `%R~`, `%W~`, `%RW~`, `resetkeys`) | [x] A user with no pattern reaches no key. `KEYS`, `SCAN` and `RANDOMKEY` need the right to read every key. |
| Rules for subcommands (`+config\|get`, `-client\|kill`) | [x] An unknown subcommand is an error. |
| Selectors `(~app:* +get)` and `clearselectors` | [x] A selector is a second set of rules, and a command is allowed if any set allows it. |
| `ACL SETUSER`, `DELUSER`, `GETUSER`, `SAVE`, `LOG` at run time | [x] |
| Channel patterns | [ ] Accepted, there are no channels yet. |
| User from the CN of a client certificate (`certonly`, `tls-auth-clients-user yes`) | [x] Only for a user with the rule `certonly`, which can not sign in with a password. |

## Limits

All of them have a setting in the file of `sedimentd`. A client that has not signed
in gets the smaller pre-sign-in limits.

| Limit | Default | Before sign in |
|---|---|---|
| Bulk string | 16 MiB | 1 KiB |
| Arguments of a command | 1024 | 8 |
| Bytes of one command | 32 MiB | 4 KiB |
| Inline line | 64 KiB | 2 KiB |
| Key, hash field and set member | 64 KiB | |
| Reply of `MGET`, `KEYS`, `HGETALL`, `SMEMBERS`, a page of `SCAN` and `EXEC` | 64 MiB | |
| `COUNT` of `SCAN` and `HSCAN` | 10000 | |
| Commands in a transaction | 10000 | |
| Bytes in a transaction | 32 MiB | |
| Watched keys of a client | 10000 | |
| Nesting of a value | 32 | 4 |
| Clients | 10000 | |
| Idle client | 10 min | 30 s |
| Time to send the rest of a command | 30 s | |
| Time to take a reply | 30 s | |
| TLS handshake | 10 s | |
| Failed sign ins before the close | 5, each one waits longer | |
| Failed sign ins of one address (`auth-fail-limit`, `auth-fail-window`) | 20 in 60 s, then the address is refused | |
| Cache of data blocks (`block-cache-size`) | 64 MiB | |
| Memtable (`memtable-size`) | 64 MiB, at most 1 GiB | |
