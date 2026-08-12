package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

const (
	publicationRepository = "github.com/abietic/yhc"
	publicationBaseline   = "8e34cc4794f0e1e9ae404c5bcf453d5e71a159c0"
)

type Config struct {
	Version      int              `yaml:"version"`
	Source       SourcePolicy     `yaml:"source"`
	Rules        []PathRule       `yaml:"rules"`
	Mappings     MappingPolicy    `yaml:"mappings"`
	Privacy      PrivacyPolicy    `yaml:"privacy"`
	Dependencies DependencyPolicy `yaml:"dependencies"`
}

type SourcePolicy struct {
	Repository     string `yaml:"repository"`
	BaselineCommit string `yaml:"baseline_commit"`
}

type PathRule struct {
	ID       string   `yaml:"id"`
	Include  []string `yaml:"include"`
	Exclude  []string `yaml:"exclude,omitempty"`
	Class    string   `yaml:"class"`
	Decision string   `yaml:"decision"`
	License  string   `yaml:"license,omitempty"`
	Evidence []string `yaml:"evidence"`
}

type MappingPolicy struct {
	Manifest string `yaml:"manifest"`
}

type PrivacyPolicy struct {
	AllowedEmails    []string          `yaml:"allowed_emails,omitempty"`
	AllowedURLHosts  []string          `yaml:"allowed_url_hosts,omitempty"`
	TestSentinels    []string          `yaml:"test_sentinels,omitempty"`
	ReviewedFindings []ReviewedFinding `yaml:"reviewed_findings,omitempty"`
}

type ReviewedFinding struct {
	Path        string `yaml:"path"`
	Line        int    `yaml:"line"`
	RuleID      string `yaml:"rule_id"`
	MatchSHA256 string `yaml:"match_sha256"`
	Purpose     string `yaml:"purpose"`
}

type DependencyPolicy struct {
	LicensePolicy string `yaml:"license_policy"`
}

func loadConfig(data []byte) (Config, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var config Config
	if err := decoder.Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode publication policy: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err != nil {
			return Config{}, fmt.Errorf("decode trailing publication policy document: %w", err)
		}
		return Config{}, errors.New("decode publication policy: multiple YAML documents are not allowed")
	}
	if err := validateConfig(config); err != nil {
		return Config{}, err
	}
	return config, nil
}

func validateConfig(config Config) error {
	if config.Version != 1 {
		return fmt.Errorf("publication policy version must be 1")
	}
	if config.Source.Repository != publicationRepository {
		return fmt.Errorf("publication repository must be %q", publicationRepository)
	}
	if config.Source.BaselineCommit != publicationBaseline {
		return fmt.Errorf("publication baseline commit must be %q", publicationBaseline)
	}
	if err := validateRepositoryPath(config.Mappings.Manifest); err != nil {
		return fmt.Errorf("mapping manifest: %w", err)
	}
	if err := validateRepositoryPath(config.Dependencies.LicensePolicy); err != nil {
		return fmt.Errorf("dependency license policy: %w", err)
	}
	if len(config.Rules) == 0 {
		return errors.New("publication policy must define path rules")
	}
	ids := make(map[string]struct{}, len(config.Rules))
	for _, rule := range config.Rules {
		if !validRuleID(rule.ID) {
			return fmt.Errorf("publication rule ID %q must be a lowercase-hyphen atom", rule.ID)
		}
		if _, ok := ids[rule.ID]; ok {
			return fmt.Errorf("duplicate publication rule ID %q", rule.ID)
		}
		ids[rule.ID] = struct{}{}
		if len(rule.Include) == 0 || len(rule.Evidence) == 0 {
			return fmt.Errorf("publication rule %q requires include and evidence", rule.ID)
		}
		if !validClass(rule.Class) || !validDecision(rule.Decision) {
			return fmt.Errorf("publication rule %q has invalid class or decision", rule.ID)
		}
		patterns := map[string]struct{}{}
		for _, pattern := range append(append([]string{}, rule.Include...), rule.Exclude...) {
			if err := validateRepositoryPathPattern(pattern); err != nil {
				return fmt.Errorf("publication rule %q: %w", rule.ID, err)
			}
			if _, exists := patterns[pattern]; exists {
				return fmt.Errorf("publication rule %q repeats pattern %q", rule.ID, pattern)
			}
			patterns[pattern] = struct{}{}
		}
		for _, pattern := range rule.Include {
			if pattern == "*" || pattern == "**" {
				return fmt.Errorf("publication rule %q has forbidden whole-repository include %q", rule.ID, pattern)
			}
		}
		evidence := map[string]struct{}{}
		for _, item := range rule.Evidence {
			if item == "" {
				return fmt.Errorf("publication rule %q has empty evidence", rule.ID)
			}
			if _, exists := evidence[item]; exists {
				return fmt.Errorf("publication rule %q repeats evidence %q", rule.ID, item)
			}
			evidence[item] = struct{}{}
		}
	}
	return validatePrivacy(config.Privacy)
}

