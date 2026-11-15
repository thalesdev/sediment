# Security policy

## Supported versions

sediment is pre-alpha. Only the latest commit on `main` is supported, and no
release is stable yet.

## Reporting a vulnerability

Please report privately and do not open a public issue.

Use the [private vulnerability report](https://github.com/thalesdev/sediment/security/advisories/new)
of this repository. Include what you found, how to reproduce it, and the commit
you tested.

You can expect an answer within a few days. This is a project of one maintainer,
so there is no fixed time for a fix, but a confirmed report is the first priority.

## What counts

Because this is a database, these count as security problems:

- a write that had returned, and that is lost after a crash or a restart;
- a database that becomes unreadable, or that returns data that was never written,
  after a crash or from damaged files;
- a panic, a hang or a very large memory use that a damaged or hostile file can
  cause (data from disk is not trusted);
- a way to read or write files outside of the database directory;
- a crash, a hang, a very large memory or CPU use, or a leak between clients, that
  a malformed or hostile request on the network can cause;
- a way to run a command that the user is not allowed to run, to read or change data
  without signing in when the server requires it, or to learn a password, a hash or
  a file name from the server;
- a way to downgrade or to skip TLS, or to skip the check of a client certificate.

## Threat model of the server

The server is the first part of the project that a stranger can reach, so it is
built on three rules: it is closed by default, every size has a limit before it
costs anything, and what it does not enforce yet is refused and not ignored.

**Who the attacker is.** Anyone who can open a TCP connection to the port, with or
without an account, and anyone who can make a web page or a proxy send bytes to
the port. An attacker who already runs code on the machine, or who can read the
data directory or the ACL file, is out of scope.

**What the server does about it.**

| Threat | What the server does |
|---|---|
| Open to the internet with no sign in | It listens on `127.0.0.1` by default. On an address that other machines reach, it starts only with TLS or with a sign in, or with `insecure-bind yes`. |
| Traffic read or changed on the network | TLS 1.3 only, with an optional client certificate (mTLS). The certificate is read again on `SIGHUP`. |
| Guessing a password | PBKDF2-HMAC-SHA256 with a salt, constant time compare, the same work for a user that does not exist, a wait that doubles after each failure, the connection closes after 5 failures, and a limit on how many hashes run at the same time. |
| A leak of the ACL file | The file is refused if group or others can read it. Plain passwords in it are hashed and reported with a warning. A plain SHA-256 hash from a Redis file (`#hash`) is an error when the file is loaded, because it is cheap to guess from. `ACL LIST` never shows a plain password. |
| A rule that does not apply | A key pattern other than `~*`, a rule for a subcommand and a command that does not exist are errors when the file is loaded. A reload that fails changes nothing. |
| Overflow and memory attacks in the parser | Every length is checked against a limit before it allocates, and a large value is allocated in steps while its bytes arrive. Numbers are parsed with an overflow check. The depth and the number of values are limited. The reader never panics on bytes from the network, and it is fuzzed. |
| Slow clients and many clients | A limit on clients, an idle time, a time to send the rest of a command, a time to take a reply, a time for the TLS handshake, and smaller limits and a short time before sign in. |
| A reply that is much larger than the request | `MGET`, `KEYS` and each page of `SCAN` have a limit on the size of the reply, and `COUNT` of `SCAN` is capped. A connection keeps only its 32 newest `SCAN` cursors, and nothing stays open in the engine between two commands. `KEYS` is `@dangerous`. |
| A user that lists key names | `KEYS` and `SCAN` return every key name to a user with `@read`, because key patterns in the ACL do not exist yet. Keep `@dangerous` and `@keyspace` from users that must not see the names. |
| Disk held by a long read | A snapshot or an iterator keeps the table files that it reads until it ends, so a compaction cannot free them. The server never keeps one between commands. |
| A web page that talks to the port | An inline command that starts with `POST` or `Host:` closes the connection with no reply. |
| A bug in one command | A panic ends only that connection. The client gets a short error and the details go to the log. |
| Running code or changing files through the server | There is no `EVAL`, no `MODULE`, no `DEBUG`, no `REPLICAOF`, and `CONFIG` is read only and has no file names. These are the paths of most Redis CVEs. |
| Secrets in logs and errors | The log never has keys, values or passwords. Errors to the client have no file names and no error text from the engine. |

**What it does not do yet.** These are known and are on the [roadmap](ROADMAP.md),
so a report about them is not a vulnerability, but a bypass of what is listed
above is.

- No limit of sign in attempts per address. The wait and the close are per
  connection, so a client that opens many connections can try more passwords. The
  limit on clients and on running hashes bounds the cost.
- No key patterns, no run-time `ACL SETUSER`, no log of denied commands (ACL phase 2).
- A user cannot be taken from the name of a client certificate.
- A user that is deleted or turned off by a reload loses its permissions at once,
  but its open connections stay open.
- No encryption of the data on disk.
- A PBKDF2 hash costs CPU on purpose (about 100,000 rounds), so a sign in is slower
  than a command.

## How to run it safely

- Keep the default of `127.0.0.1` unless clients on other machines need the server.
- If they do, use TLS 1.3 (`tls-cert-file`, `tls-key-file`) and users
  (`aclfile`, with `user default off`). Add `tls-ca-cert-file` to require a client
  certificate.
- Give each client the least it needs, for example `+@read +@connection` for a
  reader. Put `$pbkdf2-sha256$` hashes in the file, not plain passwords. Make one
  with `printf '%s' "$PASSWORD" | sedimentd hash-password`, and keep the file at
  mode 600.
- Keep the data directory private to the user that runs the server. The server
  makes it with mode 700.
