package popup

import "strings"

// Desktop's find input uses this DOM id in chat, diff and document scopes.
// UIA exposes the id as AutomationId, independently of language and focus.
const FindInputPrefix = "content-search-input-"

func SurfaceSource(role string, controlType int64, automationID string, dialog, modal bool) string {
	switch role {
	case "dialog":
		return "aria_dialog"
	case "alertdialog":
		return "aria_alertdialog"
	}
	if controlType == 50009 {
		return "uia_menu"
	}
	if dialog || modal {
		return "uia_dialog"
	}
	if controlType != 50004 || !strings.HasPrefix(automationID, FindInputPrefix) {
		return ""
	}
	id := strings.TrimPrefix(automationID, FindInputPrefix)
	if len(id) != 36 {
		return ""
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return ""
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
			return ""
		}
	}
	return "uia_find_bar"
}
