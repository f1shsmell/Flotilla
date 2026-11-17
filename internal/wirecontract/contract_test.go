package wirecontract

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// contractPath is the generated module the GUI compiles. It sits beside the
// Electron main process, which is where the wire is spoken from.
var contractPath = filepath.Join("..", "..", "gui", "electron", "contract.ts")

// TestTheGeneratedContractIsCurrent is the gate the whole package exists for: a
// wire type, a method name or a constant that changed without regenerating
// contract.ts fails here, before a renderer reads a field that no longer exists.
func TestTheGeneratedContractIsCurrent(t *testing.T) {
	_, same, err := Check(filepath.Join("..", ".."), contractPath)
	if err != nil {
		t.Fatalf("check the contract: %v", err)
	}
	if !same {
		t.Fatalf("%s is stale: run 'go run ./cmd/flt-contract -out gui/electron/contract.ts'", contractPath)
	}
}

// TestEveryWireTypeIsRendered pins the other direction: a type named in
// WireTypes must reach the file. A typo in the list would otherwise leave the
// GUI reading a shape the contract never mentions.
func TestEveryWireTypeIsRendered(t *testing.T) {
	root := filepath.Join("..", "..")

	c, err := Build(root)
	if err != nil {
		t.Fatalf("build the contract: %v", err)
	}

	rendered := make(map[string]bool, len(c.Types))
	for _, typ := range c.Types {
		rendered[typ.Key] = true
	}

	for _, key := range WireTypes {
		if !rendered[key] {
			t.Errorf("%s is listed as a wire type but no struct in the source matches it", key)
		}
	}
}

// TestTheMethodsAreTheOnesTheDaemonServes pins the method table against the
// calls a client makes: every name a window can send has a constant behind it,
// so a rename cannot leave a bare string behind.
func TestTheMethodsAreTheOnesTheDaemonServes(t *testing.T) {
	root := filepath.Join("..", "..")

	c, err := Build(root)
	if err != nil {
		t.Fatalf("build the contract: %v", err)
	}

	for _, want := range []string{
		"session.attach",
		"session.prompt",
		"session.seen",
		"session.stop",
		"daemon.status",
		"daemon.agents",
		"session.start",
	} {
		found := false
		for _, value := range c.Methods {
			if value == want {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no constant holds the method %q", want)
		}
	}
}

// TestTheGeneratedFileSaysItIsGenerated pins the header. A file that a person
// may edit has to say so; this one must not look like it.
func TestTheGeneratedFileSaysItIsGenerated(t *testing.T) {
	payload, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read the contract: %v", err)
	}

	if !strings.Contains(string(payload), "DO NOT EDIT") {
		t.Fatal("the generated contract does not say it is generated")
	}
}
