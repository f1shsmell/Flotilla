// Command flt-fake-acp-agent is the stand-in ACP agent the tests spawn. It is a
// build of internal/acp/acptest, so the fake and the tests cannot drift apart.
package main

import "github.com/f1shsmell/flotilla/internal/acp/acptest"

func main() {
	acptest.Main()
}
