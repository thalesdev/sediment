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
- a way to read or write files outside of the database directory.

Once the server exists, the same applies to the network protocol: a crash, a hang
or a memory problem from a malformed request.

## What does not count yet

The server, authentication and encryption do not exist yet, so reports about
them are out of scope. They are on the [roadmap](ROADMAP.md).
