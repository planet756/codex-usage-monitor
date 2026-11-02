package ipc

import "testing"

func TestFreshNativeQueriesCannotResolveAmbiguityFromPreviousRows(t *testing.T) {
	for range 3 {
		var candidates ViewCandidates
		candidates.Add(Selection{ID: "a", Host: "local"})
		candidates.Add(Selection{ID: "b", Host: "local"})
		if s, ambiguous := candidates.Result(true); !ambiguous || s.ID != "" {
			t.Fatal("ambiguous native rows became a verified chat")
		}
	}
	var candidates ViewCandidates
	candidates.Add(Selection{ID: "b", Host: "local"})
	candidates.Add(Selection{ID: "b", Host: "local"})
	if s, ambiguous := candidates.Result(true); ambiguous || s.ID != "b" {
		t.Fatal("unique chat did not recover")
	}
	if s, ambiguous := candidates.Result(false); !ambiguous || s.ID != "" {
		t.Fatal("truncated native results established uniqueness")
	}
	candidates.Add(Selection{ID: "b", Host: "remote"})
	if s, ambiguous := candidates.Result(true); !ambiguous || s.ID != "" {
		t.Fatal("same ID on different hosts was accepted")
	}
}
