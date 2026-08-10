// Package keybindings implements the keybinding system for the TUI.
// Mirrors src/keybindings/ from the reference implementation.
// Provides context-aware keyboard shortcut management with user customization.
package keybindings

// Context represents a UI context where keybindings can be applied.
type Context string

const (
	ContextGlobal          Context = "Global"
	ContextChat            Context = "Chat"
	ContextAutocomplete    Context = "Autocomplete"
	ContextConfirmation    Context = "Confirmation"
	ContextHelp            Context = "Help"
	ContextTranscript      Context = "Transcript"
	ContextHistorySearch   Context = "HistorySearch"
	ContextTask            Context = "Task"
	ContextThemePicker     Context = "ThemePicker"
	ContextSettings        Context = "Settings"
	ContextTabs            Context = "Tabs"
	ContextAttachments     Context = "Attachments"
	ContextFooter          Context = "Footer"
	ContextMessageSelector Context = "MessageSelector"
	ContextDiffDialog      Context = "DiffDialog"
	ContextModelPicker     Context = "ModelPicker"
	ContextSelect          Context = "Select"
	ContextPlugin          Context = "Plugin"
	ContextScroll          Context = "Scroll"
)

// AllContexts contains all valid keybinding contexts.
var AllContexts = []Context{
	ContextGlobal, ContextChat, ContextAutocomplete, ContextConfirmation,
	ContextHelp, ContextTranscript, ContextHistorySearch, ContextTask,
	ContextThemePicker, ContextSettings, ContextTabs, ContextAttachments,
	ContextFooter, ContextMessageSelector, ContextDiffDialog,
	ContextModelPicker, ContextSelect, ContextPlugin, ContextScroll,
}

// ContextDescriptions provides human-readable descriptions for each context.
var ContextDescriptions = map[Context]string{
	ContextGlobal:          "Active everywhere, regardless of focus",
	ContextChat:            "When the chat input is focused",
	ContextAutocomplete:    "When autocomplete menu is visible",
	ContextConfirmation:    "When a confirmation/permission dialog is shown",
	ContextHelp:            "When the help overlay is open",
	ContextTranscript:      "When viewing the transcript",
	ContextHistorySearch:   "When searching command history (ctrl+r)",
	ContextTask:            "When a task/agent is running in the foreground",
	ContextThemePicker:     "When the theme picker is open",
	ContextSettings:        "When the settings menu is open",
	ContextTabs:            "When tab navigation is active",
	ContextAttachments:     "When navigating image attachments",
	ContextFooter:          "When footer indicators are focused",
	ContextMessageSelector: "When the message selector (rewind) is open",
	ContextDiffDialog:      "When the diff dialog is open",
	ContextModelPicker:     "When the model picker is open",
	ContextSelect:          "When a select/list component is focused",
	ContextPlugin:          "When the plugin dialog is open",
	ContextScroll:          "When scrollable content is active",
}

// Action represents a keybinding action identifier.
type Action string

// App-level actions (Global context)
const (
	ActionAppInterrupt             Action = "app:interrupt"
	ActionAppExit                  Action = "app:exit"
	ActionAppToggleTodos           Action = "app:toggleTodos"
	ActionAppToggleTranscript      Action = "app:toggleTranscript"
	ActionAppToggleBrief           Action = "app:toggleBrief"
	ActionAppToggleTeammatePreview Action = "app:toggleTeammatePreview"
	ActionAppToggleTerminal        Action = "app:toggleTerminal"
	ActionAppRedraw                Action = "app:redraw"
	ActionAppGlobalSearch          Action = "app:globalSearch"
	ActionAppQuickOpen             Action = "app:quickOpen"
)

// History navigation actions
const (
	ActionHistorySearch   Action = "history:search"
	ActionHistoryPrevious Action = "history:previous"
	ActionHistoryNext     Action = "history:next"
)

// Chat input actions
const (
	ActionChatCancel         Action = "chat:cancel"
	ActionChatKillAgents     Action = "chat:killAgents"
	ActionChatCycleMode      Action = "chat:cycleMode"
	ActionChatModelPicker    Action = "chat:modelPicker"
	ActionChatFastMode       Action = "chat:fastMode"
	ActionChatThinkingToggle Action = "chat:thinkingToggle"
	ActionChatSubmit         Action = "chat:submit"
	ActionChatNewline        Action = "chat:newline"
	ActionChatUndo           Action = "chat:undo"
	ActionChatExternalEditor Action = "chat:externalEditor"
	ActionChatStash          Action = "chat:stash"
	ActionChatImagePaste     Action = "chat:imagePaste"
	ActionChatMessageActions Action = "chat:messageActions"
	ActionChatRewriteMessage Action = "chat:rewriteMessage"
	ActionChatPreviousAgent  Action = "chat:previousAgent"
	ActionChatNextAgent      Action = "chat:nextAgent"
)

