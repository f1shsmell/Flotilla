package main

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/f1shsmell/flotilla/internal/adapters"
)

// parseAdapter reads the operational knowledge the daemon passed on the command
// line. An empty flag means the agent declared none, which is the cautious path
// and not an error.
//
// The flag is decoded strictly: this process is the one that acts on the
// knowledge, and a field it silently skipped would be a pattern the agent's
// owner believed was active.
func parseAdapter(encoded string) (adapters.Adapter, error) {
	if encoded == "" {
		return adapters.Adapter{}, nil
	}

	var adapter adapters.Adapter
	dec := json.NewDecoder(bytes.NewReader([]byte(encoded)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&adapter); err != nil {
		return adapters.Adapter{}, fmt.Errorf("decode --adapter: %w", err)
	}
	if err := adapter.Validate(); err != nil {
		return adapters.Adapter{}, fmt.Errorf("--adapter: %w", err)
	}
	return adapter, nil
}
