package host

import (
	"bufio"
	"fmt"
	"os"
	"testing"
)

// helperEnv marks the helper process: the test binary re-executes itself as a
// stand-in agent, so no external CLI is needed for a PTY test.
const helperEnv = "FLT_TEST_HELPER_ECHO"

// TestHelperEchoAgent is not a test in the ordinary sense: it is the body of
// the fake agent the PTY tests spawn.
func TestHelperEchoAgent(t *testing.T) {
	if os.Getenv(helperEnv) != "1" {
		t.Skip("helper process only")
	}

	fmt.Printf("ready pid=%d\n", os.Getpid())

	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		fmt.Printf("echo: %s\n", scanner.Text())
	}
}
