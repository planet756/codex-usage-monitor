package mac

import (
	"context"
	"fmt"
	"testing"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/monitor"
	"codex-usage-monitor/internal/popup"
)

const testChat = "01234567-89ab-cdef-0123-456789abcdef"

type axAttributeKey struct {
	el   uintptr
	name string
}

// Only the native boundary is simulated. Tests call the production locator,
// cancellation and popup/subscription policies unchanged.
type fakeAX struct {
	attrs             map[uintptr]map[string]any
	values            map[uintptr]any
	refs              map[uintptr]int
	codes             map[axAttributeKey]int32
	next              uintptr
	calls             int
	onRead            func(uintptr, string)
	enableCode        int32
	enableCalls       int
	boundsUnavailable bool
}

func newFakeAX() *fakeAX {
	f := &fakeAX{attrs: make(map[uintptr]map[string]any), values: make(map[uintptr]any), refs: make(map[uintptr]int), codes: make(map[axAttributeKey]int32), next: 10000}
	f.node(1, "AXApplication")
	f.refs[1] = 1 // Transferred to locator.
	f.node(20, "AXWindow")
	f.attrs[1]["AXFocusedWindow"] = uintptr(20)
	f.attrs[1]["AXMainWindow"] = uintptr(20)
	f.node(30, "AXWebArea")
	f.attrs[20]["AXChildren"] = []uintptr{30}
	return f
}

func (f *fakeAX) node(id uintptr, role string) {
	f.attrs[id] = map[string]any{"AXRole": role}
}
func (f *fakeAX) box(value any) uintptr {
	f.next++
	f.values[f.next] = value
	return f.next
}
func (f *fakeAX) classes(id uintptr, names ...string) {
	var tokens []uintptr
	for _, name := range names {
		tokens = append(tokens, f.box(name))
	}
	f.attrs[id]["AXDOMClassList"] = tokens
}
func (f *fakeAX) chatHeader() {
	f.node(40, "AXToolbar")
	f.classes(40, "min-w-0", "shrink-0", "select-none")
	f.node(41, "AXStaticText")
	f.attrs[41]["AXValue"] = "Saved title"
	f.attrs[40]["AXChildren"] = []uintptr{41}
	f.node(50, "AXTextArea")
	f.classes(50, "ProseMirror")
	f.attrs[30]["AXChildren"] = []uintptr{40, 50}
}
func (f *fakeAX) attribute(el uintptr, name string) (uintptr, int32) {
	f.calls++
	if f.onRead != nil {
		f.onRead(el, name)
	}
	if code := f.codes[axAttributeKey{el, name}]; code != 0 {
		return 0, code
	}
	value, ok := f.attrs[el][name]
	if !ok {
		return 0, -25212 // kAXErrorNoValue
	}
	if node, ok := value.(uintptr); ok {
		return f.retain(node), 0
	}
	p := f.box(value)
	return f.retain(p), 0
}
func (f *fakeAX) text(p uintptr) string  { s, _ := f.values[p].(string); return s }
func (f *fakeAX) boolean(p uintptr) bool { b, _ := f.values[p].(bool); return b }
func (f *fakeAX) array(p uintptr, limit int, visit func(uintptr) bool) bool {
	items, ok := f.values[p].([]uintptr)
	if !ok || len(items) > limit {
		return false
	}
	for _, item := range items {
		if visit(item) {
			break
		}
	}
	return true
}
func (f *fakeAX) enable(uintptr) int32 {
	f.enableCalls++
	return f.enableCode
}
func (f *fakeAX) bounds(uintptr) (layout.Rect, bool) {
	return layout.Rect{X: 100, Y: 100, W: 1000, H: 700}, !f.boundsUnavailable
}
func (f *fakeAX) retain(p uintptr) uintptr { f.refs[p]++; return p }
func (f *fakeAX) release(p uintptr) {
	f.refs[p]--
	if f.refs[p] < 0 {
		panic(fmt.Sprintf("over-release of AX reference %d", p))
	}
}
func (*fakeAX) equal(a, b uintptr) bool { return a == b }
func (*fakeAX) key(p uintptr) uintptr   { return p }

