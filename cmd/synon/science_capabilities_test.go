package main

import "testing"

func TestLoadScientificCapabilityCatalogUsesEmbeddedSingleAuthority(t *testing.T) {
	catalog, err := loadScientificCapabilityCatalog()
	if err != nil {
		t.Fatal(err)
	}
	definition, engine, found := catalog.FindExecutionPack("molecular-docking.autodock-vina")
	if !found || definition.ID != "molecular-docking" || engine.ExecutionPack.Mode != "local" {
		t.Fatalf("embedded execution pack=%#v/%#v found=%t", definition, engine, found)
	}
}
