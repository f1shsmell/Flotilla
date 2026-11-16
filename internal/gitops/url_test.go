package gitops

import "testing"

// TestFirstURLExtractsThePullRequestAddress pins the parsing behind a pull
// request's handle. gh prints progress before the URL, so taking the first line
// would report the wrong thing.
func TestFirstURLExtractsThePullRequestAddress(t *testing.T) {
	output := "Creating pull request for feature in f1shsmell/Flotilla\n\nhttps://github.com/f1shsmell/Flotilla/pull/12\n"

	if got := firstURL(output); got != "https://github.com/f1shsmell/Flotilla/pull/12" {
		t.Fatalf("firstURL = %q, want the pull request address", got)
	}
}

// TestFirstURLInventsNothing pins the fallback: output with no address yields
// no address, so a caller does not present a guess as a link.
func TestFirstURLInventsNothing(t *testing.T) {
	if got := firstURL("no address here"); got != "" {
		t.Fatalf("firstURL = %q, want empty", got)
	}
}