func testLocator(t *testing.T, f *fakeAX) (*locator, string) {
	t.Helper()
	v := &locator{app: 1, ax: f, lookup: func(string, string) (ipc.Selection, string) {
		return ipc.Selection{ID: testChat, Host: "local", Source: "ax_catalog_title"}, ""
	}}
	t.Cleanup(func() {
		v.close()
		for p, count := range f.refs {
			if count != 0 {
				t.Errorf("AX reference %d has %d unreleased owners", p, count)
			}
		}
	})
	return v, t.TempDir()
}

func probeForTest(v *locator, home string) observation {
	o := v.probe(context.Background(), home)
	if o.Window != 0 {
		v.ax.release(o.Window)
		o.Window = 0
	}
	return o
}

func TestDocumentRouteSurvivesOptionalAmbiguityAndIncompleteScan(t *testing.T) {
	for _, mode := range []string{"duplicate_catalog_title", "multiple_captions", "timeout", "too_many_children", "ax_failure"} {
		t.Run(mode, func(t *testing.T) {
			f := newFakeAX()
			f.attrs[30]["AXURL"] = "codex://threads/" + testChat
			f.chatHeader()
			v, home := testLocator(t, f)
			v.lookup = func(string, string) (ipc.Selection, string) {
				t.Error("optional title lookup must not veto a document route")
				return ipc.Selection{}, "selected_chat_ambiguous"
			}
			ctx := context.Background()
			if mode == "multiple_captions" {
				f.node(42, "AXStaticText")
				f.attrs[42]["AXValue"] = "Another title"
				f.attrs[40]["AXChildren"] = []uintptr{41, 42}
			}
			if mode == "too_many_children" {
				f.attrs[40]["AXChildren"] = make([]uintptr, 513)
			}
			if mode == "ax_failure" {
				f.codes[axAttributeKey{40, "AXRole"}] = -25204
			}
			if mode == "timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				f.onRead = func(el uintptr, _ string) {
					if el == 40 {
						cancel()
					}
				}
			}
			o := v.probe(ctx, home)
			if o.Window != 0 {
				f.release(o.Window)
			}
			if o.View.ID != testChat || !o.View.ChatPage || o.View.Source != "ax_document_url" {
				t.Fatalf("document route lost: %+v", o)
			}
			incomplete := mode == "timeout" || mode == "too_many_children" || mode == "ax_failure"
			if o.Popup.Available == incomplete {
				t.Fatalf("popup completeness incorrect: %+v", o.Popup)
			}
		})
	}
}

func TestSelectedURLPrecedesDuplicateCatalogTitle(t *testing.T) {
	f := newFakeAX()
	f.chatHeader()
	f.node(60, "AXRow")
	f.attrs[60]["AXSelected"] = true
	f.attrs[60]["AXURL"] = "codex://threads/" + testChat
	f.attrs[20]["AXChildren"] = []uintptr{60, 30}
	v, home := testLocator(t, f)
	v.lookup = func(string, string) (ipc.Selection, string) {
		t.Error("explicit selection must not query optional catalog title")
		return ipc.Selection{}, "selected_chat_ambiguous"
	}
	o := probeForTest(v, home)
	if o.View.ID != testChat || o.View.Source != "ax_selected_url" {
		t.Fatalf("explicit selection lost: %+v", o.View)
	}
	delete(f.attrs[30], "AXChildren")
	if o = probeForTest(v, home); o.View.ID != "" {
		t.Fatal("stale sidebar selected a chat without current page evidence")
	}
}

func TestCaptionFallbackRequiresUniqueTitleAndCompleteScan(t *testing.T) {
	f := newFakeAX()
	f.chatHeader()
	v, home := testLocator(t, f)
	if o := probeForTest(v, home); o.View.ID != testChat {
		t.Fatalf("unique catalog fallback failed: %+v", o.View)
	}
	v.lookup = func(string, string) (ipc.Selection, string) { return ipc.Selection{}, "selected_chat_ambiguous" }
	if o := probeForTest(v, home); o.View.ID != "" || o.View.Reason != "selected_chat_ambiguous" {
		t.Fatalf("duplicate title established identity: %+v", o.View)
	}
	f.codes[axAttributeKey{50, "AXRole"}] = -25204
	if o := probeForTest(v, home); o.View.ID != "" || o.Popup.Available {
		t.Fatalf("incomplete title probe was accepted: %+v", o)
	}
}

