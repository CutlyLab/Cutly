// Package runtimebundle verifies a signed, complete FFmpeg/FFprobe runtime
// closure before publishing executable paths to a host.
//
// A manifest signature authenticates immutable metadata first. Resolve then
// walks one fixed bundle root, rejects links, special files and undeclared
// children, and hashes every declared regular file through an os.Root handle.
// No network lookup, PATH lookup, version-text trust or caller-supplied closure
// digest is part of this package.
package runtimebundle
