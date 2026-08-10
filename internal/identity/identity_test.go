package identity

import "testing"

func TestCanonicalIdentityConstants(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{name: "product", got: ProductName, want: "YHC"},
		{name: "long product", got: ProductLongName, want: "YHC — Yet Hooked on Coding"},
		{name: "command", got: CommandName, want: "yhc"},
		{name: "module", got: ModulePath, want: "github.com/abietic/yhc"},
		{name: "project directory", got: ProjectDirName, want: ".yhc"},
		{name: "legacy directory", got: LegacyDirName, want: ".eino-agent"},
		{name: "legacy command", got: LegacyCommandName, want: "eino-agent"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("identity constant = %q, want %q", test.got, test.want)
			}
		})
	}
}