func TestNativeDialogKeepsChatWindowAndSubscription(t *testing.T) {
	f := newFakeAX()
	f.attrs[30]["AXURL"] = "codex://threads/" + testChat
	v, home := testLocator(t, f)
	previous := probeForTest(v, home).View
	f.node(70, "AXSheet")
	f.attrs[1]["AXFocusedWindow"] = uintptr(70)
	for _, main := range []uintptr{20, 70} {
		f.attrs[1]["AXMainWindow"] = main
		o := probeForTest(v, home)
		if o.View.Window != 20 || !o.Popup.Visible || !o.Popup.Available || !retainChat(previous, o, true) {
			t.Fatalf("dialog changed chat binding (main=%d): %+v", main, o)
		}
	}
	if retainChat(previous, observation{View: monitor.View{Window: 90}}, true) {
		t.Fatal("retained a subscription across a real window change")
	}
}

func TestIncompleteProbeCannotClearKnownPopup(t *testing.T) {
	f := newFakeAX()
	f.chatHeader()
	v, home := testLocator(t, f)
	probeForTest(v, home)
	state := popup.State{}
	at := time.Now()
	state.Observe(20, popup.Observation{At: at, Available: true, Visible: true})
	f.codes[axAttributeKey{50, "AXRole"}] = -25204
	for i := 1; i <= 2; i++ {
		o := probeForTest(v, home)
		o.Popup.At = at.Add(time.Duration(i) * time.Second)
		state.Observe(uint64(o.View.Window), o.Popup)
		if !state.Hidden || o.Popup.Available {
			t.Fatalf("partial scan cleared popup: %+v %+v", state, o)
		}
	}
	delete(f.codes, axAttributeKey{50, "AXRole"})
	for i := 3; i <= 4; i++ {
		o := probeForTest(v, home)
		o.Popup.At = at.Add(time.Duration(i) * time.Second)
		state.Observe(uint64(o.View.Window), o.Popup)
	}
	if state.Hidden {
		t.Fatal("two complete clear probes did not restore visibility")
	}
	// An unavailable root also keeps the same window's popup state unknown.
	f.codes[axAttributeKey{1, "AXFocusedWindow"}] = -25204
	o := probeForTest(v, home)
	if o.View.Window != 20 || o.Popup.Available {
		t.Fatalf("unavailable root reset known window: %+v", o)
	}
}

func TestCancellationStopsNewAXCallsAndReleasesLastValue(t *testing.T) {
	for _, before := range []bool{true, false} {
		t.Run(fmt.Sprint(before), func(t *testing.T) {
			f := newFakeAX()
			v, home := testLocator(t, f)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if before {
				cancel()
			} else {
				f.onRead = func(uintptr, string) { cancel() }
			}
			o := v.probe(ctx, home)
			wantCalls := 1
			if before {
				wantCalls = 0
			}
			if f.calls != wantCalls || o.Popup.Available || o.View.ID != "" {
				t.Fatalf("cancelled AX probe continued: calls=%d observation=%+v", f.calls, o)
			}
		})
	}
}

func TestExplicitNonChatRouteRejectsSidebarAndCaption(t *testing.T) {
	f := newFakeAX()
	f.chatHeader()
	f.attrs[30]["AXURL"] = "codex://settings"
	v, home := testLocator(t, f)
	o := probeForTest(v, home)
	if o.View.ID != "" || o.View.ChatPage || o.View.Reason != "non_chat_page" {
		t.Fatalf("non-chat navigation retained chat: %+v", o.View)
	}
}

func TestEmbeddedDocumentCannotOverrideDesktopRoute(t *testing.T) {
	f := newFakeAX()
	f.attrs[30]["AXURL"] = "codex://threads/" + testChat
	f.node(60, "AXWebArea")
	f.attrs[60]["AXURL"] = "codex://settings"
	f.attrs[30]["AXChildren"] = []uintptr{60}
	v, home := testLocator(t, f)
	if o := probeForTest(v, home); o.View.ID != testChat {
		t.Fatalf("embedded page overrode Desktop route: %+v", o.View)
	}
	// Separate top-level routes within the same window must still fail closed.
	f.attrs[20]["AXChildren"] = []uintptr{30, 60}
	if o := probeForTest(v, home); o.View.ID != "" || o.View.Reason != "selected_chat_ambiguous" {
		t.Fatalf("conflicting documents accepted: %+v", o.View)
	}
}

