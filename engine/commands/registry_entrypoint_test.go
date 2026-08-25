package commands

import "testing"

func TestAppServerEntrypointReusesACPCommandCapabilitySet(t *testing.T) {
	tests := []struct {
		name string
		set  EntrypointSet
		want bool
	}{
		{name: "ACP command", set: EntrypointsACP, want: true},
		{name: "plain only", set: EntrypointsPlain, want: false},
		{name: "headless only", set: EntrypointsHeadless, want: false},
		{name: "none", set: EntrypointsNone, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.set.Supports(EntrypointAppServer); got != test.want {
				t.Fatalf("Supports(app-server) = %v, want %v", got, test.want)
			}
		})
	}
}
