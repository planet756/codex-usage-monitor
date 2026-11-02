package ipc

// ViewCandidates only represents one fresh native query. No previous selected
// row can establish uniqueness after a later ambiguous observation.
type ViewCandidates struct {
	selected  Selection
	ambiguous bool
}

func (v *ViewCandidates) Add(s Selection) bool {
	if s.ID == "" {
		return false
	}
	if v.selected.ID != "" && (v.selected.ID != s.ID || v.selected.Host != s.Host) {
		v.ambiguous = true
		return true
	}
	v.selected = s
	return false
}

func (v ViewCandidates) Result(complete bool) (Selection, bool) {
	if v.ambiguous || !complete {
		return Selection{}, true
	}
	return v.selected, false
}
