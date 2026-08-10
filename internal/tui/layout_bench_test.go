package tui

import (
	"fmt"
	"strings"
	"testing"
)

func BenchmarkRenderLayoutBands(b *testing.B) {
	layout := calculateLayout(layoutRequest{
		totalWidth: 120, totalHeight: 40,
		editorContentRows: 3, hintHeight: 8, taskTreeHeight: 5,
		contextHeight: 1, spinnerVisible: true, editorVisible: true,
	})
	chat := strings.Repeat("chat content\n", layout.chatRect.Height)
	activity := strings.Repeat("activity\n", layout.activityRect.Height)
	hints := strings.Repeat("hint\n", layout.hintRect.Height)
	editor := strings.Repeat("editor\n", layout.editorRect.Height)

	b.ResetTimer()
	for range b.N {
		renderLayoutBands(DefaultDisplayCellProfile(), 120, 40,
			layoutBand{rect: layout.headerRect, content: "search"},
			layoutBand{rect: layout.chatRect, content: chat, alignBottom: true},
			layoutBand{rect: layout.activityRect, content: activity},
			layoutBand{rect: layout.hintRect, content: hints},
			layoutBand{rect: layout.editorRect, content: editor},
			layoutBand{rect: layout.statusRect, content: "status"},
		)
	}
}

func BenchmarkAppViewExplicitLayout(b *testing.B) {
	app := newTestApp(120, 40)
	for i := 0; i < 100; i++ {
		app.chat.AppendUser(fmt.Sprintf("prompt %d", i))
		app.chat.AppendOrUpdateAssistant("A cached assistant response with **Markdown** content.")
		app.chat.FinishAssistant()
	}
	app.updateLayout()
	app.renderView()

	b.ResetTimer()
	for range b.N {
		app.renderView()
	}
}
