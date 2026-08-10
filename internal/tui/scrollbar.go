package tui

import "charm.land/lipgloss/v2"

// renderScrollbar renders a vertical scrollbar as a single-column string.
// Returns empty string if all content fits in the viewport.
func renderScrollbar(height, contentHeight, viewportHeight, offsetRow int, styles Styles) string {
	if contentHeight <= viewportHeight || height <= 0 {
		return ""
	}

	thumbSize := height * viewportHeight / contentHeight
	if thumbSize < 1 {
		thumbSize = 1
	}

	maxOffset := contentHeight - viewportHeight
	if maxOffset <= 0 {
		return ""
	}

	trackSpace := height - thumbSize
	thumbPos := 0
	if trackSpace > 0 && offsetRow > 0 {
		thumbPos = offsetRow * trackSpace / maxOffset
		if thumbPos > trackSpace {
			thumbPos = trackSpace
		}
	}

	thumbStyle := lipgloss.NewStyle().Foreground(styles.ScrollThumb)
	trackStyle := lipgloss.NewStyle().Foreground(styles.ScrollTrack)

	var sb []byte
	for i := 0; i < height; i++ {
		if i > 0 {
			sb = append(sb, '\n')
		}
		if i >= thumbPos && i < thumbPos+thumbSize {
			sb = append(sb, []byte(thumbStyle.Render("┃"))...)
		} else {
			sb = append(sb, []byte(trackStyle.Render("│"))...)
		}
	}
	return string(sb)
}

// TotalContentHeight returns the total height of all items in the chat
// (sum of rendered heights + gaps). Cached via render cache.
func (c *ChatView) TotalContentHeight() int {
	if len(c.items) == 0 {
		return 0
	}
	rw := c.renderWidth()
	total := 0
	for idx, item := range c.items {
		entry := c.renderItem(item, rw)
		total += entry.height
		if idx < len(c.items)-1 {
			total++ // gap between items
		}
	}
	return total
}
