package tui

import (
	"strings"

	"github.com/charmbracelet/x/ansi"
)

// HistoryRenderMode selects one projection of a semantic conversation item.
type HistoryRenderMode uint8

const (
	HistoryRenderRich HistoryRenderMode = iota
	HistoryRenderRaw
	HistoryRenderCompact
	HistoryRenderExpanded
	HistoryRenderTranscript
)

// HistoryRenderContext contains every input that may change rendered output.
// Width is part of the ChatView cache key; renderers must not read viewport
// dimensions from global state.
type HistoryRenderContext struct {
	Width       int
	Styles      Styles // legacy compatibility; Environment is authoritative.
	Environment RenderEnvironment
	Mode        HistoryRenderMode
	selection   bool
}

func (c HistoryRenderContext) normalized() HistoryRenderContext {
	if c.Width < 1 {
		c.Width = 1
	}
	if c.Environment.profile.id == "" {
		c.Environment = defaultRenderEnvironment(c.Styles)
	} else {
		c.Environment = c.Environment.normalized()
	}
	c.Styles = c.Environment.styles
	return c
}

func (c HistoryRenderContext) displayCellProfile() DisplayCellProfile {
	return c.normalized().Environment.profile
}

// HistoryItem is the semantic unit stored by the conversation renderer.
//
// ID is stable for the lifetime of one transcript projection. Version must
// advance after every mutation that changes output. Finished permits the
// viewport cache to freeze an item until either version or width changes. Raw
// is copy-friendly and free of terminal control sequences. Height must equal
// the number of rows produced by Render for the same normalized context.
type HistoryItem interface {
	ID() string
	Version() uint64
	Finished() bool
	Render(HistoryRenderContext) string
	Raw(HistoryRenderContext) string
	Height(HistoryRenderContext) int
}

// HistoryRawItem overrides the copy-friendly projection of a legacy ChatItem.
type HistoryRawItem interface {
	RenderRaw(HistoryRenderContext) string
}

// HistoryCompactItem supplies a bounded summary projection.
type HistoryCompactItem interface {
	RenderCompact(HistoryRenderContext) string
}

// HistoryExpandedItem supplies a full projection and interactive expansion
// state. ToggleExpanded returns the new state.
type HistoryExpandedItem interface {
	RenderExpanded(HistoryRenderContext) string
	Expanded() bool
	ToggleExpanded() bool
	ExpandedContent() (title, content string)
}

// HistoryTranscriptItem supplies the representation used by a transcript
// overlay. It may be more complete than the normal or expanded viewport row.
type HistoryTranscriptItem interface {
	RenderTranscript(HistoryRenderContext) string
}

// HistoryNestedItem exposes semantic children without flattening them into the
// parent transcript.
type HistoryNestedItem interface {
	NestedHistoryItems() []HistoryItem
}

// HistorySelectableItem describes whether and where copy selection may begin.
type HistorySelectableItem interface {
	Selectable() bool
	NoSelectPrefix() int
}

// HistoryAnimatableItem lets ChatView invalidate only a visible, active item.
// PrepareHistoryAnimation installs the frame used by Render; the returned
// animation version is folded into the ordinary item version cache key.
type HistoryAnimatableItem interface {
	PrepareHistoryAnimation(frame uint64)
	HistoryAnimationVersion(frame uint64) uint64
}

// legacyHistoryItem incrementally adapts the existing ChatItem contract. New
// semantic renderers can implement HistoryItem directly and use
// ChatView.AppendHistoryItem.
type legacyHistoryItem struct {
	id   string
	item ChatItem
}

func adaptChatItem(id string, item ChatItem) HistoryItem {
	return &legacyHistoryItem{id: id, item: item}
}

func (a *legacyHistoryItem) ID() string      { return a.id }
func (a *legacyHistoryItem) Version() uint64 { return a.item.Version() }
func (a *legacyHistoryItem) Finished() bool  { return a.item.Finished() }

func (a *legacyHistoryItem) Render(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	switch ctx.Mode {
	case HistoryRenderRaw:
		return a.Raw(ctx)
	case HistoryRenderCompact:
		if item, ok := a.item.(HistoryCompactItem); ok {
			return item.RenderCompact(ctx)
		}
	case HistoryRenderExpanded:
		if item, ok := a.item.(HistoryExpandedItem); ok {
			return item.RenderExpanded(ctx)
		}
	case HistoryRenderTranscript:
		if item, ok := a.item.(HistoryTranscriptItem); ok {
			return item.RenderTranscript(ctx)
		}
		return a.Raw(ctx)
	}
	if item, ok := a.item.(interface {
		RenderWithEnvironment(int, RenderEnvironment) string
	}); ok {
		return item.RenderWithEnvironment(ctx.Width, ctx.Environment)
	}
	return a.item.Render(ctx.Width, ctx.Styles)
}

