package version

import "testing"

func TestString(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		preRelease string
		channel    string
		want       string
	}{
		{name: "a stable build has no suffix", version: "0.1.0", want: "0.1.0"},
		{name: "a preview build names its pre-release", version: "0.1.0", preRelease: "preview.1", channel: "preview", want: "0.1.0-preview.1"},
		// The channel names an install; it must not be pasted onto the version.
		// A preview build that carries no pre-release is a mistake, and the
		// version string has to say so rather than invent a suffix.
		{name: "the channel alone adds nothing", version: "0.1.0", channel: "preview", want: "0.1.0"},
		{name: "a source build keeps its placeholder", version: "0.0.0-dev", want: "0.0.0-dev"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version, PreRelease, Channel = tt.version, tt.preRelease, tt.channel
			t.Cleanup(func() { Version, PreRelease, Channel = "0.0.0-dev", "", "stable" })

			if got := String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