func TestPositivePopupRemainsAvailableWhenLaterBranchFails(t *testing.T) {
	f := newFakeAX()
	f.node(60, "AXMenu")
	f.node(70, "AXGroup")
	f.codes[axAttributeKey{70, "AXRole"}] = -25204
	f.attrs[30]["AXChildren"] = []uintptr{60, 70}
	v, home := testLocator(t, f)
	o := probeForTest(v, home)
	if !o.Popup.Visible || !o.Popup.Available {
		t.Fatalf("positive popup was lost on a later timeout: %+v", o)
	}
}

func TestNewChatDocumentRetiresOldTitleAndSidebar(t *testing.T) {
	for _, route := range []string{"/", "app://codex/index.html#/", "/threads/client-new-thread:" + testChat} {
		for _, incomplete := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/incomplete=%t", route, incomplete), func(t *testing.T) {
				f := newFakeAX()
				f.chatHeader()
				f.attrs[30]["AXURL"] = route
				f.node(60, "AXRow")
				f.attrs[60]["AXSelected"] = true
				f.attrs[60]["AXURL"] = "codex://threads/" + testChat
				f.attrs[20]["AXChildren"] = []uintptr{60, 30}
				if incomplete {
					f.codes[axAttributeKey{40, "AXRole"}] = -25204
				}
				v, home := testLocator(t, f)
				v.lookup = func(string, string) (ipc.Selection, string) {
					t.Error("new-chat route must not query old catalog title")
					return ipc.Selection{ID: testChat}, ""
				}
				o := probeForTest(v, home)
				if o.View.ID != "" || o.View.Reason != "pending_chat_identity" || !o.View.ChatPage {
					t.Fatalf("new chat bound old evidence: %+v", o.View)
				}
				previous := monitor.View{Window: 20, ID: testChat, ChatPage: true}
				if retainChat(previous, o, true) {
					t.Fatal("popup debounce retained an explicitly retired chat")
				}
			})
		}
	}
}

func TestMinimizedWindowWithoutFocusKeepsSubscription(t *testing.T) {
	f := newFakeAX()
	f.attrs[30]["AXURL"] = "codex://threads/" + testChat
	v, home := testLocator(t, f)
	previous := probeForTest(v, home).View
	delete(f.attrs[1], "AXFocusedWindow")
	delete(f.attrs[1], "AXMainWindow")
	f.attrs[20]["AXMinimized"] = true
	f.boundsUnavailable = true
	o := probeForTest(v, home)
	if !o.Minimized || o.View.Window != previous.Window || !retainChat(previous, o, false) {
		t.Fatalf("minimized window lost subscription: %+v", o)
	}
	// Missing focused/main slots alone cannot keep a closed window bound.
	f.attrs[20]["AXMinimized"] = false
	if o = probeForTest(v, home); retainChat(previous, o, false) {
		t.Fatal("unavailable non-minimized window retained old conversation")
	}
	f.boundsUnavailable = false
	f.attrs[1]["AXFocusedWindow"] = uintptr(20)
	if o = probeForTest(v, home); o.Minimized || o.View.ID != testChat {
		t.Fatalf("restored window did not resume: %+v", o)
	}
}

func TestElectronDialogHidesWithoutClearingConversation(t *testing.T) {
	for _, subrole := range []string{"AXApplicationDialog", "AXApplicationAlertDialog"} {
		t.Run(subrole, func(t *testing.T) {
			f := newFakeAX()
			f.attrs[30]["AXURL"] = "codex://threads/" + testChat
			v, home := testLocator(t, f)
			previous := probeForTest(v, home).View
			f.node(70, "AXGroup")
			f.attrs[70]["AXSubrole"] = subrole
			f.attrs[30]["AXChildren"] = []uintptr{70}
			delete(f.attrs[30], "AXURL") // Background page can be hidden by the modal.
			o := probeForTest(v, home)
			var state popup.State
			state.Observe(uint64(o.View.Window), o.Popup)
			if !state.Hidden || !o.Popup.Available || !retainChat(previous, o, state.Hidden) {
				t.Fatalf("web dialog lost display/subscription separation: %+v", o)
			}
		})
	}
}
