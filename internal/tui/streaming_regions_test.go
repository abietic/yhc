package tui

import (
	"strings"
	"testing"
)

func TestStreamingMarkdownCommitsCompleteBlocksAndKeepsMutableTail(t *testing.T) {
	t.Parallel()

	source := "# Title\n\nParagraph one.\n\n- first\n- second\n\n"
	stream := &StreamingMarkdown{}
	stream.Render(source, 72, ThemePolarNight)

	wantStable := "# Title\n\nParagraph one.\n\n"
	if stream.stablePrefix != wantStable {
		t.Fatalf("stable prefix = %q, want %q", stream.stablePrefix, wantStable)
	}
	if tail := source[len(stream.stablePrefix):]; tail != "- first\n- second\n\n" {
		t.Fatalf("mutable tail = %q", tail)
	}
	if !strings.HasSuffix(stream.stablePrefix, "\n") {
		t.Fatalf("stable region is not newline-complete: %q", stream.stablePrefix)
	}

	source += "After list"
	stream.Render(source, 72, ThemePolarNight)
	if !strings.Contains(stream.stablePrefix, "- second\n\n") {
		t.Fatalf("closed list was not promoted: %q", stream.stablePrefix)
	}
	if tail := source[len(stream.stablePrefix):]; tail != "After list" {
		t.Fatalf("new paragraph should remain mutable, got %q", tail)
	}
}

func TestStreamingMarkdownHoldsTableUntilFollowingBlock(t *testing.T) {
	t.Parallel()

	prefix := "Before table.\n\n"
	table := "| Name | State |\n| --- | --- |\n| alpha | ready |\n"
	stream := &StreamingMarkdown{}
	stream.Render(prefix+table+"\n", 72, ThemePolarNight)

	if stream.stablePrefix != prefix {
		t.Fatalf("active table leaked into stable region: %q", stream.stablePrefix)
	}

	source := prefix + table + "\nAfter table"
	stream.Render(source, 72, ThemePolarNight)
	if !strings.Contains(stream.stablePrefix, "| alpha | ready |\n\n") {
		t.Fatalf("closed table was not promoted: %q", stream.stablePrefix)
	}
	if tail := source[len(stream.stablePrefix):]; tail != "After table" {
		t.Fatalf("following block should be mutable, got %q", tail)
	}
}

func TestStreamingMarkdownHoldsFenceUntilFollowingBlock(t *testing.T) {
	t.Parallel()

	prefix := "Before code.\n\n"
	fence := "```go\nfmt.Println(\"ok\")\n```\n"
	stream := &StreamingMarkdown{}
	stream.Render(prefix+fence+"\n", 72, ThemePolarNight)
	if stream.stablePrefix != prefix {
		t.Fatalf("active fence leaked into stable region: %q", stream.stablePrefix)
	}

	source := prefix + fence + "\nAfter code"
	stream.Render(source, 72, ThemePolarNight)
	if !strings.Contains(stream.stablePrefix, "```\n\n") {
		t.Fatalf("closed fence was not promoted: %q", stream.stablePrefix)
	}
	if tail := source[len(stream.stablePrefix):]; tail != "After code" {
		t.Fatalf("following block should be mutable, got %q", tail)
	}
}

func TestStreamingMarkdownWidthChangeRerendersFromSource(t *testing.T) {
	t.Parallel()

	source := "# Width\n\nThis paragraph is deliberately long enough to wrap at the narrow width.\n\nMutable tail"
	stream := &StreamingMarkdown{}
	wide := stream.Render(source, 72, ThemePolarNight)
	narrow := stream.Render(source, 24, ThemePolarNight)
	freshNarrow := (&StreamingMarkdown{}).Render(source, 24, ThemePolarNight)

	if wide == narrow {
		t.Fatal("width change did not alter rendered output")
	}
	if narrow != freshNarrow {
		t.Fatal("width change reused rendered fragments instead of rerendering source")
	}
	if stream.width != 24 {
		t.Fatalf("cached width = %d, want 24", stream.width)
	}
}

