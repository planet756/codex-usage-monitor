package popup

import (
	"testing"
	"time"
)

func TestFindBarIdentityIsExactAndIndependentOfFocusOrLanguage(t *testing.T) {
	id := FindInputPrefix + "01a15da4-16e9-7951-8fce-50f7bef8bb83"
	if got := SurfaceSource("", 50004, id, false, false); got != "uia_find_bar" {
		t.Fatalf("find bar: %q", got)
	}
	for _, value := range []string{"", "search", "prompt-textarea", FindInputPrefix, id + "-other", "other-" + id,
		FindInputPrefix + "not-a-uuid", FindInputPrefix + "01a15da4x16e9-7951-8fce-50f7bef8bb83"} {
		if got := SurfaceSource("", 50004, value, false, false); got != "" {
			t.Fatalf("unrelated edit hidden: %q", value)
		}
	}
	if got := SurfaceSource("", 50020, id, false, false); got != "" {
		t.Fatal("transcript text treated as search input")
	}
}

func TestFindBarKeepsExistingDialogAndMenuDetection(t *testing.T) {
	for _, tc := range []struct {
		role          string
		control       int64
		dialog, modal bool
		want          string
	}{
		{"dialog", 50026, false, false, "aria_dialog"},
		{"alertdialog", 50026, false, false, "aria_alertdialog"},
		{"", 50009, false, false, "uia_menu"},
		{"", 50032, true, false, "uia_dialog"},
		{"", 50032, false, true, "uia_dialog"},
	} {
		if got := SurfaceSource(tc.role, tc.control, "", tc.dialog, tc.modal); got != tc.want {
			t.Fatalf("lost modal: %+v -> %s", tc, got)
		}
	}
}

func TestFindBarHidesThroughBlurAndNestedPopupThenRestores(t *testing.T) {
	now := time.Unix(100, 0)
	var state State
	find := SurfaceSource("", 50004, FindInputPrefix+"01a15da4-16e9-7951-8fce-50f7bef8bb83", false, false)
	state.Observe(1, Observation{Available: true, Visible: true, Source: find, At: now})
	// Search remains present when focus moves elsewhere; no focus flag is used.
	state.Observe(1, Observation{Available: true, Visible: true, Source: find, At: now.Add(time.Second)})
	if !state.Hidden {
		t.Fatal("blur exposed strip over visible search")
	}
	state.Observe(1, Observation{Available: true, Visible: true, Source: "aria_dialog", At: now.Add(2 * time.Second)})
	state.Observe(1, Observation{Available: true, Visible: true, Source: find, At: now.Add(3 * time.Second)})
	state.Observe(1, Observation{Source: "uia_popup_query_failed", At: now.Add(4 * time.Second)})
	if !state.Hidden {
		t.Fatal("timeout or nested close exposed search")
	}
	state.Observe(1, Observation{Available: true, At: now.Add(5 * time.Second)})
	if !state.Hidden {
		t.Fatal("single clear query restored too early")
	}
	state.Observe(1, Observation{Available: true, At: now.Add(5250 * time.Millisecond)})
	if state.Hidden {
		t.Fatal("closed search never restored strip")
	}
}
