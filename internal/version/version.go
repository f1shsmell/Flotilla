// Package version carries the build's version and release channel.
package version

// All three are overridden at build time with -ldflags "-X ...".
var (
	// Version is the semantic version of this build, without a pre-release
	// suffix. A source build keeps the development placeholder.
	Version = "0.0.0-dev"
	// PreRelease is the suffix a preview build carries, such as "preview.1".
	// Empty means the version stands alone: a stable build.
	PreRelease = ""
	// Channel is "stable" or "preview"; it names the install a binary belongs
	// to. It selects the state root and the pipe namespace, and it is not part
	// of the version string: a preview build's number is already 0.1.0-preview.1.
	Channel = "stable"
)

// String renders the build identity for `--version` output and logs.
func String() string {
	if PreRelease == "" {
		return Version
	}
	return Version + "-" + PreRelease
}
