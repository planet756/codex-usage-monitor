package win

import "testing"

// Running this native Windows package test is also a startup smoke test: all
// callbacks are registered during package initialization before tests execute.
// Cross-compilation alone cannot execute Go's callback bridge validation.
func TestNativeCallbackInitialization(t *testing.T) {
	for name, address := range map[string]uintptr{
		"window":               overlayProc,
		"watcher":              watcherProc,
		"settings":             settingsProc,
		"settings enumeration": settingsEnumProc,
		"enumeration":          enumProc,
		"window events":        hostEventProc,
		"watcher events":       watcherEventProc,
		"UIA query":            sinkQuery,
		"UIA reference":        sinkAddRef,
		"UIA release":          sinkRelease,
		"UIA selection":        sinkEvent,
		"UIA property":         sinkProperty,
		"UIA structure":        sinkStructure,
		"popup events":         popupEventProc,
	} {
		if address == 0 {
			t.Fatalf("%s callback was not initialized", name)
		}
	}
}
