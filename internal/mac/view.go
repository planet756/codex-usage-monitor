package mac

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/monitor"
	"codex-usage-monitor/internal/popup"
)

type observation struct {
	Epoch             uint64
	View              monitor.View
	Bounds            layout.Rect
	GeometryAt        time.Time
	Window            uintptr // Transferred, retained AX reference; the UI must release it.
	Popup             popup.Observation
	Nodes             int
	Minimized         bool
	AXEnableRequested bool
	AXEnableCode      int32
}

type locator struct {
	app, window uintptr
	ax          accessibility
	lookup      func(string, string) (ipc.Selection, string)
	catalog     ipc.ViewCatalog
	catalogFile os.FileInfo
	activation  axActivation
}

func (v *locator) close() {
	if v.window != 0 {
		v.ax.release(v.window)
		v.window = 0
	}
	if v.app != 0 {
		v.ax.release(v.app)
		v.app = 0
	}
}

func (v *locator) selectWindow(q *axQuery) (uintptr, bool) {
	blocked := false
	if focused := q.copy(v.app, "AXFocusedWindow"); focused != 0 {
		blocked = q.dialog(focused)
		if !blocked && q.live() {
			return focused, false
		}
		v.ax.release(focused)
	}
	if main := q.copy(v.app, "AXMainWindow"); main != 0 {
		modal := q.dialog(main)
		blocked = blocked || modal
		if !modal && q.live() {
			return main, blocked
		}
		v.ax.release(main)
	}
	// Sheets can occupy both focused and main slots; minimized windows can
	// occupy neither. Preserve only a verified reason to use the cached window.
	if v.window != 0 && (blocked || q.flag(v.window, "AXMinimized")) {
		return v.ax.retain(v.window), blocked
	}
	return 0, blocked
}

