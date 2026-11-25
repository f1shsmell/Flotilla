package agent

// RegistryEntry is one agent the published ACP Registry offers that this build
// does not otherwise know: enough for a list to show what exists, and the
// recipe that would install it. It is not a preset: no switch, no start.
type RegistryEntry struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Description string `json:"description,omitempty"`
	Repository  string `json:"repository,omitempty"`
	License     string `json:"license,omitempty"`
	Recipe      string `json:"recipe"`
}