func TestStreamingMarkdownFinalizeUsesCanonicalSourceRender(t *testing.T) {
	t.Parallel()

	source := "# Final\n\n- one\n- two\n\n| A | B |\n| --- | --- |\n| 1 | 2 |\n"
	stream := &StreamingMarkdown{}
	stream.Render(source, 60, ThemePolarNight)
	if stream.stablePrefix == "" {
		t.Fatal("test requires a populated streaming prefix")
	}

	stream.Finalize(source)
	got := stream.Render(source, 60, ThemePolarNight)
	fresh := &StreamingMarkdown{}
	fresh.Finalize(source)
	want := fresh.Render(source, 60, ThemePolarNight)
	if got != want {
		t.Fatal("finalized render does not match one canonical source render")
	}
	if stream.stablePrefix != "" || stream.stableRender != "" {
		t.Fatalf("finalized stream retained stitched regions: prefix=%q", stream.stablePrefix)
	}
	if !stream.finalized {
		t.Fatal("stream was not marked finalized")
	}

	resized := stream.Render(source, 28, ThemePolarNight)
	freshResizedStream := &StreamingMarkdown{}
	freshResizedStream.Finalize(source)
	freshResized := freshResizedStream.Render(source, 28, ThemePolarNight)
	if resized != freshResized {
		t.Fatal("finalized resize did not rerender canonical source")
	}
}

func TestStreamingMarkdownReferenceLinksRemainOneRegion(t *testing.T) {
	t.Parallel()

	referenceSource := "[documentation][docs]\n\nMutable tail\n\n[docs]: https://example.com"
	referenceStream := &StreamingMarkdown{}
	referenceStream.Render(referenceSource, 72, ThemePolarNight)
	if referenceStream.stablePrefix != "" {
		t.Fatalf("global reference link crossed a render region: %q", referenceStream.stablePrefix)
	}

	inlineSource := "[documentation](https://example.com)\n\nMutable tail"
	inlineStream := &StreamingMarkdown{}
	inlineStream.Render(inlineSource, 72, ThemePolarNight)
	if inlineStream.stablePrefix != "[documentation](https://example.com)\n\n" {
		t.Fatalf("inline link should permit a stable block, got %q", inlineStream.stablePrefix)
	}
}

func TestStreamingMarkdownSourceReplacementResetsRegions(t *testing.T) {
	t.Parallel()

	stream := &StreamingMarkdown{}
	stream.Render("# First\n\nMutable", 72, ThemePolarNight)
	stream.Render("# Second\n\nReplacement", 72, ThemePolarNight)
	if stream.stablePrefix != "# Second\n\n" {
		t.Fatalf("replacement retained old stable source: %q", stream.stablePrefix)
	}

	source := "# Final\n\nDone"
	stream.Finalize(source)
	stream.Render(source, 72, ThemePolarNight)
	stream.Render(source+"\n\nNew stream", 72, ThemePolarNight)
	if stream.finalized {
		t.Fatal("append after finalization did not start a new streaming lifecycle")
	}
}

func TestChatAssistantFinalizationKeepsOneCanonicalItem(t *testing.T) {
	t.Parallel()

	chat := NewChatView(defaultStyles())
	chat.StreamAssistantDelta("draft ")
	chat.StreamAssistantDelta("answer")
	chat.AppendOrUpdateAssistant("canonical answer")
	chat.FinishAssistant()

	if len(chat.items) != 1 {
		t.Fatalf("assistant stream produced %d transcript items, want 1", len(chat.items))
	}
	message, ok := chat.items[0].(*AssistantMessage)
	if !ok {
		t.Fatalf("item type = %T, want *AssistantMessage", chat.items[0])
	}
	if message.content != "canonical answer" || message.builder.String() != message.content {
		t.Fatalf("canonical source mismatch: content=%q builder=%q", message.content, message.builder.String())
	}
	if !message.finished || !message.streamingMd.finalized {
		t.Fatal("assistant item did not finalize its markdown lifecycle")
	}

	chat.StreamAssistantDelta("next stream")
	if len(chat.items) != 2 {
		t.Fatalf("new stream reused finalized item; item count = %d", len(chat.items))
	}
}
