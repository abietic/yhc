package permission

import (
	"regexp"
	"strings"
)

// DangerousPattern defines a regex-based pattern for detecting risky shell
// commands that should require user confirmation even in auto-mode.
// Mirrors the dangerous command detection concept from dangerousPatterns.ts,
// extended to cover runtime command-level safety checks.
type DangerousPattern struct {
	// Pattern is the regex pattern to match against commands.
	Pattern *regexp.Regexp
	// ExcludePattern is an optional pattern that, if matched, negates the match.
	// This replaces negative lookaheads which Go's regexp doesn't support.
	ExcludePattern *regexp.Regexp
	// Description explains why this pattern is dangerous.
	Description string
	// Severity is how dangerous this is: "high", "medium", "low".
	Severity string
	// Category groups related patterns: "destructive", "network", "privilege", "exfiltration",
	// "git", "process", "package", "database", "credential".
	Category string
}

// Severity constants for DangerousPattern.
const (
	SeverityHigh   = "high"
	SeverityMedium = "medium"
	SeverityLow    = "low"
)

// Category constants for DangerousPattern.
const (
	CategoryDestructive  = "destructive"
	CategoryNetwork      = "network"
	CategoryPrivilege    = "privilege"
	CategoryExfiltration = "exfiltration"
	CategoryGit          = "git"
	CategoryProcess      = "process"
	CategoryPackage      = "package"
	CategoryDatabase     = "database"
	CategoryCredential   = "credential"
)

