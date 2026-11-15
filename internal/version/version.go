// Package version carries the build's version and release channel.
package version

// Both are overridden at build time with -ldflags "-X ...".
var (
	// Version is the semantic version of this build, without a channel suffix.
	Version = "0.0.0-dev"
	// Channel is "stable" or "preview"; it names the install a binary belongs to.
	Channel = "stable"
)

// String renders the build identity for `--version` output and logs.
func String() string {
	if Channel == "" || Channel == "stable" {
		return Version
	}
	return Version + "-" + Channel
}
