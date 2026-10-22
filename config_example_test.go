package multislow

import "testing"

// The shipped example must always load and validate.
func TestExampleConfigLoads(t *testing.T) {
	c, err := Load("config.example.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Entries) != MaxEntries {
		t.Errorf("example has %d entries, want %d", len(c.Entries), MaxEntries)
	}
}
