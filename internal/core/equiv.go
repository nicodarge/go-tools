//go:build equiv

package core

// SetMemberCap is the equivalence build's way to shrink the packument member cap. Never part of
// the shipped build.
func SetMemberCap(chars int64) { maxMemberChars = chars }