// DefaultDangerousPatterns contains the built-in set of dangerous command patterns.
// These patterns are checked against Bash tool invocations to flag commands that
// could cause irreversible damage or security issues.
var DefaultDangerousPatterns = []DangerousPattern{
	// === Destructive commands ===
	{
		Pattern:     regexp.MustCompile(`\brm\s+(-[^\s]*)?r`),
		Description: "Recursive file deletion (rm -r / rm -rf)",
		Severity:    SeverityHigh,
		Category:    CategoryDestructive,
	},
	{
		Pattern:     regexp.MustCompile(`\brm\s+(-[^\s]*)?\s*/`),
		Description: "Deletion targeting root or absolute paths",
		Severity:    SeverityHigh,
		Category:    CategoryDestructive,
	},
	{
		Pattern:     regexp.MustCompile(`\bmkfs\b`),
		Description: "Filesystem formatting (mkfs)",
		Severity:    SeverityHigh,
		Category:    CategoryDestructive,
	},
	{
		Pattern:     regexp.MustCompile(`\bdd\s+if=`),
		Description: "Direct disk write (dd)",
		Severity:    SeverityHigh,
		Category:    CategoryDestructive,
	},
	{
		Pattern:     regexp.MustCompile(`>\s*/dev/`),
		Description: "Write to device file (> /dev/)",
		Severity:    SeverityHigh,
		Category:    CategoryDestructive,
	},
	{
		Pattern:     regexp.MustCompile(`\bformat\b`),
		Description: "Disk format command",
		Severity:    SeverityHigh,
		Category:    CategoryDestructive,
	},

	// === Network commands (remote code execution) ===
	{
		Pattern:     regexp.MustCompile(`\bcurl\b.*\|\s*(ba)?sh`),
		Description: "Piping curl output to shell (remote code execution)",
		Severity:    SeverityHigh,
		Category:    CategoryNetwork,
	},
	{
		Pattern:     regexp.MustCompile(`\bwget\b.*\|\s*(ba)?sh`),
		Description: "Piping wget output to shell (remote code execution)",
		Severity:    SeverityHigh,
		Category:    CategoryNetwork,
	},
	{
		Pattern:     regexp.MustCompile(`\bnc\s+-[^\s]*l`),
		Description: "Netcat listen mode (potential backdoor)",
		Severity:    SeverityHigh,
		Category:    CategoryNetwork,
	},
	{
		Pattern:     regexp.MustCompile(`\bncat\b`),
		Description: "Ncat network utility (potential backdoor)",
		Severity:    SeverityMedium,
		Category:    CategoryNetwork,
	},
	{
		Pattern:     regexp.MustCompile(`/dev/tcp/`),
		Description: "Bash /dev/tcp reverse shell",
		Severity:    SeverityHigh,
		Category:    CategoryNetwork,
	},
	{
		Pattern:     regexp.MustCompile(`\bmkfifo\b.*\bnc\b`),
		Description: "Named pipe with netcat (reverse shell pattern)",
		Severity:    SeverityHigh,
		Category:    CategoryNetwork,
	},

	// === Privilege escalation ===
	{
		Pattern:     regexp.MustCompile(`\bsudo\b`),
		Description: "Privilege escalation via sudo",
		Severity:    SeverityMedium,
		Category:    CategoryPrivilege,
	},
	{
		Pattern:     regexp.MustCompile(`\bsu\s+-`),
		Description: "Switch user (su -)",
		Severity:    SeverityMedium,
		Category:    CategoryPrivilege,
	},
	{
		Pattern:     regexp.MustCompile(`\bchmod\s+777\b`),
		Description: "Setting world-writable permissions (chmod 777)",
		Severity:    SeverityMedium,
		Category:    CategoryPrivilege,
	},
	{
		Pattern:     regexp.MustCompile(`\bchown\s+root\b`),
		Description: "Changing ownership to root (chown root)",
		Severity:    SeverityMedium,
		Category:    CategoryPrivilege,
	},

	// === Data exfiltration ===
	{
		Pattern:     regexp.MustCompile(`\bcurl\b.*\s-[^\s]*d\b`),
		Description: "Curl with data upload (-d flag, potential exfiltration)",
		Severity:    SeverityMedium,
		Category:    CategoryExfiltration,
	},
	{
		Pattern:     regexp.MustCompile(`\bcurl\b.*--data`),
		Description: "Curl with --data (potential exfiltration)",
		Severity:    SeverityMedium,
		Category:    CategoryExfiltration,
	},
	{
		Pattern:     regexp.MustCompile(`\bscp\b`),
		Description: "Secure copy to/from remote (scp)",
		Severity:    SeverityMedium,
		Category:    CategoryExfiltration,
	},
	{
		Pattern:     regexp.MustCompile(`\brsync\b.*@`),
		Description: "Rsync to remote host (potential exfiltration)",
		Severity:    SeverityMedium,
		Category:    CategoryExfiltration,
	},
	{
		Pattern:     regexp.MustCompile(`\btar\b.*\|\s*curl`),
		Description: "Tar piped to curl (archive exfiltration)",
		Severity:    SeverityHigh,
		Category:    CategoryExfiltration,
	},

	// === Git destructive operations ===
	{
		Pattern:     regexp.MustCompile(`\bgit\s+push\b.*--force`),
		Description: "Force push (git push --force)",
		Severity:    SeverityHigh,
		Category:    CategoryGit,
	},
	{
		Pattern:     regexp.MustCompile(`\bgit\s+push\b.*-f\b`),
		Description: "Force push shorthand (git push -f)",
		Severity:    SeverityHigh,
		Category:    CategoryGit,
	},
	{
		Pattern:     regexp.MustCompile(`\bgit\s+reset\b.*--hard`),
		Description: "Hard reset (git reset --hard)",
		Severity:    SeverityHigh,
		Category:    CategoryGit,
	},
	{
		Pattern:     regexp.MustCompile(`\bgit\s+clean\b.*-[^\s]*f`),
		Description: "Force clean untracked files (git clean -f)",
		Severity:    SeverityMedium,
		Category:    CategoryGit,
	},

	// === Process/system commands ===
	{
		Pattern:     regexp.MustCompile(`\bkill\s+-9\b`),
		Description: "Force kill process (kill -9)",
		Severity:    SeverityMedium,
		Category:    CategoryProcess,
	},
	{
		Pattern:     regexp.MustCompile(`\bkillall\b`),
		Description: "Kill all matching processes (killall)",
		Severity:    SeverityMedium,
		Category:    CategoryProcess,
	},
	{
		Pattern:     regexp.MustCompile(`\bshutdown\b`),
		Description: "System shutdown",
		Severity:    SeverityHigh,
		Category:    CategoryProcess,
	},
	{
		Pattern:     regexp.MustCompile(`\breboot\b`),
		Description: "System reboot",
		Severity:    SeverityHigh,
		Category:    CategoryProcess,
	},
	{
		Pattern:     regexp.MustCompile(`\bsystemctl\s+stop\b`),
		Description: "Stop system service (systemctl stop)",
		Severity:    SeverityMedium,
		Category:    CategoryProcess,
	},

	// === Package management ===
	{
		Pattern:     regexp.MustCompile(`\bnpm\s+publish\b`),
		Description: "Publish npm package (npm publish)",
		Severity:    SeverityHigh,
		Category:    CategoryPackage,
	},
	{
		Pattern:        regexp.MustCompile(`\bpip\s+install\b`),
		ExcludePattern: regexp.MustCompile(`--user`),
		Description:    "Pip install without --user (system-wide install)",
		Severity:       SeverityLow,
		Category:       CategoryPackage,
	},
	{
		Pattern:     regexp.MustCompile(`\bapt\s+(remove|purge)\b`),
		Description: "Remove system package (apt remove/purge)",
		Severity:    SeverityMedium,
		Category:    CategoryPackage,
	},
	{
		Pattern:     regexp.MustCompile(`\bapt-get\s+(remove|purge)\b`),
		Description: "Remove system package (apt-get remove/purge)",
		Severity:    SeverityMedium,
		Category:    CategoryPackage,
	},

	// === Database operations ===
	{
		Pattern:     regexp.MustCompile(`(?i)\bDROP\s+TABLE\b`),
		Description: "Drop database table (DROP TABLE)",
		Severity:    SeverityHigh,
		Category:    CategoryDatabase,
	},
	{
		Pattern:     regexp.MustCompile(`(?i)\bDROP\s+DATABASE\b`),
		Description: "Drop entire database (DROP DATABASE)",
		Severity:    SeverityHigh,
		Category:    CategoryDatabase,
	},
	{
		Pattern:     regexp.MustCompile(`(?i)\bTRUNCATE\b`),
		Description: "Truncate table data (TRUNCATE)",
		Severity:    SeverityHigh,
		Category:    CategoryDatabase,
	},

	// === Credential access ===
	{
		Pattern:     regexp.MustCompile(`\bcat\b.*\.env\b`),
		Description: "Reading .env file (potential credential exposure)",
		Severity:    SeverityMedium,
		Category:    CategoryCredential,
	},
	{
		Pattern:     regexp.MustCompile(`\bcat\b.*credentials`),
		Description: "Reading credentials file",
		Severity:    SeverityMedium,
		Category:    CategoryCredential,
	},
	{
		Pattern:     regexp.MustCompile(`\bcat\b.*\.ssh/`),
		Description: "Reading SSH keys or config",
		Severity:    SeverityHigh,
		Category:    CategoryCredential,
	},
}

