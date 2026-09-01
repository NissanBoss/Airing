package main

// The test that runs against a real repository, when there is one to hand.
//
// The repositories the other tests build are loose objects only, because
// that is what can be written by hand. A packfile with delta chains in it
// is a different thing to read and the only honest way to check the reader
// is to point it at one somebody's git actually wrote. Set AIRING_REPO to
// a clone and this runs; the CI does exactly that with this repository.

import (
	"os"
	"testing"
)

// Every object has to rebuild and hash back to its own name. If anything
// in the pack reader or the delta code is wrong this fails, because the
// name of an object is the hash of the finished object and nothing else.
func TestEveryObjectHashesBack(t *testing.T) {
	where := os.Getenv("AIRING_REPO")
	if where == "" {
		t.Skip("set AIRING_REPO to a real clone to run this")
	}
	r, err := OpenRepo(where)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	all := r.Store.Everything()
	if len(all) == 0 {
		t.Fatal("no objects at all, so this proved nothing")
	}
	t.Logf("%d objects", len(all))

	kinds := map[Kind]int{}
	bad := 0
	for _, h := range all {
		o, err := r.Store.Read(h)
		if err != nil {
			t.Errorf("%s: %v", h.Short(), err)
			if bad++; bad > 5 {
				t.Fatal("too many to be worth going on")
			}
			continue
		}
		kinds[o.Kind]++
		if !o.Verify() {
			t.Errorf("%s rebuilt into something else", h.Short())
			if bad++; bad > 5 {
				t.Fatal("too many to be worth going on")
			}
		}
	}
	for kind, n := range kinds {
		t.Logf("  %-8s %d", kind, n)
	}

	// A clone straight off a server has nothing unreachable in it. This is
	// the check that found the tree mode bug: the walk was skipping every
	// subdirectory, so a fifth of the objects came out as reachable from
	// nothing and a fresh clone cannot have any.
	a := inspect(r)
	if len(a.History.Loose) > 0 {
		t.Errorf("%d objects came out reachable from nothing, and a clone has none",
			len(a.History.Loose))
	}
}
