package win

import "strings"

func classTokens(value string, required ...string) bool {
	parts := strings.Fields(value)
	for _, want := range required {
		found := false
		for _, part := range parts {
			if part == want {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Called only inside the verified Chat toolbar scope. Padding is layout, not
// identity: current Desktop uses tokenized toolbar padding and can change it
// independently of the title's role. Do not make it an identity requirement.
func chatTitleClass(value string) bool {
	return classTokens(value, "min-w-0", "text-base", "font-medium", "text-default")
}

type headerText struct {
	Name, Role string
	Control    int64
	Visible    bool
}

// Native Chromium can omit the DOM wrapper and expose its caption as one
// classless Text/description child of the Chat toolbar. Callers supply ONLY
// direct children, excluding action-button descendants and transcript text.
// More than one caption is ambiguous even if just one happens to match a chat.
func directChatCaption(nodes []headerText) (name string, ambiguous bool) {
	count := 0
	for _, node := range nodes {
		if !node.Visible || node.Control != 50020 || node.Role != "description" {
			continue
		}
		count++
		name = node.Name
	}
	if count != 1 {
		return "", count > 1
	}
	return name, false
}
