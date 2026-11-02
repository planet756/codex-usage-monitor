package ipc

import "testing"

func TestDocumentPageSeparatesChatDraftsAndApplicationPages(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  PageKind
	}{
		{"codex://threads/" + id1, PageChat},
		{"/local/" + id1 + "?hostId=local", PageChat},
		{"file:///app/index.html#/local/client-new-thread:" + id1, PageChat},
		{"file:///app/index.html#/", PageChat},
		{"/", PageChat},
		{"/plugins", PageOther},
		{"file:///app/index.html#/plugins/example", PageOther},
		{"file:///app/index.html#/settings/appearance", PageOther},
		{"/settings?threadId=" + id1, PageOther},
		{"codex://settings/usage", PageOther},
		{"/automations", PageOther},
		{"/skills", PageOther},
		{"/local/not-a-chat-id", PageOther},
		{"", PageUnknown},
		{"file:///app/index.html", PageUnknown},
		{"/app/index.html", PageUnknown},
		{"Chat", PageUnknown},
		{"https://example.com/", PageUnknown},
		{"%invalid", PageUnknown},
	} {
		if got := DocumentPage(tc.value); got != tc.want {
			t.Errorf("DocumentPage(%q)=%v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestViewURLRequiresExplicitChatRoute(t *testing.T) {
	id := "01a15da4-16e9-7951-8fce-50f7bef8bb83"
	for _, value := range []string{"codex://threads/" + id, "/threads/" + id, "/local/" + id, "file:///app/index.html#/threads/" + id} {
		if got, _ := ViewURL(value); got != id {
			t.Fatalf("view route was not recognized: %s", value)
		}
	}
	for _, value := range []string{id, "/settings?threadId=" + id, "/threads/" + id + "/other", "/search/" + id} {
		if got, _ := ViewURL(value); got != "" {
			t.Fatal("unrelated UI data was treated as the current chat")
		}
	}
}

func TestSelectedRouteResolvesExactClientBinding(t *testing.T) {
	client := "client-new-thread:" + id2
	c := ViewCatalog{Bindings: map[string]string{client: id1}}
	if id, _ := c.URL("/local/" + client); id != id1 {
		t.Fatal("client route did not resolve to its canonical chat")
	}
	if id, _ := c.URL("/local/client-new-thread:" + id1); id != "" {
		t.Fatal("unknown client route guessed a chat")
	}
}

func TestClientHashRouteRetainsExactHost(t *testing.T) {
	c := ViewCatalog{Bindings: map[string]string{"client-new-thread:" + id2: id1}}
	if id, host := c.URL("file:///app/index.html#/local/client-new-thread:" + id2 + "?hostId=local"); id != id1 || host != "local" {
		t.Fatalf("lost hash host: %s %s", id, host)
	}
	if id, _ := c.URL("file:///app/index.html?hostId=other#/local/client-new-thread:" + id2 + "?hostId=local"); id != "" {
		t.Fatal("conflicting host accepted")
	}
}