// Autocomplete actions
const (
	ActionAutocompleteAccept  Action = "autocomplete:accept"
	ActionAutocompleteDismiss Action = "autocomplete:dismiss"
	ActionAutocompletePrev    Action = "autocomplete:previous"
	ActionAutocompleteNext    Action = "autocomplete:next"
)

// Confirmation dialog actions
const (
	ActionConfirmYes               Action = "confirm:yes"
	ActionConfirmNo                Action = "confirm:no"
	ActionConfirmPrevious          Action = "confirm:previous"
	ActionConfirmNext              Action = "confirm:next"
	ActionConfirmNextField         Action = "confirm:nextField"
	ActionConfirmPreviousField     Action = "confirm:previousField"
	ActionConfirmCycleMode         Action = "confirm:cycleMode"
	ActionConfirmToggle            Action = "confirm:toggle"
	ActionConfirmToggleExplanation Action = "confirm:toggleExplanation"
)

// Tabs actions
const (
	ActionTabsNext     Action = "tabs:next"
	ActionTabsPrevious Action = "tabs:previous"
)

// Transcript actions
const (
	ActionTranscriptToggleShowAll Action = "transcript:toggleShowAll"
	ActionTranscriptToggleRaw     Action = "transcript:toggleRaw"
	ActionTranscriptExit          Action = "transcript:exit"
	ActionTranscriptSearch        Action = "transcript:search"
)

// History search actions
const (
	ActionHistorySearchNext    Action = "historySearch:next"
	ActionHistorySearchAccept  Action = "historySearch:accept"
	ActionHistorySearchCancel  Action = "historySearch:cancel"
	ActionHistorySearchExecute Action = "historySearch:execute"
)

// Task actions
const (
	ActionTaskBackground Action = "task:background"
	ActionTaskClose      Action = "task:close"
	ActionTaskRefresh    Action = "task:refresh"
)

// Theme picker actions
const (
	ActionThemeToggleSyntaxHighlighting Action = "theme:toggleSyntaxHighlighting"
)

// Help actions
const (
	ActionHelpDismiss Action = "help:dismiss"
)

// Scroll actions
const (
	ActionScrollPageUp   Action = "scroll:pageUp"
	ActionScrollPageDown Action = "scroll:pageDown"
	ActionScrollHalfUp   Action = "scroll:halfPageUp"
	ActionScrollHalfDown Action = "scroll:halfPageDown"
	ActionScrollLineUp   Action = "scroll:lineUp"
	ActionScrollLineDown Action = "scroll:lineDown"
	ActionScrollTop      Action = "scroll:top"
	ActionScrollBottom   Action = "scroll:bottom"
	ActionSelectionCopy  Action = "selection:copy"
)

// Attachment actions
const (
	ActionAttachmentsNext     Action = "attachments:next"
	ActionAttachmentsPrevious Action = "attachments:previous"
	ActionAttachmentsRemove   Action = "attachments:remove"
	ActionAttachmentsExit     Action = "attachments:exit"
)

// Footer actions
const (
	ActionFooterUp             Action = "footer:up"
	ActionFooterDown           Action = "footer:down"
	ActionFooterNext           Action = "footer:next"
	ActionFooterPrevious       Action = "footer:previous"
	ActionFooterOpenSelected   Action = "footer:openSelected"
	ActionFooterClearSelection Action = "footer:clearSelection"
	ActionFooterClose          Action = "footer:close"
)

// Message selector actions
const (
	ActionMessageSelectorUp     Action = "messageSelector:up"
	ActionMessageSelectorDown   Action = "messageSelector:down"
	ActionMessageSelectorTop    Action = "messageSelector:top"
	ActionMessageSelectorBottom Action = "messageSelector:bottom"
	ActionMessageSelectorSelect Action = "messageSelector:select"
	ActionMessageSelectorCancel Action = "messageSelector:cancel"
)

// Diff dialog actions
const (
	ActionDiffDismiss      Action = "diff:dismiss"
	ActionDiffPrevSource   Action = "diff:previousSource"
	ActionDiffNextSource   Action = "diff:nextSource"
	ActionDiffBack         Action = "diff:back"
	ActionDiffViewDetails  Action = "diff:viewDetails"
	ActionDiffPreviousFile Action = "diff:previousFile"
	ActionDiffNextFile     Action = "diff:nextFile"
)

