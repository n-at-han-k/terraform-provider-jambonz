// Code in this file is hand-written.
//
// The acceptance-test fixtures: the values the generated tests need and the
// spec cannot supply. See schemas/acceptance.json for what they are and why
// they are not derivable.
package main

import (
	"encoding/json"
	"log"
	"os"
	"sort"
)

// Fixture is one resource's entry in schemas/acceptance.json.
type Fixture struct {
	// Prerequisites is HCL emitted above the resource under test, for the
	// records it refers to. A sip gateway has no meaning without a carrier, and
	// a test that assumed one already existed would depend on the order the
	// other tests ran in.
	Prerequisites string `json:"prerequisites"`
	// Create is the body of the resource block, required attributes only.
	Create string `json:"create"`
	// Update is the same block after a change, for the second apply.
	Update string `json:"update"`
	// Changed is what Update changed, as attribute name to expected value. The
	// test asserts these after the second apply, because an update that reports
	// success and stores nothing is a real failure mode here — every jambonz
	// update is a whole-record PUT and the read that follows it is the only
	// thing that would notice.
	Changed map[string]string `json:"changed"`
}

// ChangedKeys is Changed in a stable order. Go randomises map iteration and a
// generator whose output moves between runs fails `make verify`.
func (f Fixture) ChangedKeys() []string {
	keys := make([]string, 0, len(f.Changed))
	for k := range f.Changed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// readFixtures loads schemas/acceptance.json.
func readFixtures(path string) map[string]Fixture {
	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read fixtures: %v", err)
	}
	var all map[string]json.RawMessage
	if err := json.Unmarshal(raw, &all); err != nil {
		log.Fatalf("parse fixtures: %v", err)
	}
	out := make(map[string]Fixture, len(all))
	for name, body := range all {
		if name == "$comment" {
			continue
		}
		var f Fixture
		if err := json.Unmarshal(body, &f); err != nil {
			log.Fatalf("fixture %q: %v", name, err)
		}
		out[name] = f
	}
	return out
}