func (v *locator) loadCatalog(home string) {
	var state struct {
		Atoms ipc.ViewCatalog `json:"electron-persisted-atom-state"`
	}
	f, err := os.Open(filepath.Join(home, ".codex-global-state.json"))
	if err != nil {
		v.catalog, v.catalogFile = ipc.ViewCatalog{}, nil
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || info.Size() > 16<<20 {
		v.catalog, v.catalogFile = ipc.ViewCatalog{}, nil
		return
	}
	if v.catalogFile != nil && os.SameFile(info, v.catalogFile) && info.Size() == v.catalogFile.Size() && info.ModTime().Equal(v.catalogFile.ModTime()) {
		return
	}
	v.catalog, v.catalogFile = ipc.ViewCatalog{}, nil
	if json.NewDecoder(io.LimitReader(f, (16<<20)+1)).Decode(&state) == nil {
		v.catalog, v.catalogFile = state.Atoms, info
	}
}

func (v *locator) probe(parent context.Context, home string) (o observation) {
	ctx, cancel := context.WithTimeout(parent, axProbeTimeout)
	defer cancel()
	q := axQuery{ax: v.ax, ctx: ctx, complete: true}
	v.activation.ensure(ctx, v.ax, v.app, time.Now())
	o.AXEnableRequested, o.AXEnableCode = v.activation.requested, v.activation.code
	o.View.Reason = "selected_chat_unavailable"
	o.Popup = popup.Observation{At: time.Now(), Source: "ax_unavailable"}
	scanned := false
	defer func() {
		// A positive surface is sufficient to hide. Only a complete query may
		// clear an earlier popup, including on an early identity return.
		o.Popup.Available = o.Popup.Visible || scanned && q.live()
	}()
	window, blocked := v.selectWindow(&q)
	if blocked {
		o.Popup.Visible, o.Popup.Source = true, "ax_native_dialog"
	}
	if window == 0 {
		if v.window != 0 {
			o.View.Window = v.ax.key(v.window)
		}
		return o
	}
	o.Window, o.View.Window = window, v.ax.key(window)
	o.Minimized = q.flag(window, "AXMinimized")
	// Minimized windows can have no usable bounds. Their verified binding
	// still belongs to the same conversation while the bar is hidden.
	if o.Minimized {
		return o
	}
	r, ok := q.bounds(window)
	if !ok {
		return o
	}
	o.Bounds, o.GeometryAt = r, time.Now()
	if v.window == 0 || !v.ax.equal(v.window, window) {
		if v.window != 0 {
			v.ax.release(v.window)
		}
		v.window = v.ax.retain(window)
	}
	if blocked || r.W < 400 || r.H < 250 {
		return o
	}
	v.loadCatalog(home)
	var documents, selected ipc.ViewCandidates
	var titles []string
	selectedCount := 0
	composer, headerAmbiguous, headerPlaceholder := false, false, false
	documentPending, selectedPending, other := false, false, false
	o.Popup.Source = "ax_surfaces"
	resolve := func(value, source string) (ipc.Selection, bool) {
		id, host := v.catalog.URL(value)
		raw, _ := ipc.ViewRoute(value)
		return ipc.Selection{ID: id, Host: host, Source: source}, raw != "" && id == ""
	}
	caption := func(node uintptr) string {
		value := q.text(node, "AXTitle")
		if value == "" && q.text(node, "AXRole") == "AXStaticText" {
			value = q.text(node, "AXValue")
		}
		return value
	}
	addCaption := func(title string) {
		if title == "" {
			return
		}
		if ipc.PlaceholderChatTitle(title) {
			headerPlaceholder = true
			return
		}
		for _, prior := range titles {
			if prior == title {
				return
			}
		}
		titles = append(titles, title)
	}
	var walk func(uintptr, int, bool, bool, bool)
	walk = func(el uintptr, depth int, inSelected, inHeader, inDocument bool) {
		if !q.live() {
			return
		}
		if o.Nodes >= 512 || depth > 18 {
			q.complete = false
			return
		}
		o.Nodes++
		role := q.text(el, "AXRole")
		subrole := q.text(el, "AXSubrole")
		identifier := q.text(el, "AXDOMIdentifier")
		if identifier == "" {
			identifier = q.text(el, "AXIdentifier")
		}
		if dialogRole(role, subrole) || q.flag(el, "AXModal") {
			o.Popup.Visible = true
			return
		}
		if (role == "AXTextArea" || role == "AXTextField") && popup.SurfaceSource("", 50004, identifier, false, false) != "" {
			o.Popup.Visible = true
			return
		}
		var tokens []string
		q.each(el, "AXDOMClassList", 128, func(p uintptr) bool { tokens = append(tokens, v.ax.text(p)); return false })
		has := func(value string) bool {
			for _, token := range tokens {
				if token == value {
					return true
				}
			}
			return false
		}
		// Transcript blocks and editor values cannot establish selected-page
		// identity. Do not traverse their contents.
		if has("[content-visibility:auto]") {
			return
		}
		if role == "AXWebArea" {
			if inDocument {
				return // An embedded page cannot override the Desktop route.
			}
			inDocument = true
			url := q.text(el, "AXURL")
			s, pending := resolve(url, "ax_document_url")
			page := ipc.DocumentPage(url)
			documentPending = documentPending || pending || page == ipc.PageChat && s.ID == ""
			documents.Add(s)
			other = other || page == ipc.PageOther
		}
		if role == "AXOutline" || role == "AXList" {
			selectedOnly := false
			q.each(el, "AXSelectedChildren", 32, func(child uintptr) bool {
				selectedOnly = true
				walk(child, depth+1, true, inHeader, inDocument)
				return !q.live()
			})
			if selectedOnly {
				return
			}
		}
		toolbar := role == "AXToolbar" && has("min-w-0") && has("shrink-0") && has("select-none")
		if toolbar {
			inHeader = true
			o.View.HeaderProbe[0]++
			var title string
			count := 0
			q.each(el, "AXChildren", 32, func(child uintptr) bool {
				if q.text(child, "AXRole") == "AXStaticText" {
					count++
					title = caption(child)
				}
				return !q.live()
			})
			if count == 1 {
				addCaption(title)
			} else if count > 1 {
				headerAmbiguous = true
			}
		}
		if has("ProseMirror") {
			composer = true
			return
		}
		if role == "AXTextArea" || role == "AXTextField" || role == "AXSecureTextField" {
			return
		}
		if (role == "AXRow" || role == "AXOutlineRow" || role == "AXLink") && q.flag(el, "AXSelected") {
			inSelected = true
			selectedCount++
		}
		if inSelected {
			s, pending := resolve(q.text(el, "AXURL"), "ax_selected_url")
			selectedPending = selectedPending || pending
			selected.Add(s)
		}
		if inHeader && has("min-w-0") && has("text-base") && has("font-medium") && has("text-default") {
			o.View.HeaderProbe[1]++
			if title := caption(el); title != "" {
				addCaption(title)
			} else {
				q.each(el, "AXChildren", 32, func(child uintptr) bool {
					if q.text(child, "AXRole") == "AXStaticText" {
						addCaption(caption(child))
					}
					return !q.live()
				})
			}
		}
		if role == "AXStaticText" && !inHeader && !inSelected {
			return
		}
		q.each(el, "AXChildren", 512, func(child uintptr) bool {
			walk(child, depth+1, inSelected, inHeader, inDocument)
			return !q.live()
		})
	}
	walk(window, 0, false, false, false)
	scanned = true
	d, da := documents.Result(true)
	o.View.SelectedElements = selectedCount
	bind := func(s ipc.Selection) {
		o.View.ID, o.View.Host, o.View.Source, o.View.Reason = s.ID, s.Host, s.Source, "verified"
		o.View.ChatPage = true
	}
	if da || d.ID != "" && (other || documentPending) {
		o.View.Reason = "selected_chat_ambiguous"
		return o
	}
	// A fresh document route is authoritative even if an optional caption is
	// duplicated or the subsequent scan exhausts its budget.
	if d.ID != "" {
		bind(d)
		return o
	}
	if other {
		o.View.Reason = "non_chat_page"
		return o
	}
	if documentPending {
		o.View.ChatPage, o.View.Reason = true, "pending_chat_identity"
		return o
	}
	if !q.live() {
		o.View.Reason = "ax_query_incomplete"
		return o
	}
	chatScope := composer && o.View.HeaderProbe[0] == 1
	o.View.ChatPage = chatScope
	if selectedPending {
		o.View.Reason = "pending_chat_identity"
		return o
	}
	s, sa := selected.Result(true)
	if sa {
		o.View.Reason = "selected_chat_ambiguous"
		return o
	}
	if s.ID != "" && chatScope && !headerPlaceholder {
		bind(s)
		return o
	}
	if headerAmbiguous || len(titles) > 1 {
		o.View.Reason = "selected_chat_ambiguous"
		return o
	}
	if headerPlaceholder {
		o.View.Reason = "pending_chat_identity"
		return o
	}
	if len(titles) == 1 {
		s, issue := v.lookup(home, titles[0])
		if issue != "" {
			o.View.Reason = issue
		} else if s.ID != "" {
			o.View.HeaderProbe[2]++
			bind(s)
		}
	}
	return o
}

func retainChat(previous monitor.View, o observation, hidden bool) bool {
	if o.View.Reason == "pending_chat_identity" || o.View.Reason == "non_chat_page" {
		return false
	}
	return (hidden || o.Minimized) && o.View.Window == previous.Window && o.View.ID == "" && previous.ID != ""
}
