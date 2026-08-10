package acp

import (
	"sync"

	acpsdk "github.com/coder/acp-go-sdk"
)

type acpSessionRootState struct {
	root      string
	ambiguous bool
}

type acpSessionRootLocator struct {
	mu    sync.Mutex
	roots map[acpsdk.SessionId]acpSessionRootState
}

func newACPSessionRootLocator() *acpSessionRootLocator {
	return &acpSessionRootLocator{roots: make(map[acpsdk.SessionId]acpSessionRootState)}
}

func (locator *acpSessionRootLocator) remember(id acpsdk.SessionId, root string) {
	root = canonicalACPSessionDirectory(root)

	locator.mu.Lock()
	defer locator.mu.Unlock()

	state, ok := locator.roots[id]
	if !ok {
		locator.roots[id] = acpSessionRootState{root: root}
		return
	}
	if state.ambiguous || state.root == root {
		return
	}
	state.root = ""
	state.ambiguous = true
	locator.roots[id] = state
}

func (locator *acpSessionRootLocator) resolve(id acpsdk.SessionId, fallback string) (string, bool, bool) {
	fallback = canonicalACPSessionDirectory(fallback)

	locator.mu.Lock()
	defer locator.mu.Unlock()

	state, ok := locator.roots[id]
	if !ok {
		return fallback, false, false
	}
	if state.ambiguous {
		return "", true, true
	}
	return state.root, true, false
}

func (locator *acpSessionRootLocator) forget(id acpsdk.SessionId, expectedRoot string) {
	expectedRoot = canonicalACPSessionDirectory(expectedRoot)

	locator.mu.Lock()
	defer locator.mu.Unlock()

	state, ok := locator.roots[id]
	if ok && !state.ambiguous && state.root == expectedRoot {
		delete(locator.roots, id)
	}
}
