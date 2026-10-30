package appserver

import "testing"

func TestWebAssetPathsServeTimelineRendererModule(t *testing.T) {
	t.Parallel()

	if got, want := webAssetPaths["/timeline_view.mjs"], "assets/timeline_view.mjs"; got != want {
		t.Fatalf("timeline module path = %q, want %q", got, want)
	}
}
