package engine

import "testing"

// These ordered target types deliberately exist only in tests. Their distinct
// marker methods make untrusted ingress parts and admitted durable parts
// non-interchangeable; they are a compile-time sketch, not a shipped API.
type p30UntrustedPart interface {
	isP30UntrustedPart()
}

type p30UntrustedText struct {
	Text string
}

func (p30UntrustedText) isP30UntrustedPart() {}

type p30UntrustedImage struct {
	Base64Data string
	MIMEType   string
}

func (p30UntrustedImage) isP30UntrustedPart() {}

type p30AdmittedPart interface {
	isP30AdmittedPart()
}

type p30AdmittedText struct {
	Text string
}

func (p30AdmittedText) isP30AdmittedPart() {}

type p30AdmittedMedia struct {
	MediaRef string
}

func (p30AdmittedMedia) isP30AdmittedPart() {}

func TestP300TargetTypesKeepUntrustedAndAdmittedPartsDistinctAndOrdered(t *testing.T) {
	untrusted := []p30UntrustedPart{
		p30UntrustedText{Text: "before"},
		p30UntrustedImage{Base64Data: "cG5n", MIMEType: "image/png"},
		p30UntrustedText{Text: "after"},
	}
	admitted := []p30AdmittedPart{
		p30AdmittedText{Text: "before"},
		p30AdmittedMedia{MediaRef: "media-test-only"},
		p30AdmittedText{Text: "after"},
	}

	if len(untrusted) != 3 || len(admitted) != 3 {
		t.Fatalf("untrusted=%#v admitted=%#v", untrusted, admitted)
	}
	if _, ok := untrusted[1].(p30UntrustedImage); !ok {
		t.Fatalf("untrusted order = %#v", untrusted)
	}
	if _, ok := admitted[1].(p30AdmittedMedia); !ok {
		t.Fatalf("admitted order = %#v", admitted)
	}
}
