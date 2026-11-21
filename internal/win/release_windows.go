package win

import (
	"unsafe"

	"codex-usage-monitor/internal/i18n"
)

func (a *overlay) finishReleaseCheck() {
	result, ok := a.releaseCheck.Poll()
	if !ok {
		return
	}
	a.releaseCheck.Close()
	a.releaseCheck = nil
	language := a.cfg.Language
	title := i18n.Text(language, "Check for Updates…")
	message, url := result.Prompt(language)
	if url == "" {
		messageBox(title, message)
		return
	}
	message += "\n\n" + i18n.Text(language, "Open the release page?")
	choice := call(user32, "MessageBoxW", a.hwnd, uintptr(unsafe.Pointer(utf(message))), uintptr(unsafe.Pointer(utf(title))), 0x40|0x4|0x100)
	if choice == 6 { // IDYES
		a.openProjectPage(url)
	}
}

func (a *overlay) openProjectPage(url string) {
	result := call(shell32, "ShellExecuteW", a.hwnd, uintptr(unsafe.Pointer(utf("open"))), uintptr(unsafe.Pointer(utf(url))), 0, 0, 1)
	if result <= 32 {
		messageBox(i18n.Text(a.cfg.Language, "Project Homepage"), i18n.Text(a.cfg.Language, "Could not open the page in your browser."))
	}
}
