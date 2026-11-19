package ipc

// Capability names one behaviour a peer understands. It is the gate between
// builds, in place of the protocol version: a version that did not match used
// to reject the connection outright, which left no room for a stable window and
// a preview daemon to live on one machine. A peer that does not name a
// capability simply does not receive what it cannot read, and stays connected.
//
// Each entry records when it was added and what removes it, so the set stays a
// list of live differences rather than a graveyard. The format is the one
// refs/paseo uses in packages/protocol/src/client-capabilities.ts.
type Capability string

const (
	// CapabilityDiagnostics says the peer understands the extra diagnostic
	// fields on a status report. The daemon omits them for a peer that does
	// not, so a window older than those fields still reads the fleet it asked
	// for. Diagnostics are the right first choice because nothing branches on
	// them: a missing failures list is a quieter report, not a wrong one.
	//
	// COMPAT(CapabilityDiagnostics): added 2026-11-19. Remove the gate once no
	// supported window fails to name it; the field then always travels.
	CapabilityDiagnostics Capability = "diagnostics_v1"
)

// Supported is what this build understands for itself. A client that speaks a
// newer dialect than this list still connects: the peer trims what it sends
// rather than refusing the connection.
func Supported() []Capability {
	return []Capability{
		CapabilityDiagnostics,
	}
}

// Has reports whether caps names want. An empty set is a peer that named none,
// which is what an older build looks like on the wire.
func Has(caps []Capability, want Capability) bool {
	for _, one := range caps {
		if one == want {
			return true
		}
	}
	return false
}