// IsDangerousCommand checks if a shell command matches any dangerous patterns.
// Returns the matching pattern info or nil if safe.
func IsDangerousCommand(command string) *DangerousPattern {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}

	for i := range DefaultDangerousPatterns {
		p := &DefaultDangerousPatterns[i]
		if p.Pattern.MatchString(command) {
			if p.ExcludePattern != nil && p.ExcludePattern.MatchString(command) {
				continue
			}
			return p
		}
	}
	return nil
}

// GetDangerousPatternsByCategory returns all patterns in a given category.
func GetDangerousPatternsByCategory(category string) []DangerousPattern {
	var result []DangerousPattern
	for _, p := range DefaultDangerousPatterns {
		if p.Category == category {
			result = append(result, p)
		}
	}
	return result
}

// destructiveGitPatterns are the specific patterns used by IsDestructiveGitCommand.
// Separated for efficient targeted checking.
var destructiveGitPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bgit\s+push\b.*--force`),
	regexp.MustCompile(`\bgit\s+push\b.*-f\b`),
	regexp.MustCompile(`\bgit\s+reset\b.*--hard`),
	regexp.MustCompile(`\bgit\s+clean\b.*-[^\s]*f`),
	regexp.MustCompile(`\bgit\s+branch\b.*-[^\s]*D\b`),
	regexp.MustCompile(`\bgit\s+rebase\b`),
}

// IsDestructiveGitCommand specifically checks for destructive git operations
// that are hard to reverse (force push, hard reset, etc.)
func IsDestructiveGitCommand(command string) bool {
	command = strings.TrimSpace(command)
	if command == "" {
		return false
	}

	for _, pat := range destructiveGitPatterns {
		if pat.MatchString(command) {
			return true
		}
	}
	return false
}
