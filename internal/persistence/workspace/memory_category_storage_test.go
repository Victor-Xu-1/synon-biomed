package workspace

import (
	"context"
	"path/filepath"
	"testing"
)

func TestMemoryCategoryStorageRejectsCrossOwnerAssignments(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	ownerCategory, err := store.CreateMemoryCategory(ctx, "owner-a", "Owner A", "Owner scoped", true)
	if err != nil {
		t.Fatal(err)
	}
	foreignCategory, err := store.CreateMemoryCategory(ctx, "owner-b", "Owner B", "Foreign", true)
	if err != nil {
		t.Fatal(err)
	}
	ownerMemory, err := store.CreateMemory(CreateMemoryInput{
		ID: "memory-owner", UserID: "owner-a", Body: "owner memory", Origin: "user",
	})
	if err != nil {
		t.Fatal(err)
	}
	foreignMemory, err := store.CreateMemory(CreateMemoryInput{
		ID: "memory-foreign", UserID: "owner-b", Body: "foreign memory", Origin: "user",
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := store.db.ExecContext(ctx, `UPDATE memories SET category_id = CASE id
		WHEN ? THEN ? WHEN ? THEN ? END WHERE id IN (?, ?)`,
		ownerMemory.ID, foreignCategory.ID, foreignMemory.ID, ownerCategory.ID, ownerMemory.ID, foreignMemory.ID); err != nil {
		t.Fatal(err)
	}

	listed, err := store.ListMemoriesForUser(ctx, "owner-a", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 {
		t.Fatalf("owner memories = %#v", listed)
	}
	if listed[0].CategoryID != "" || listed[0].CategoryName != "" || listed[0].CategoryGuidance != "" {
		t.Fatalf("foreign category leaked through owner projection: %#v", listed[0])
	}

	categories, err := store.ListMemoryCategories(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 1 || categories[0].RowCount != 0 {
		t.Fatalf("foreign memory counted in owner category: %#v", categories)
	}

	deleted, err := store.DeleteMemoryCategory(ctx, "owner-a", ownerCategory.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted || deleted.FactsDeleted != 0 {
		t.Fatalf("delete result = %#v", deleted)
	}
	if memories, err := store.ListMemoriesForUser(ctx, "owner-b", "", "", false); err != nil || len(memories) != 1 {
		t.Fatalf("foreign memory changed: memories=%#v err=%v", memories, err)
	}
}

func TestMemoryCategoryStorageUsesCanonicalDirectReference(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "workspace.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ctx := context.Background()

	category, err := store.CreateMemoryCategory(ctx, "owner", "Research", "Recall research facts", true)
	if err != nil {
		t.Fatal(err)
	}
	memory, err := store.CreateMemory(CreateMemoryInput{
		ID: "memory", UserID: "owner", Body: "research fact", Origin: "user", CategoryID: category.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if memory.CategoryID != category.ID || memory.CategoryName != category.Name || memory.CategoryGuidance != category.Guidance {
		t.Fatalf("created memory category projection = %#v", memory)
	}

	name := "Validated research"
	updatedCategory, err := store.UpdateMemoryCategory(ctx, "owner", category.ID, UpdateMemoryCategoryInput{Name: &name})
	if err != nil {
		t.Fatal(err)
	}
	if updatedCategory.Name != name || updatedCategory.RowCount != 1 {
		t.Fatalf("updated category = %#v", updatedCategory)
	}

	updatedMemory, err := store.UpdateMemoryOwned(ctx, memory.ID, "owner", UpdateMemoryInput{ClearCategory: true})
	if err != nil {
		t.Fatal(err)
	}
	if updatedMemory.CategoryID != "" {
		t.Fatalf("cleared memory category = %#v", updatedMemory)
	}
	categories, err := store.ListMemoryCategories(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 1 || categories[0].RowCount != 0 {
		t.Fatalf("categories after clear = %#v", categories)
	}

	categoryID := category.ID
	updatedMemory, err = store.UpdateMemoryOwned(ctx, memory.ID, "owner", UpdateMemoryInput{CategoryID: &categoryID})
	if err != nil {
		t.Fatal(err)
	}
	if updatedMemory.CategoryID != category.ID || updatedMemory.CategoryName != name {
		t.Fatalf("reassigned memory category = %#v", updatedMemory)
	}
	deleted, err := store.DeleteMemoryCategory(ctx, "owner", category.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if !deleted.Deleted || deleted.FactsDeleted != 0 {
		t.Fatalf("delete category result = %#v", deleted)
	}
	memories, err := store.ListMemoriesForUser(ctx, "owner", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(memories) != 1 || memories[0].CategoryID != "" {
		t.Fatalf("memory after category deletion = %#v", memories)
	}
}
