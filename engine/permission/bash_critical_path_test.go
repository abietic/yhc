package permission

import "testing"

func TestP512ClassifyBashCriticalPathLiteralCorpus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command string
		cwd     string
		home    string
		match   bool
		reason  string
	}{
		{name: "root", command: "rm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "root after end options", command: "rm -rf -- /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "root through relative parent", command: "rm -rf ..", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "root child", command: "rmdir /etc", cwd: "/work", home: "/home/u", match: true, reason: "root_child"},
		{name: "relative root child", command: "rm etc", cwd: "/", home: "/home/u", match: true, reason: "root_child"},
		{name: "volume root", command: "rm -rf /Volumes/Data", cwd: "/work", home: "/Users/u", match: true, reason: "volume_root"},
		{name: "volume child", command: "rm -rf /Volumes/Data/child", cwd: "/work", home: "/Users/u", match: true, reason: "volume_child"},
		{name: "home", command: "rm -rf /home/u", cwd: "/work", home: "/home/u", match: true, reason: "home"},
		{name: "home relative", command: "rm -rf u", cwd: "/home", home: "/home/u", match: true, reason: "home"},
		{name: "tilde home", command: "rm -rf ~", cwd: "/work", home: "/home/u", match: true, reason: "home"},
		{name: "current all entries", command: "rm -rf *", cwd: "/work", home: "/home/u", match: true, reason: "all_entries"},
		{name: "absolute all entries", command: "rm -rf /tmp/*", cwd: "/work", home: "/home/u", match: true, reason: "all_entries"},
		{name: "parent entries", command: "rm -rf ../*", cwd: "/work/repo", home: "/home/u", match: true, reason: "all_entries"},
		{name: "command wrapper", command: "command rm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "builtin wrapper", command: "builtin rmdir /etc", cwd: "/work", home: "/home/u", match: true, reason: "root_child"},
		{name: "ordinary quoting", command: "rm -rf '/etc'", cwd: "/work", home: "/home/u", match: true, reason: "root_child"},
		{name: "ordinary escape", command: `rm -rf /et\c`, cwd: "/work", home: "/home/u", match: true, reason: "root_child"},
		{name: "semicolon segment", command: "printf ok; rm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "newline segment", command: "printf ok\nrm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "and segment", command: "printf ok && rm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "or segment", command: "false || rm -rf /", cwd: "/work", home: "/home/u", match: true, reason: "root"},
		{name: "multiple targets", command: "rm -rf /work/file /etc", cwd: "/work", home: "/home/u", match: true, reason: "root_child"},
		{name: "non critical nested path", command: "rm -rf /etc/config/file", cwd: "/work", home: "/home/u"},
		{name: "non critical home child", command: "rm -rf ~/child", cwd: "/work", home: "/home/u"},
		{name: "echo is not rm", command: "echo rm /", cwd: "/work", home: "/home/u"},
		{name: "quoted tilde does not expand", command: `rm -rf "~"`, cwd: "/work", home: "/home/u"},
		{name: "quoted wildcard does not expand", command: `rm -rf "/tmp/*"`, cwd: "/work", home: "/home/u"},
		{name: "escaped wildcard does not expand", command: `rm -rf /tmp/\*`, cwd: "/work", home: "/home/u"},
		{name: "unsupported wildcard", command: "rm -rf /tmp/**", cwd: "/work", home: "/home/u"},
		{name: "unsupported prefix wildcard", command: "rm -rf /tmp/child*", cwd: "/work", home: "/home/u"},
		{name: "variable expansion", command: "rm -rf $ROOT", cwd: "/work", home: "/home/u"},
		{name: "command substitution", command: "rm -rf $(printf /)", cwd: "/work", home: "/home/u"},
		{name: "backtick substitution", command: "rm -rf `printf /`", cwd: "/work", home: "/home/u"},
		{name: "pipeline", command: "printf x | rm -rf /", cwd: "/work", home: "/home/u"},
		{name: "redirection", command: "rm -rf / >/tmp/log", cwd: "/work", home: "/home/u"},
		{name: "background", command: "rm -rf / &", cwd: "/work", home: "/home/u"},
		{name: "subshell", command: "(rm -rf /)", cwd: "/work", home: "/home/u"},
		{name: "malformed quote", command: "rm -rf '/", cwd: "/work", home: "/home/u"},
		{name: "malformed separator", command: "rm -rf / &&", cwd: "/work", home: "/home/u"},
		{name: "relative cwd", command: "rm -rf /", cwd: "work", home: "/home/u"},
		{name: "relative home", command: "rm -rf /", cwd: "/work", home: "home/u"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := ClassifyBashCriticalPath(test.command, test.cwd, test.home)
			if got.Match != test.match || got.ReasonCode != test.reason {
				t.Fatalf("decision = %#v, want match=%v reason=%q", got, test.match, test.reason)
			}
		})
	}
}
