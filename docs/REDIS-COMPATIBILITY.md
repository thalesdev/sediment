# Compatibility with Redis

This page says how much of Redis sediment speaks, and how we measure it. The measure is
the official test suite of Redis, run against `sedimentd`. The numbers are a picture of one
day, and `scripts/redis-compat.sh` makes them again.

## The short answer

- Of the tests of 22 files of the suite that we ran, **61.7%** pass (354 of 574) with the
  harness as it is, and **71.6%** (525 of 733) with the profile that ignores `CONFIG SET`
  and the encoding of values.
- Most of what fails is a command or a feature that sediment does not have and has not
  promised: other types (lists, sorted sets, streams), `DEBUG`, `SYNC`, `MONITOR`,
  `DUMP`. About 128 of the 208 failures of the second run are of this kind.
- What is left, about 80 tests, is a command that sediment has and that answers in another
  way than Redis. This is the list of work in [What differs](#what-differs).
- If you leave out what is missing, 86.8% of the tests that use only what sediment has
  pass (525 of 605). That number is hopeful: we sorted the failures by their error message,
  and some of the 80 may be a missing feature that we did not see.
- This is **not** a percentage of Redis. The whole suite is much larger than the 22 files, and
  most of it tests things that are not in the plan for v1.0.

## How to run it

```bash
scripts/redis-compat.sh                  # the files in FILES, with the harness as it is
LENIENT=1 scripts/redis-compat.sh        # CONFIG SET, encoding and digest checks do nothing
FILES="unit/type/string unit/expire" scripts/redis-compat.sh
```

It needs `git`, `tclsh` (8.5 or newer) and `go`. It clones the Redis repository once, at the
tag `REDIS_TAG` (8.10.3 by default), starts a new `sedimentd` with an empty data directory for
each file, runs the file with the `runtest` of Redis against it (`--host`, `--port`,
`--singledb`), and prints one line for each file. The whole output of each file stays in
`OUT/<file>.out`.

The suite can run against a server that is already up, but its harness resets that server with
commands that sediment does not have (`FUNCTION FLUSH`, `SCRIPT FLUSH`, `CONFIG RESETSTAT`,
`CONFIG GET *`). The patch `scripts/redis-compat/harness.patch` removes these calls and nothing
else. It keeps `FLUSHALL`, which sediment has, so the suite resets the data as it does for
Redis.

### The two profiles

| Profile | What it changes | Why |
|---|---|---|
| default | only the patch above | The honest number. A file that starts with `CONFIG SET` stops at its first line. |
| `LENIENT=1` | `CONFIG SET` answers OK and does nothing (`lenient.patch`), and the suite runs with its own options `--ignore-encoding` and `--ignore-digest` | Many files set a configuration before they test a command. With it, the tests of `hash` and `set` run and show the behavior of the commands. A test that needs the setting to have an effect still fails. |

## Results

Redis 8.10.3, `sedimentd` at the commit that made these numbers. A test counts as passed only if the suite
prints `[ok]`.

| File | Default: ok | err | pass | Lenient: ok | err | pass |
|---|---|---|---|---|---|---|
| `unit/type/string` | 161 | 21 | 88.5% | 163 | 19 | 89.6% |
| `unit/type/incr` | 25 | 8 | 75.8% | 29 | 4 | 87.9% |
| `unit/type/hash` | 0 | 1 | 0.0% | 2 | 1 | 66.7% |
| `unit/type/set` | 0 | 0 | - | 146 | 8 | 94.8% |
| `unit/expire` | 57 | 19 | 75.0% | 57 | 19 | 75.0% |
| `unit/keyspace` | 29 | 2 | 93.5% | 39 | 8 | 83.0% |
| `unit/multi` | 4 | 49 | 7.5% | 4 | 2 | 66.7% |
| `unit/scan` | 11 | 15 | 42.3% | 13 | 13 | 50.0% |
| `unit/auth` | 0 | 0 | - | 0 | 0 | - |
| `unit/acl` | 0 | 0 | - | 0 | 0 | - |
| `unit/info` | 0 | 0 | - | 0 | 0 | - |
| `unit/info-command` | 0 | 3 | 0.0% | 0 | 3 | 0.0% |
| `unit/introspection` | 14 | 30 | 31.8% | 15 | 29 | 34.1% |
| `unit/introspection-2` | 19 | 32 | 37.3% | 19 | 32 | 37.3% |
| `unit/protocol` | 17 | 10 | 63.0% | 17 | 10 | 63.0% |
| `unit/quit` | 3 | 0 | 100.0% | 3 | 0 | 100.0% |
| `unit/slowlog` | 0 | 0 | - | 3 | 29 | 9.4% |
| `unit/limits` | 0 | 0 | - | 0 | 0 | - |
| `unit/other` | 13 | 13 | 50.0% | 14 | 12 | 53.8% |
| `unit/dump` | 0 | 15 | 0.0% | 0 | 15 | 0.0% |
| `unit/networking` | 1 | 0 | 100.0% | 1 | 0 | 100.0% |
| `unit/querybuf` | 0 | 2 | 0.0% | 0 | 4 | 0.0% |
| **total** | **354** | **220** | **61.7%** | **525** | **208** | **71.6%** |

Read the table with these four things in mind.

1. **A file stops at an error that is not an assertion.** The suite goes on after a test that
   fails an assertion, but a command that the server does not know, in the setup of a block or
   in a loop of tests, ends the rest of that block. So `unit/type/hash` shows 3 tests
   (it stops at `MEMORY USAGE`) and `unit/multi` shows 6 (it stops at `RPUSH`). Their real
   number is not known. The percentage is of the tests that ran.
2. **Files with 0 tests** (`unit/auth`, `unit/acl`, `unit/info`, `unit/limits`) have all their
   tests tagged to skip against an external server, or need `CONFIG SET` or a restart. They tell
   us nothing yet.
3. **A failure can start a chain.** A test that fails in the middle of a transaction leaves the
   connection in `MULTI`, and the next tests fail with "WATCH inside MULTI is not allowed". Most
   of the 49 failures of `unit/multi` in the default profile are of this kind.
4. **The numbers move by a few tests** between runs: some tests depend on the clock.

## What is missing on purpose, or not yet

These are the commands and features that the failures name. They are not bugs.

| What | Failing tests | Why |
|---|---|---|
| `DEBUG` (`SET-ACTIVE-EXPIRE`, `RELOAD`, `HTSTATS-KEY`, ...) | 41 | The source of many Redis CVEs. Not planned. |
| `LPUSH`, `RPUSH`, `ZADD`, `XADD` | 31 | Lists, sorted sets and streams come after v1.0. Many tests use them only to make a key of another type and test `WRONGTYPE`. |
| `DUMP`, `RESTORE`, `MIGRATE` | 14 | They need the serialization format of Redis. Not planned. |
| `SYNC`, replication streams | 8 | Replication is Raft, in milestone 3. The tests that ask for a stream of replication will not pass. |
| `CONFIG SET` and `CONFIG RESETSTAT` | 10 | `CONFIG` is read only on purpose. See [SECURITY.md](../SECURITY.md). |
| `MONITOR` | 6 | Not planned. |
| `OBJECT IDLETIME` and `FREQ` | 3 | The server does not keep the time of the last access of a key, and it says so in the error. It does not make up a number. |
| `MEMORY USAGE`, `SAVE`, `CLIENT NO-TOUCH`, `CLIENT REPLY`, `CLIENT TRACKING`, `CLIENT CACHING` | 14 | Not done yet. |
| `EVAL`, `FUNCTION`, `SUBSCRIBE` | 3 | No scripts and no pub/sub. Not planned for v1.0. |

## What differs

These are commands that sediment has and that answer in another way than Redis. This is where a
client can notice. Each line is a failing test of the suite.

**Deliberate.**
- `SLOWLOG GET` shows the name of the command and the number of its arguments, never the
  arguments, because an argument can be a key, a value or a password. The tests of
  `slowlog` that look at the arguments, and at `slowlog-entry-max-argc` and
  `slowlog-entry-max-string-len`, fail for this reason (about 12 tests).
- `KEYS` with 50000 repetitions of `*?` returns the key that matches. Redis stops its glob at a
  depth of 1000 and returns nothing. Ours is iterative and its time is bounded, so we keep the
  right answer.
- Commands are limited to 1024 arguments by default (`proto-max-multibulk-len` is the limit in
  Redis, and its default is much larger). The `HSCAN` and `ZSCAN` tests that send tens of
  thousands of arguments get a protocol error.
- `FLUSHALL` needs the right to write every key, and `KEYS`, `SCAN` and `RANDOMKEY` need the
  right to read every key, when the server has users. Redis filters instead.

**To fix.** These are the next quick wins.

| What | Redis does | sediment does |
|---|---|---|
| `EXPIRE key <big negative number>` | `ERR invalid expire time in 'expire' command` | answers 1 |
| `INCRBYFLOAT key inf` (or `nan`) | `ERR increment would produce NaN or Infinity` | `ERR value is not a valid float` |
| `COMMAND LIST FILTERBY ...` | filters by module, ACL category and pattern, and refuses a bad option | answers every command, whatever the option |
| `COMMAND INFO` for a command with a count of keys | the flag `movablekeys` | has it for the commands we made with `KeyFunc` |
| `CLIENT LIST` and `CLIENT INFO` | many more fields (`db`, `sub`, `psub`, `multi`, `qbuf`, `tot-mem`, `events`, `tot-net-in`, ...) | the fields that we keep |
| `CLIENT KILL` with no filter | `wrong number of arguments for 'client|kill'` | `ERR syntax error` |
| `HSCAN ... NOVALUES` | only the fields | `ERR syntax error` |
| `*-10` (a negative length of a request) | ignores it and runs the next command | a protocol error |
| `INFO memory` and `INFO commandstats` | the sections exist | the sections do not exist |
| `ACL SETUSER` on a server that has no ACL file | makes the user in memory | `ERR this server has no ACL file`, because `ACL SAVE` and `ACL LOAD` need the file |

## What is not measured

The suite has files that we did not run: `unit/type/list*`, `unit/type/zset`, `unit/type/stream*`,
`unit/pubsub*`, `unit/scripting`, `unit/functions`, `unit/geo`, `unit/hyperloglog`, `unit/bitops`,
`unit/sort`, `unit/tracking`, `unit/maxmemory`, `unit/cluster`, the tests of replication and
of the modules. We leave them out because sediment has none of these types or features, and a
file would only show a column of "unknown command".

`unit/bitops` is the one to run next: `SETBIT`, `GETBIT` and `LCS` exist now, and `BITCOUNT`,
`BITPOS` and `BITOP` are the commands that are left.

## How the commands were chosen

After the first run, the commands that the failures named were ranked by the number of
tests that each would unlock, and the ones that sediment could do in a short time on its types were
made: `FLUSHALL`, `FLUSHDB`, `DIGEST`, `DELEX`, `MSETEX`, `SETBIT`, `GETBIT`, `LCS`, `SUBSTR`, the
conditions `IFEQ`, `IFNE`, `IFDEQ` and `IFDNE` of `SET`, `SMOVE`, `SINTERCARD`, `SUNIONCARD`,
`SDIFFCARD`, `COPY`, `TOUCH`, `TIME`, `RESET`, `OBJECT` and `COMMAND GETKEYS`. The suite also found a
bug: `SRANDMEMBER` with the smallest count panicked. Before and after, with the profile that
ignores `CONFIG SET` and the encoding:

| File | Before | After |
|---|---|---|
| `unit/type/string` | 33.5% | 89.6% |
| `unit/type/set` | 27.3% | 94.8% |
| `unit/keyspace` | 63.8% | 83.0% |
| `unit/type/incr` | 72.7% | 87.9% |
| all the files above | 39.7% | 71.6% |
