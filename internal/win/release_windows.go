package win

import (
	"fmt"
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
	if result.Err != nil {
		messageBox(title, i18n.Text(language, "Could not check for updates. Please try again."))
		return
	}
	message, _ := result.Prompt(language)
	if !result.Newer {
		messageBox(title, message)
		return
	}
	message += "\n\n" + i18n.Text(language, "Download and install the update?")
	if confirmUpdate(a.hwnd, title, message) {
		if err := startUpdate(result.Latest, a.configPath); err != nil {
			showUpdateFailure(language, err)
		}
	}
}

func confirmUpdate(owner uintptr, title, message string) bool {
	const (
		yesNoWithNoDefault = 0x40 | 0x4 | 0x100
		setForeground      = 0x10000
	)
	return call(user32, "MessageBoxW", owner, uintptr(unsafe.Pointer(utf(message))), uintptr(unsafe.Pointer(utf(title))), yesNoWithNoDefault|setForeground) == 6
}

func showUpdateFailure(language string, err error) {
	messageBox(i18n.Text(language, "Software Update"), fmt.Sprintf("%s\n\n%v", i18n.Text(language, "The update could not be installed."), err))
}

func (a *overlay) openProjectPage(url string) {
	result := call(shell32, "ShellExecuteW", a.hwnd, uintptr(unsafe.Pointer(utf("open"))), uintptr(unsafe.Pointer(utf(url))), 0, 0, 1)
	if result <= 32 {
		messageBox(i18n.Text(a.cfg.Language, "Project Homepage"), i18n.Text(a.cfg.Language, "Could not open the page in your browser."))
	}
}
