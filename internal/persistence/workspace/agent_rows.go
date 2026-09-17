package workspace

import (
	"encoding/json"
	"fmt"
)

type agentRowScanner interface {
	Scan(dest ...any) error
}

func scanAgent(row agentRowScanner) (Agent, error) {
	var agent Agent
	var rawTags, rawSkills, rawSkillTombstones, rawConnectorTombstones string
	if err := row.Scan(
		&agent.ID, &agent.UserID, &agent.Name, &agent.DisplayName, &agent.Description, &agent.SystemPrompt,
		&agent.IconKey, &agent.ColorKey, &rawTags, &rawSkills, &rawSkillTombstones, &rawConnectorTombstones,
		&agent.Unrestricted, &agent.Enabled, &agent.CreatedAt, &agent.UpdatedAt,
	); err != nil {
		return Agent{}, err
	}
	for label, value := range map[string]struct {
		raw    string
		target *[]string
	}{
		"tags":                 {rawTags, &agent.Tags},
		"skills":               {rawSkills, &agent.SkillNames},
		"skill tombstones":     {rawSkillTombstones, &agent.SkillTombstones},
		"connector tombstones": {rawConnectorTombstones, &agent.ConnectorTombstones},
	} {
		if err := json.Unmarshal([]byte(value.raw), value.target); err != nil {
			return Agent{}, fmt.Errorf("decode agent %s: %w", label, err)
		}
		if *value.target == nil {
			*value.target = []string{}
		}
	}
	return agent, nil
}
