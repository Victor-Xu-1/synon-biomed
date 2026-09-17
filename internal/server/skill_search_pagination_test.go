package server

import (
	"fmt"
	"testing"

	"synon-go/internal/skills"
)

func TestSkillSearchHonorsRequestedPageAndReportsContinuation(t *testing.T) {
	srv := New(Options{})
	catalog := skills.NewCatalog()
	for index := 0; index < 75; index++ {
		catalog.AddSkill(skills.Skill{
			Name:        fmt.Sprintf("research-capability-%03d", index),
			Description: "research evidence capability",
		})
	}
	srv.skillCatalog = catalog

	firstRaw, err := srv.executeSkillSearchTool(map[string]any{
		"query": "research", "max_results": float64(25),
	})
	if err != nil {
		t.Fatal(err)
	}
	first := firstRaw.(map[string]any)
	if first["returned_count"] != 25 || first["total_matches"] != 75 || first["has_more"] != true || first["next_offset"] != 25 {
		t.Fatalf("first page=%#v", first)
	}

	lastRaw, err := srv.executeSkillSearchTool(map[string]any{
		"query": "research", "max_results": float64(25), "offset": float64(50),
	})
	if err != nil {
		t.Fatal(err)
	}
	last := lastRaw.(map[string]any)
	if last["returned_count"] != 25 || last["offset"] != 50 || last["has_more"] != false {
		t.Fatalf("last page=%#v", last)
	}
}