func validatePrivacy(privacy PrivacyPolicy) error {
	emails := make(map[string]struct{}, len(privacy.AllowedEmails))
	for _, value := range privacy.AllowedEmails {
		match := emailPattern.FindStringIndex(value)
		if strings.TrimSpace(value) != value || match == nil || match[0] != 0 || match[1] != len(value) {
			return fmt.Errorf("privacy allowed email %q is not canonical", value)
		}
		key := strings.ToLower(value)
		if _, exists := emails[key]; exists {
			return fmt.Errorf("privacy allowed emails repeat %q case-insensitively", value)
		}
		emails[key] = struct{}{}
	}
	hosts := make(map[string]struct{}, len(privacy.AllowedURLHosts))
	for _, value := range privacy.AllowedURLHosts {
		parsed, err := url.Parse("https://" + value)
		if err != nil || parsed.User != nil || parsed.Port() != "" || parsed.Host != value || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Hostname() == "" || value != strings.ToLower(strings.TrimSuffix(parsed.Hostname(), ".")) {
			return fmt.Errorf("privacy allowed URL host %q is not canonical", value)
		}
		if _, exists := hosts[value]; exists {
			return fmt.Errorf("privacy allowed URL hosts repeat %q", value)
		}
		hosts[value] = struct{}{}
	}
	sentinels := make(map[string]struct{}, len(privacy.TestSentinels))
	for _, value := range privacy.TestSentinels {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") || !utf8.ValidString(value) {
			return fmt.Errorf("privacy test sentinel is not a canonical single-line token")
		}
		if _, exists := sentinels[value]; exists {
			return fmt.Errorf("privacy test sentinels repeat %q", value)
		}
		sentinels[value] = struct{}{}
	}
	reviewed := make(map[ScanFinding]struct{}, len(privacy.ReviewedFindings))
	for _, finding := range privacy.ReviewedFindings {
		if err := validateRepositoryPath(finding.Path); err != nil {
			return fmt.Errorf("privacy reviewed finding path: %w", err)
		}
		if finding.Line <= 0 {
			return fmt.Errorf("privacy reviewed finding %q requires a positive line", finding.Path)
		}
		if !validScanRuleID(finding.RuleID) {
			return fmt.Errorf("privacy reviewed finding %q has unknown rule %q", finding.Path, finding.RuleID)
		}
		if len(finding.MatchSHA256) != sha256.Size*2 || strings.ToLower(finding.MatchSHA256) != finding.MatchSHA256 {
			return fmt.Errorf("privacy reviewed finding %q has a noncanonical digest", finding.Path)
		}
		if _, err := hex.DecodeString(finding.MatchSHA256); err != nil {
			return fmt.Errorf("privacy reviewed finding %q has a noncanonical digest", finding.Path)
		}
		if !validReviewedFindingPurpose(finding.Purpose) {
			return fmt.Errorf("privacy reviewed finding %q has unknown purpose %q", finding.Path, finding.Purpose)
		}
		key := ScanFinding{Path: finding.Path, Line: finding.Line, RuleID: finding.RuleID, MatchSHA256: finding.MatchSHA256}
		if _, exists := reviewed[key]; exists {
			return fmt.Errorf("privacy reviewed findings repeat %q line %d rule %q", finding.Path, finding.Line, finding.RuleID)
		}
		reviewed[key] = struct{}{}
	}
	return nil
}

func validScanRuleID(value string) bool {
	switch value {
	case "home-path", "private-email", "private-url", "private-key", "credential-assignment", "provider-token", "bearer-token", "high-entropy-token":
		return true
	default:
		return false
	}
}

func validReviewedFindingPurpose(value string) bool {
	switch value {
	case "synthetic-security-fixture", "documentation-example", "detector-definition", "runtime-loopback", "build-metadata", "historical-trace-identifier":
		return true
	default:
		return false
	}
}

func validRuleID(value string) bool {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, character := range value {
		if character == '-' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9' {
			continue
		}
		return false
	}
	return true
}

func validClass(value string) bool {
	return value == "project-owned-original" || value == "reference-informed-independent" || value == "license-compatible-third-party" || value == "proprietary-or-reconstructable" || value == "private-operational"
}

func validDecision(value string) bool {
	return value == "include" || value == "exclude" || value == "rewrite" || value == "unresolved"
}

func validateRepositoryPathPattern(pattern string) error {
	if pattern == "" || path.IsAbs(pattern) || strings.Contains(pattern, `\`) || strings.Contains(pattern, "\x00") {
		return fmt.Errorf("invalid repository path pattern %q", pattern)
	}
	if clean := path.Clean(pattern); clean != pattern || clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("invalid repository path pattern %q", pattern)
	}
	for _, segment := range strings.Split(pattern, "/") {
		if segment == "**" {
			continue
		}
		if _, err := path.Match(segment, ""); err != nil {
			return fmt.Errorf("invalid repository path pattern %q: %w", pattern, err)
		}
	}
	return nil
}

func validateRepositoryPath(name string) error {
	if !utf8.ValidString(name) || strings.Contains(name, "\x00") || strings.Contains(name, `\`) {
		return fmt.Errorf("invalid repository path %q", name)
	}
	if clean := path.Clean(name); clean != name || clean == "." || clean == ".." || strings.HasPrefix(clean, "../") || path.IsAbs(clean) {
		return fmt.Errorf("invalid repository path %q", name)
	}
	return nil
}
