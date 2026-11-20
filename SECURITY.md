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
| Guessing a password | PBKDF2-HMAC-SHA256 with a salt, constant time compare, the same work for a user that does not exist, a wait that doubles after each failure, the connection closes after 5 failures, a limit of failed sign ins by address (20 in a minute by default, `auth-fail-limit` and `auth-fail-window`) after which the address is refused without checking the password, and a limit on how many hashes run at the same time. Every failed sign in is in `ACL LOG` and in the log of the server. |
| A leak of the ACL file | The file is refused if group or others can read it. Plain passwords in it are hashed and reported with a warning. A plain SHA-256 hash from a Redis file (`#hash`) is an error when the file is loaded, because it is cheap to guess from. `ACL LIST` never shows a plain password. |
| A rule that does not apply | A rule for a command, a subcommand or a category that does not exist, a key pattern that is not valid and a selector that is not closed are errors when the file is loaded or when `ACL SETUSER` runs. A reload that fails changes nothing, and `ACL SETUSER` changes a user whole or not at all. |
| A user that reaches keys it should not | A user can touch only the keys that its patterns match (`~app:*`, `%R~`, `%W~`), with a selector for a second set of rules. A command that writes needs the right to write each of its keys, and any other command the right to read them, in `execute`, for each `SET` of a pipeline and for each command in `EXEC`. A user with no key pattern reaches no key, as in Redis. The error never names the key. |
| Overflow and memory attacks in the parser | Every length is checked against a limit before it allocates, and a large value is allocated in steps while its bytes arrive. Numbers are parsed with an overflow check. The depth and the number of values are limited. The reader never panics on bytes from the network, and it is fuzzed. |
| Slow clients and many clients | A limit on clients, an idle time, a time to send the rest of a command, a time to take a reply, a time for the TLS handshake, and smaller limits and a short time before sign in. |
| A reply that is much larger than the request | `MGET`, `KEYS`, `HGETALL`, `SMEMBERS`, the operations between sets, `EXEC` and each page of `SCAN` have a limit on the size of the reply, and `COUNT` of `SCAN` is capped. A connection keeps only its 32 newest `SCAN` cursors, and nothing stays open in the engine between two commands. `KEYS` is `@dangerous`. |
| A user that lists key names | `KEYS`, `SCAN` and `RANDOMKEY` return key names that the command does not take as arguments, so the server runs them only for a user that can read every key (`~*` or `%R~*`), and refuses them for a user with narrower patterns, which is stricter than the filter that Redis applies. `KEYS` is also `@dangerous`. |
| Another client that is listed or closed | `CLIENT LIST` and `CLIENT KILL` need the categories `@admin` and `@dangerous`, even for a user that can run `CLIENT`. A rule can also allow or deny one subcommand (`+client\|list`, `-client\|kill`). `SLOWLOG` keeps the name of a command and its number of arguments, never an argument. |
| A client certificate that stands for a user | With `tls-auth-clients-user yes`, a client whose certificate has the common name of a user that has the rule `certonly` is signed in as that user, after the handshake checked the chain. A `certonly` user can never sign in with a password, so a password cannot be used to take its place. Other users are not matched by a certificate. |
| Metrics that leak or are open to a network | The metrics have the name of a command and its number of keys, never a key or a value. The Prometheus endpoint (`metrics-addr`) is off by default and on loopback when it is on. An address that other machines reach is refused unless the server has TLS and `metrics-user` with `metrics-password-hash`, and the endpoint checks the password with HTTP basic authentication. It serves only `/metrics`. |
| A transaction that holds the server | A client between `MULTI` and `EXEC` holds no lock, and the queue has a limit on its commands and its bytes, and a client has a limit on the keys that it watches. `EXEC` takes the locks of its keys in one order, so two of them cannot wait for each other. `AUTH` and `HELLO` are refused inside a transaction. |
| A client that makes the server write files | `BGSAVE` writes only into `backup-dir`, which the settings name, and the name of the directory is made by the server. A client cannot choose a path. `BGSAVE` is `@admin` and `@dangerous`. The files of a backup are hard links of the table files, so they share the disk with the database and do not protect against the loss of the disk. Copy them to another machine. |
| Disk held by a long read | A snapshot or an iterator keeps the table files that it reads until it ends, so a compaction cannot free them. The server never keeps one between commands. |
| A web page that talks to the port | An inline command that starts with `POST` or `Host:` closes the connection with no reply. |
| A bug in one command | A panic ends only that connection. The client gets a short error and the details go to the log. |
| Running code or changing files through the server | There is no `EVAL`, no `MODULE`, no `DEBUG`, no `REPLICAOF`, and `CONFIG` is read only and has no file names. These are the paths of most Redis CVEs. |
| Secrets in logs and errors | The log never has keys, values or passwords. Errors to the client have no file names and no error text from the engine. |

**What it does not do yet.** These are known and are on the [roadmap](ROADMAP.md),
so a report about them is not a vulnerability, but a bypass of what is listed
above is.

- The limit of failed sign ins by address counts the address without its port, so
  clients behind one proxy or one NAT share it, and an attacker with many addresses
  is not stopped by it. The wait, the close, the limit on clients and the limit on
  running hashes bound the cost.
- Channel patterns (`&channel`) are accepted and ignored, because there are no
  channels yet.
- No encryption of the data on disk.
- A PBKDF2 hash costs CPU on purpose (about 100,000 rounds), so a sign in is slower
  than a command.

## How to run it safely

- Keep the default of `127.0.0.1` unless clients on other machines need the server.
- If they do, use TLS 1.3 (`tls-cert-file`, `tls-key-file`) and users
  (`aclfile`, with `user default off`). Add `tls-ca-cert-file` to require a client
  certificate.
- Give each client the least it needs, for example `+@read +@connection` and the
  patterns of its keys, such as `~app:*`, for a reader. A user with no key pattern
  reaches no key. Put `$pbkdf2-sha256$` hashes in the file, not plain passwords. Make one
  with `printf '%s' "$PASSWORD" | sedimentd hash-password`, and keep the file at
  mode 600.
- Keep the data directory private to the user that runs the server. The server
  makes it with mode 700.