func (a *legacyHistoryItem) Raw(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	if item, ok := a.item.(HistoryRawItem); ok {
		return ansi.Strip(item.RenderRaw(ctx))
	}
	return ansi.Strip(a.item.Render(ctx.Width, ctx.Styles))
}

func (a *legacyHistoryItem) Height(ctx HistoryRenderContext) int {
	return historyRenderedHeight(a.Render(ctx))
}

// The adapter exposes safe projections for every optional capability so M4
// consumers can work uniformly during migration. Mutating capabilities remain
// no-ops when the wrapped legacy item does not implement them.
func (a *legacyHistoryItem) RenderCompact(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	if item, ok := a.item.(HistoryCompactItem); ok {
		return item.RenderCompact(ctx)
	}
	return a.item.Render(ctx.Width, ctx.Styles)
}

func (a *legacyHistoryItem) RenderExpanded(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	if item, ok := a.item.(HistoryExpandedItem); ok {
		return item.RenderExpanded(ctx)
	}
	return a.item.Render(ctx.Width, ctx.Styles)
}

func (a *legacyHistoryItem) Expanded() bool {
	item, ok := a.item.(HistoryExpandedItem)
	return ok && item.Expanded()
}

func (a *legacyHistoryItem) ToggleExpanded() bool {
	if item, ok := a.item.(HistoryExpandedItem); ok {
		return item.ToggleExpanded()
	}
	return false
}

func (a *legacyHistoryItem) ExpandedContent() (string, string) {
	if item, ok := a.item.(HistoryExpandedItem); ok {
		return item.ExpandedContent()
	}
	return "", a.Raw(HistoryRenderContext{})
}

func (a *legacyHistoryItem) RenderTranscript(ctx HistoryRenderContext) string {
	ctx = ctx.normalized()
	if item, ok := a.item.(HistoryTranscriptItem); ok {
		return item.RenderTranscript(ctx)
	}
	return a.Raw(ctx)
}

func (a *legacyHistoryItem) NestedHistoryItems() []HistoryItem {
	if item, ok := a.item.(HistoryNestedItem); ok {
		return item.NestedHistoryItems()
	}
	return nil
}

func (a *legacyHistoryItem) Selectable() bool {
	if item, ok := a.item.(HistorySelectableItem); ok {
		return item.Selectable()
	}
	return true
}

func (a *legacyHistoryItem) NoSelectPrefix() int {
	return max(0, a.item.NoSelectPrefix())
}

func (a *legacyHistoryItem) PrepareHistoryAnimation(frame uint64) {
	if item, ok := a.item.(HistoryAnimatableItem); ok {
		item.PrepareHistoryAnimation(frame)
	}
}

func (a *legacyHistoryItem) HistoryAnimationVersion(frame uint64) uint64 {
	if item, ok := a.item.(HistoryAnimatableItem); ok {
		return item.HistoryAnimationVersion(frame)
	}
	return a.Version()
}

// semanticChatItem lets new HistoryItem implementations coexist with the
// legacy ChatView slice while M4 migrates tool families incrementally.
type semanticChatItem struct {
	item HistoryItem
}

func historyCapabilitySource(item ChatItem) any {
	if semantic, ok := item.(*semanticChatItem); ok {
		return semantic.item
	}
	return item
}

func (a *semanticChatItem) Render(width int, styles Styles) string {
	return renderHistoryItem(a.item, HistoryRenderContext{
		Width:  width,
		Styles: styles,
		Mode:   HistoryRenderRich,
	})
}

func (a *semanticChatItem) Finished() bool  { return a.item.Finished() }
func (a *semanticChatItem) Version() uint64 { return a.item.Version() }

func (a *semanticChatItem) NoSelectPrefix() int {
	if item, ok := a.item.(HistorySelectableItem); ok && item.Selectable() {
		return max(0, item.NoSelectPrefix())
	}
	return 0
}

func renderHistoryItem(item HistoryItem, ctx HistoryRenderContext) string {
	if item == nil {
		return ""
	}
	ctx = ctx.normalized()
	switch ctx.Mode {
	case HistoryRenderRaw:
		return item.Raw(ctx)
	case HistoryRenderCompact:
		if item, ok := item.(HistoryCompactItem); ok {
			return item.RenderCompact(ctx)
		}
	case HistoryRenderExpanded:
		if item, ok := item.(HistoryExpandedItem); ok {
			return item.RenderExpanded(ctx)
		}
	case HistoryRenderTranscript:
		if item, ok := item.(HistoryTranscriptItem); ok {
			return item.RenderTranscript(ctx)
		}
		return item.Raw(ctx)
	}
	return item.Render(ctx)
}

func historyRenderedHeight(rendered string) int {
	// ChatView has always represented an empty item as one empty row. Preserve
	// that behavior so adopting the semantic contract cannot shift offsets.
	return strings.Count(rendered, "\n") + 1
}
