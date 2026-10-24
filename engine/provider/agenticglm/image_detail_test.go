package agenticglm

import (
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestImageDetailDefaultsAndExplicitQuality(t *testing.T) {
	for _, detail := range []schema.ImageURLDetail{"", "auto", "low", "high", "original", "unknown"} {
		t.Run(string(detail), func(t *testing.T) {
			image := &schema.UserInputImage{Base64Data: "aW1hZ2U=", MIMEType: "image/png", Detail: detail}
			part, err := convertImage(image)
			if detail == "" || detail == "auto" {
				if err != nil || part.ImageURL == nil || part.ImageURL.URL != "data:image/png;base64,aW1hZ2U=" {
					t.Fatalf("neutral detail image = %#v, %v", part, err)
				}
			} else if err == nil || err.Error() != "image_detail_unsupported" {
				t.Fatalf("explicit detail %q: %v", detail, err)
			}
			if image.Detail != detail {
				t.Fatal("conversion mutated the caller's image detail")
			}
		})
	}
}
