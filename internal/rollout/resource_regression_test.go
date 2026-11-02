package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type appendDuringProof struct {
	f       *os.File
	path    string
	appends int
}

func TestMalformedCarrierCannotBecomeAnExactHistoryWitness(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	bad := strings.Replace(usageLine, `"turn_id":"t"`, `"id":"`+strings.Repeat("x", 257)+`","turn_id":"t"`, 1)
	if err := os.WriteFile(path, []byte(header+bad+usageLine), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(path)
	defer r.Close()
	r.seen = make([]uint64, seenWords)
	for i := range r.seen {
		r.seen[i] = ^uint64(0)
	}
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if r.State.StepCount != 1 || r.State.TurnCount != 1 || r.State.Malformed != 1 {
		t.Fatal("malformed metadata hid a confirmed response")
	}
}

func (a *appendDuringProof) ReadAt(p []byte, off int64) (int, error) {
	n, e := a.f.ReadAt(p, off)
	w, err := os.OpenFile(a.path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return n, err
	}
	_, err = w.Write([]byte{'\n'})
	w.Close()
	a.appends++
	if err != nil {
		return n, err
	}
	return n, e
}
func TestContinuousSafeAppendDefersProofWithoutRebuilding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source")
	os.WriteFile(path, []byte(header+usageLine), 0600)
	r := New(path)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := r.State.Snapshot()
	gen := r.rebuilds
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	info, _ := f.Stat()
	part := r.sourceParts(f, info)[0]
	a := &appendDuringProof{f: f, path: path}
	part.reader = a
	r.proofs[0].Stamp.Trusted = false
	for range 4 {
		err = r.sourceFailure(r.verifyPart(context.Background(), part, &r.proofs[0], 0))
		if !errors.Is(err, ErrSourceBusy) || r.failure != nil || !r.State.Snapshot().Equal(before) {
			t.Fatal("safe append was treated as rewritten history", err)
		}
	}
	if _, err = r.Update(context.Background()); err != nil || r.rebuilds != gen || !r.State.Snapshot().Equal(before) {
		t.Fatal("stable proof did not resume without replay", err)
	}
}
