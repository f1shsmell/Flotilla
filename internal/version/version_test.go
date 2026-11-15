package version

import "testing"

func TestString(t *testing.T) {
	tests := []struct {
		name    string
		version string
		channel string
		want    string
	}{
		{name: "stable carries no suffix", version: "0.1.0", channel: "stable", want: "0.1.0"},
		{name: "empty channel reads as stable", version: "0.1.0", channel: "", want: "0.1.0"},
		{name: "preview names its channel", version: "0.1.0", channel: "preview", want: "0.1.0-preview"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Version, Channel = tt.version, tt.channel
			t.Cleanup(func() { Version, Channel = "0.0.0-dev", "stable" })

			if got := String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}
