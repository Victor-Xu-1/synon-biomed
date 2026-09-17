package skills

import "testing"

func TestCatalogSearchChineseQueryMatchesEnglishSkillMetadata(t *testing.T) {
	catalog := NewCatalog()
	catalog.AddSkill(Skill{
		Name:        "compound-search",
		Description: "Search public compound databases and analyze chemical properties.",
	})
	catalog.AddSkill(Skill{
		Name:        "calendar-helper",
		Description: "Manage calendar events.",
	})

	matches := catalog.Search("请搜索化合物并分析理化性质", 5)
	if len(matches) == 0 || matches[0].Name != "compound-search" {
		t.Fatalf("Search() = %#v, want compound-search first", matches)
	}
}
