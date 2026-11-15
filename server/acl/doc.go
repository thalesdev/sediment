// SPDX-License-Identifier: MIT

// Package acl holds the users of the server, their passwords and the commands
// that each one can run. The users come from a file in the line format of the
// Redis ACL file, for example:
//
//	user default off
//	user app on #<sha256> ~* +@read +@write -@dangerous
//	user admin on $pbkdf2-sha256$... ~* +@all
//
// The package is made to fail closed. A rule that it does not enforce yet, such
// as a key pattern other than ~*, is an error when the file is loaded, never a
// rule that is silently ignored. A command that the server does not have is an
// error too, so a typo in a deny rule cannot grant a command.
package acl
