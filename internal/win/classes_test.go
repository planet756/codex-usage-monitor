package win

import "testing"

func TestChatTitleClassUsesCurrentDesktopRoleWithoutPaddingDependency(t *testing.T) {
	// Captured from the installed 26.1021.2667.0 app.asar's Dqn title
	// component. This is the default header variant used by the Chat toolbar.
	current := "min-w-0 text-base font-medium text-default px-button-toolbar-inline min-h-token-button-composer content-center -ms-0.5 no-drag cursor-interaction rounded-md text-start hover:bg-primary-ghost-hover focus-visible:bg-primary-ghost-hover focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-0 truncate leading-6"
	for _, value := range []string{current,
		"min-w-0 px-1.5 text-base font-medium text-default",
		"text-default font-medium text-base min-w-0",
		"min-w-0 text-base font-medium text-default px-any-future-spacing"} {
		if !chatTitleClass(value) {
			t.Fatalf("valid title role rejected by spacing: %q", value)
		}
	}
	for _, value := range []string{"", "min-w-0 shrink-0 select-none",
		"text-sm font-medium text-default", "min-w-0 px-1.5 text-base text-default",
		"min-w-0 px-1.5 text-base font-medium text-default-extra", "prefix-min-w-0 text-base font-medium text-default"} {
		if chatTitleClass(value) {
			t.Fatalf("unrelated control accepted: %q", value)
		}
	}
}

func TestRealAccessibleToolbarCaptionDoesNotNeedDOMWrapper(t *testing.T) {
	// Shape captured by CurrentPageProbe on the actual Desktop: a direct,
	// visible Text/description node, with no CSS class and a unique catalog name.
	actual := headerText{Name: "Current task", Role: "description", Control: 50020, Visible: true}
	if name, ambiguous := directChatCaption([]headerText{actual}); name != actual.Name || ambiguous {
		t.Fatal("real accessible caption rejected", name, ambiguous)
	}
	for _, other := range []headerText{
		{Name: "Action label", Role: "button", Control: 50000, Visible: true},
		{Name: "Editor", Role: "textbox", Control: 50004, Visible: true},
		{Name: "Hidden title", Role: "description", Control: 50020},
	} {
		if name, ambiguous := directChatCaption([]headerText{actual, other}); name != actual.Name || ambiguous {
			t.Fatal("non-caption changed identity", name, ambiguous)
		}
		if name, _ := directChatCaption([]headerText{other}); name != "" {
			t.Fatal("non-caption supplied a chat identity")
		}
	}
	other := headerText{Name: "Another label", Role: "description", Control: 50020, Visible: true}
	if name, ambiguous := directChatCaption([]headerText{actual, other}); name != "" || !ambiguous {
		t.Fatal("multiple direct texts must remain ambiguous", name, ambiguous)
	}
	if name, ambiguous := directChatCaption(nil); name != "" || ambiguous {
		t.Fatal("missing title invented an identity")
	}
}
