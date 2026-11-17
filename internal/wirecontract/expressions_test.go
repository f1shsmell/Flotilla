package wirecontract

import (
	"os"
	"strings"
	"testing"
)

// TestExpressionsReachTheContract pins the constants that are written as
// expressions. A client mirrors a frame limit or an error code as a number, and
// the parser used to drop every one that was not a bare literal — which silently
// removed the whole error-code table, the very thing the package exists to
// publish.
func TestExpressionsReachTheContract(t *testing.T) {
	c, err := Build("../..")
	if err != nil {
		t.Fatalf("build the contract: %v", err)
	}

	for key, want := range map[string]string{
		// A negative code: an ast.UnaryExpr, not a BasicLit.
		"ipc.CodeMethodFailed":   "-32000",
		"ipc.CodeUnknownSession": "-32003",
		// A shift: an ast.BinaryExpr.
		"ipc.MaxFrameSize": "1048576",
		// A plain literal, for the contrast that makes the two above meaningful.
		"ipc.HeaderSize": "9",
	} {
		got, ok := c.Constants[key]
		if !ok {
			t.Errorf("%s is missing from the contract", key)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// TestTheGeneratedFileCarriesTheErrorCodes pins the same fact at the file the GUI
// compiles: the codes are what a window branches on, so a generated module that
// lacks them would leave every failure a string again. The assertion reads the
// file on disk, because that is the copy the renderer imports — reading what the
// generator would have produced would pass while the committed file is stale.
func TestTheGeneratedFileCarriesTheErrorCodes(t *testing.T) {
	payload, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}
	body := string(payload)

	for _, want := range []string{
		"CodeUnknownSession",
		"CodeUnknownMethod",
		"CodeAlreadyRunning",
		"MaxFrameSize",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the generated contract does not carry %s", want)
		}
	}
}