// Model picker actions
const (
	ActionModelPickerDecreaseEffort Action = "modelPicker:decreaseEffort"
	ActionModelPickerIncreaseEffort Action = "modelPicker:increaseEffort"
)

// Select component actions
const (
	ActionSelectNext     Action = "select:next"
	ActionSelectPrevious Action = "select:previous"
	ActionSelectAccept   Action = "select:accept"
	ActionSelectCancel   Action = "select:cancel"
)

// Plugin actions
const (
	ActionPluginToggle  Action = "plugin:toggle"
	ActionPluginInstall Action = "plugin:install"
)

// Permission actions
const (
	ActionPermissionToggleDebug Action = "permission:toggleDebug"
)

// Settings actions
const (
	ActionSettingsSearch Action = "settings:search"
	ActionSettingsRetry  Action = "settings:retry"
	ActionSettingsClose  Action = "settings:close"
)

// Voice actions
const (
	ActionVoicePushToTalk Action = "voice:pushToTalk"
)

// SupportedActionContexts is the product-reachable configurable surface. The
// schema keeps additional reference actions above for staged migration, but an
// action is not user-configurable until the App has a real handler for it.
var SupportedActionContexts = map[Action][]Context{
	ActionAppInterrupt:          {ContextGlobal},
	ActionAppExit:               {ContextGlobal},
	ActionAppToggleTodos:        {ContextGlobal},
	ActionAppToggleTranscript:   {ContextGlobal},
	ActionAppRedraw:             {ContextGlobal},
	ActionAppGlobalSearch:       {ContextGlobal},
	ActionAppQuickOpen:          {ContextGlobal},
	ActionHistoryPrevious:       {ContextChat},
	ActionHistoryNext:           {ContextChat},
	ActionHistorySearch:         {ContextChat},
	ActionHistorySearchNext:     {ContextHistorySearch},
	ActionHistorySearchAccept:   {ContextHistorySearch},
	ActionHistorySearchCancel:   {ContextHistorySearch},
	ActionHistorySearchExecute:  {ContextHistorySearch},
	ActionChatCancel:            {ContextChat},
	ActionChatCycleMode:         {ContextChat},
	ActionChatModelPicker:       {ContextChat},
	ActionChatSubmit:            {ContextChat},
	ActionChatNewline:           {ContextChat},
	ActionChatUndo:              {ContextChat},
	ActionChatExternalEditor:    {ContextChat},
	ActionChatImagePaste:        {ContextChat},
	ActionChatRewriteMessage:    {ContextChat},
	ActionChatPreviousAgent:     {ContextChat},
	ActionChatNextAgent:         {ContextChat},
	ActionAutocompleteAccept:    {ContextAutocomplete},
	ActionAutocompleteDismiss:   {ContextAutocomplete},
	ActionAutocompletePrev:      {ContextAutocomplete},
	ActionAutocompleteNext:      {ContextAutocomplete},
	ActionTranscriptExit:        {ContextTranscript},
	ActionTranscriptSearch:      {ContextTranscript},
	ActionTranscriptToggleRaw:   {ContextTranscript},
	ActionTaskBackground:        {ContextChat},
	ActionTaskClose:             {ContextTask},
	ActionTaskRefresh:           {ContextTask},
	ActionHelpDismiss:           {ContextHelp},
	ActionScrollPageUp:          {ContextScroll},
	ActionScrollPageDown:        {ContextScroll},
	ActionScrollHalfUp:          {ContextScroll},
	ActionScrollHalfDown:        {ContextScroll},
	ActionScrollLineUp:          {ContextScroll, ContextHelp, ContextTranscript, ContextTask},
	ActionScrollLineDown:        {ContextScroll, ContextHelp, ContextTranscript, ContextTask},
	ActionScrollTop:             {ContextScroll},
	ActionScrollBottom:          {ContextScroll},
	ActionSelectionCopy:         {ContextScroll},
	ActionMessageSelectorUp:     {ContextMessageSelector},
	ActionMessageSelectorDown:   {ContextMessageSelector},
	ActionMessageSelectorTop:    {ContextMessageSelector},
	ActionMessageSelectorBottom: {ContextMessageSelector},
	ActionMessageSelectorSelect: {ContextMessageSelector},
	ActionMessageSelectorCancel: {ContextMessageSelector},
}

// SupportsAction reports whether an action is implemented in the requested
// context. This prevents config from making a scaffold look reachable.
func SupportsAction(context Context, action Action) bool {
	for _, supported := range SupportedActionContexts[action] {
		if supported == context {
			return true
		}
	}
	return false
}
