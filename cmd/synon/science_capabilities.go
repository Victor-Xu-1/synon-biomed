package main

import "synon-go/internal/sciencecapability"

func loadScientificCapabilityCatalog() (*sciencecapability.Catalog, error) {
	catalog, err := sciencecapability.DefaultCatalog()
	if err != nil {
		return nil, err
	}
	return &catalog, nil
}
