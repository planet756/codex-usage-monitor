package ipc

// ViewCatalog resolves Desktop's persisted client-to-server bindings. Titles
// come from its UI catalog database, not generated thread descriptions.
type ViewCatalog struct {
	Bindings map[string]string `json:"client-thread-bindings-v1"`
}

func (c ViewCatalog) URL(value string) (string, string) {
	id, host := ViewRoute(value)
	if id == "" {
		return "", ""
	}
	if !ValidThreadID(id) {
		id = c.Bindings[id]
	}
	if !ValidThreadID(id) {
		return "", ""
	}
	return id, host
}
