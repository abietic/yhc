package keybindings

import "runtime"

// Block represents a set of keybindings for a specific context.
type Block struct {
	Context  Context
	Bindings map[string]Action // key pattern -> action
}

// DefaultBindings returns the default keybinding blocks.
// Mirrors src/keybindings/defaultBindings.ts.
func DefaultBindings() []Block {
	modeCycleKey := "shift+tab"
	if runtime.GOOS == "windows" {
		modeCycleKey = "meta+m"
	}

	return []Block{
		{
			Context: ContextGlobal,
			Bindings: map[string]Action{
				"ctrl+c": ActionAppInterrupt,
				"ctrl+d": ActionAppExit,
				"ctrl+l": ActionAppRedraw,
				"ctrl+t": ActionAppToggleTodos,
				"ctrl+o": ActionAppToggleTranscript,
				"ctrl+f": ActionAppGlobalSearch,
				"ctrl+k": ActionAppQuickOpen,
			},
		},
		{
			Context: ContextChat,
			Bindings: map[string]Action{
				"escape":      ActionChatCancel,
				modeCycleKey:  ActionChatCycleMode,
				"meta+p":      ActionChatModelPicker,
				"alt+left":    ActionChatPreviousAgent,
				"alt+right":   ActionChatNextAgent,
				"enter":       ActionChatSubmit,
				"ctrl+j":      ActionChatNewline,
				"shift+enter": ActionChatNewline,
				"up":          ActionHistoryPrevious,
				"ctrl+p":      ActionHistoryPrevious,
				"down":        ActionHistoryNext,
				"ctrl+n":      ActionHistoryNext,
				"ctrl+r":      ActionHistorySearch,
				"ctrl+g":      ActionChatExternalEditor,
				"ctrl+z":      ActionChatUndo,
				"ctrl+b":      ActionTaskBackground,
				"ctrl+v":      ActionChatImagePaste,
			},
		},
		{
			Context: ContextAutocomplete,
			Bindings: map[string]Action{
				"tab":    ActionAutocompleteAccept,
				"enter":  ActionAutocompleteAccept,
				"up":     ActionAutocompletePrev,
				"down":   ActionAutocompleteNext,
				"escape": ActionAutocompleteDismiss,
			},
		},
		{
			Context: ContextHistorySearch,
			Bindings: map[string]Action{
				"ctrl+r": ActionHistorySearchNext,
				"enter":  ActionHistorySearchAccept,
				"escape": ActionHistorySearchCancel,
			},
		},
		{
			Context: ContextTranscript,
			Bindings: map[string]Action{
				"ctrl+o": ActionTranscriptExit,
				"escape": ActionTranscriptExit,
				"q":      ActionTranscriptExit,
				"ctrl+f": ActionTranscriptSearch,
				"r":      ActionTranscriptToggleRaw,
				"up":     ActionScrollLineUp,
				"k":      ActionScrollLineUp,
				"down":   ActionScrollLineDown,
				"j":      ActionScrollLineDown,
			},
		},
		{
			Context: ContextTask,
			Bindings: map[string]Action{
				"escape": ActionTaskClose,
				"q":      ActionTaskClose,
				"ctrl+t": ActionTaskClose,
				"r":      ActionTaskRefresh,
				"up":     ActionScrollLineUp,
				"k":      ActionScrollLineUp,
				"down":   ActionScrollLineDown,
				"j":      ActionScrollLineDown,
			},
		},
		{
			Context: ContextScroll,
			Bindings: map[string]Action{
				"pageup":       ActionScrollPageUp,
				"pagedown":     ActionScrollPageDown,
				"ctrl+u":       ActionScrollHalfUp,
				"home":         ActionScrollTop,
				"ctrl+home":    ActionScrollTop,
				"end":          ActionScrollBottom,
				"ctrl+end":     ActionScrollBottom,
				"y":            ActionSelectionCopy,
				"ctrl+shift+c": ActionSelectionCopy,
			},
		},
		{
			Context: ContextHelp,
			Bindings: map[string]Action{
				"escape": ActionHelpDismiss,
				"up":     ActionScrollLineUp,
				"k":      ActionScrollLineUp,
				"down":   ActionScrollLineDown,
				"j":      ActionScrollLineDown,
			},
		},
		{
			Context: ContextMessageSelector,
			Bindings: map[string]Action{
				"up":         ActionMessageSelectorUp,
				"down":       ActionMessageSelectorDown,
				"k":          ActionMessageSelectorUp,
				"j":          ActionMessageSelectorDown,
				"ctrl+p":     ActionMessageSelectorUp,
				"ctrl+n":     ActionMessageSelectorDown,
				"ctrl+up":    ActionMessageSelectorTop,
				"shift+up":   ActionMessageSelectorTop,
				"ctrl+down":  ActionMessageSelectorBottom,
				"shift+down": ActionMessageSelectorBottom,
				"enter":      ActionMessageSelectorSelect,
				"escape":     ActionMessageSelectorCancel,
			},
		},
	}
}
