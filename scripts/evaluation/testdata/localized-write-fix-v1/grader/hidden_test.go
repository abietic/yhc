package greet

import "testing"

func TestHiddenGreetingCases(t *testing.T) {
	for name, want := range map[string]string{"": "hello, ", "李": "hello, 李"} {
		if got := Greeting(name); got != want {
			t.Fatalf("Greeting(%q) = %q, want %q", name, got, want)
		}
	}
}
