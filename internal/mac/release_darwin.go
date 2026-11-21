package mac

import (
	"codex-usage-monitor/internal/i18n"
	"github.com/ebitengine/purego/objc"
)

func (a *overlay) finishReleaseCheck() {
	result, ok := a.releaseCheck.Poll()
	if !ok {
		return
	}
	a.releaseCheck.Close()
	a.releaseCheck = nil
	tr := func(s string) string { return i18n.Text(a.cfg.Language, s) }
	title := tr("Check for Updates…")
	message, url := result.Prompt(a.cfg.Language)
	if url == "" {
		showAlert(title, message)
		return
	}
	alert := newObject("NSAlert")
	defer alert.Send(sel("release"))
	alert.Send(sel("setMessageText:"), ns(title))
	alert.Send(sel("setInformativeText:"), ns(message))
	alert.Send(sel("addButtonWithTitle:"), ns(tr("Open Release Page")))
	alert.Send(sel("addButtonWithTitle:"), ns(tr("Cancel")))
	if alert.Send(sel("runModal")) == 1000 { // NSAlertFirstButtonReturn
		a.openProjectPage(url)
	}
}

func (a *overlay) openProjectPage(url string) {
	address := objc.ID(objc.GetClass("NSURL")).Send(sel("URLWithString:"), ns(url))
	workspace := objc.ID(objc.GetClass("NSWorkspace")).Send(sel("sharedWorkspace"))
	if !objc.Send[bool](workspace, sel("openURL:"), address) {
		showAlert(i18n.Text(a.cfg.Language, "Project Homepage"), i18n.Text(a.cfg.Language, "Could not open the page in your browser."))
	}
}
