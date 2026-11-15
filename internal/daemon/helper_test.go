package daemon

import (
	"bufio"
	"fmt"
	"os"
	"testing"
)

// helperEnv marks the helper process, which stands in for an agent CLI so the
// two-process test needs nothing installed on the machine.
const helperEnv = "FLT_TEST_HELPER_ECHO"

// TestHelperEchoAgent is the body of the fake agent the PTY tests spawn.
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
