package commands

import (
	"fmt"
	"sort"
	"strings"

	"github.com/abietic/yhc/engine/skills"
)

// executeSkills implements /skills — list available skills.
// Mirrors reference commands/skills.tsx.
//
// Usage:
//
//	/skills            → list all skills
//	/skills <query>    → search skills by name/description
//	/skills <name>     → show detail for exact skill name match
func executeSkills(ctx *CommandContext, args string) (*CommandResult, error) {
	snapshot, ok := runtimeInspectionSnapshot(ctx)
	if !ok {
		return &CommandResult{
			Output: "Skill inspection is unavailable for this runtime.",
		}, nil
	}

	// If args match an exact skill name, show detail view.
	if args != "" {
		for _, detail := range snapshot.Skills.Skills {
			if detail != nil && strings.EqualFold(detail.Name, args) {
				return &CommandResult{
					Output: formatSkillDetail(detail, snapshot.Skills.Diagnostics),
				}, nil
			}
		}
	}

	skillList := make([]*skills.Skill, 0, len(snapshot.Skills.Skills))
	query := strings.ToLower(strings.TrimSpace(args))
	for _, skill := range snapshot.Skills.Skills {
		if skill == nil {
			continue
		}
		if query == "" ||
			strings.Contains(strings.ToLower(skill.Name), query) ||
			strings.Contains(strings.ToLower(skill.Description), query) {
			skillList = append(skillList, skill)
		}
	}

	if len(skillList) == 0 {
		if args != "" {
			return &CommandResult{Output: fmt.Sprintf("No skills matching %q.", args)}, nil
		}
		output := "No skills registered."
		if len(snapshot.Skills.Diagnostics) > 0 {
			output += fmt.Sprintf(
				"\n\n%d skill source(s) failed validation.",
				len(snapshot.Skills.Diagnostics),
			)
		}
		return &CommandResult{Output: output}, nil
	}

	// If search returns exactly one result, show detail.
	if len(skillList) == 1 {
		return &CommandResult{
			Output: formatSkillDetail(
				skillList[0],
				snapshot.Skills.Diagnostics,
			),
		}, nil
	}

	// Sort by name for stable output.
	sort.Slice(skillList, func(i, j int) bool {
		return skillList[i].Name < skillList[j].Name
	})

	var sb strings.Builder
	if args != "" {
		fmt.Fprintf(&sb, "Skills matching %q (%d):\n\n", args, len(skillList))
	} else {
		fmt.Fprintf(&sb, "Available skills (%d):\n\n", len(skillList))
	}

	for _, s := range skillList {
		fmt.Fprintf(&sb, "  %-20s", s.Name)
		if s.Description != "" {
			sb.WriteString("  " + s.Description)
		}
		sb.WriteString("\n")
		if len(s.Tags) > 0 {
			fmt.Fprintf(&sb, "  %-20s  tags: %s\n", "", strings.Join(s.Tags, ", "))
		}
		fmt.Fprintf(
			&sb,
			"  %-20s  source: %s; health: %s\n",
			"",
			firstNonEmpty(s.Source, "runtime"),
			firstNonEmpty(s.Health, "available"),
		)
	}

	if len(snapshot.Skills.Diagnostics) > 0 {
		fmt.Fprintf(
			&sb,
			"\nDiagnostics: %d source(s) failed validation.\n",
			len(snapshot.Skills.Diagnostics),
		)
	}
	sb.WriteString("\nSkills are invoked automatically by the agent through the Skill tool.")
	sb.WriteString("\nUse /skills <name> to see details for a specific skill.")
	return &CommandResult{Output: sb.String()}, nil
}

func formatSkillDetail(
	s *skills.Skill,
	diagnostics []skills.Diagnostic,
) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Skill: %s\n", s.Name)
	fmt.Fprintf(&sb, "Source: %s\n", firstNonEmpty(s.Source, "runtime"))
	fmt.Fprintf(&sb, "Health: %s\n", firstNonEmpty(s.Health, "available"))
	if s.Description != "" {
		fmt.Fprintf(&sb, "Description: %s\n", s.Description)
	}
	if len(s.Tags) > 0 {
		fmt.Fprintf(&sb, "Tags: %s\n", strings.Join(s.Tags, ", "))
	}
	if s.FilePath != "" {
		fmt.Fprintf(&sb, "File: %s\n", s.FilePath)
	}
	if len(s.Args) > 0 {
		sb.WriteString("Arguments:\n")
		for _, arg := range s.Args {
			req := ""
			if arg.Required {
				req = " (required)"
			}
			fmt.Fprintf(&sb, "  {{%s}}%s", arg.Name, req)
			if arg.Description != "" {
				fmt.Fprintf(&sb, " — %s", arg.Description)
			}
			sb.WriteString("\n")
		}
	}
	sb.WriteString("\nContent preview:\n")
	content := s.Content
	if len(content) > 500 {
		content = content[:500] + "\n...(truncated, use Skill tool for full content)"
	}
	sb.WriteString(content)
	if len(diagnostics) > 0 {
		fmt.Fprintf(
			&sb,
			"\n\nRegistry diagnostics: %d invalid source(s).",
			len(diagnostics),
		)
	}
	return sb.String()
}
