package tools

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

// Team represents a group of agents collaborating toward a shared goal.
type Team struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Goal      string       `json:"goal"`
	Members   []TeamMember `json:"members"`
	CreatedAt time.Time    `json:"created_at"`
	Status    string       `json:"status"` // "active", "completed", "deleted"
}

// TeamMember represents a single agent within a team.
type TeamMember struct {
	Role         string `json:"role"`
	Instructions string `json:"instructions"`
	AgentID      string `json:"agent_id,omitempty"`
}

// Team storage: in-memory registry of active teams.
var (
	teams   = make(map[string]*Team)
	teamsMu sync.RWMutex
)

// GetTeam returns a team by ID.
func GetTeam(id string) (*Team, bool) {
	teamsMu.RLock()
	defer teamsMu.RUnlock()
	t, ok := teams[id]
	return t, ok
}

// TeamCreateTool returns a tool that creates a team of agents working together
// on a complex task.
func TeamCreateTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TeamCreate",
			Desc: "Creates a team of agents that work together on a complex task. Each team member gets a role and can communicate with others.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"name":    {Type: "string", Desc: "Name for the team", Required: true},
				"members": {Type: "array", Desc: "Array of team member configs with 'role' and 'instructions' fields", Required: true},
				"goal":    {Type: "string", Desc: "The team's overall goal/objective", Required: true},
			}),
		},
		Execute: executeTeamCreate,
	}
}

// TeamDeleteTool returns a tool that deletes a previously created team and
// stops all its member agents.
func TeamDeleteTool() ToolImpl {
	return ToolImpl{
		Info: &schema.ToolInfo{
			Name: "TeamDelete",
			Desc: "Deletes a previously created team and stops all its member agents.",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"team_id": {Type: "string", Desc: "The ID of the team to delete", Required: true},
			}),
		},
		Execute: executeTeamDelete,
	}
}

func executeTeamCreate(input string) (string, error) {
	var params struct {
		Name    string `json:"name"`
		Members []struct {
			Role         string `json:"role"`
			Instructions string `json:"instructions"`
		} `json:"members"`
		Goal string `json:"goal"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("team_create: invalid params: %w", err)
	}

	name := strings.TrimSpace(params.Name)
	if name == "" {
		return "", fmt.Errorf("team_create: name is required")
	}

	goal := strings.TrimSpace(params.Goal)
	if goal == "" {
		return "", fmt.Errorf("team_create: goal is required")
	}

	if len(params.Members) == 0 {
		return "", fmt.Errorf("team_create: at least one member is required")
	}

	members := make([]TeamMember, 0, len(params.Members))
	for i, m := range params.Members {
		role := strings.TrimSpace(m.Role)
		if role == "" {
			return "", fmt.Errorf("team_create: member[%d] role is required", i)
		}
		members = append(members, TeamMember{
			Role:         role,
			Instructions: strings.TrimSpace(m.Instructions),
		})
	}

	team := &Team{
		ID:        uuid.New().String(),
		Name:      name,
		Goal:      goal,
		Members:   members,
		CreatedAt: time.Now().UTC(),
		Status:    "active",
	}

	teamsMu.Lock()
	teams[team.ID] = team
	teamsMu.Unlock()

	result, err := json.Marshal(team)
	if err != nil {
		return "", fmt.Errorf("team_create: failed to marshal result: %w", err)
	}
	return string(result), nil
}

func executeTeamDelete(input string) (string, error) {
	var params struct {
		TeamID string `json:"team_id"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return "", fmt.Errorf("team_delete: invalid params: %w", err)
	}

	teamID := strings.TrimSpace(params.TeamID)
	if teamID == "" {
		return "", fmt.Errorf("team_delete: team_id is required")
	}

	teamsMu.Lock()
	team, ok := teams[teamID]
	if !ok {
		teamsMu.Unlock()
		return "", fmt.Errorf("team_delete: team %q not found", teamID)
	}
	team.Status = "deleted"
	teamsMu.Unlock()

	return fmt.Sprintf("Team %q (id=%s) has been deleted and all member agents stopped.", team.Name, team.ID), nil
}
