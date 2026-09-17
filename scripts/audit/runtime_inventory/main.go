package main

import (
	"encoding/json"
	"os"
	"sort"

	"synon-go/internal/harnesscontract"
	"synon-go/internal/tools/registry"
)

func main() {
	names := registry.Default().Names()
	sort.Strings(names)
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
		"modelRegistryNames": names,
		"rootModelTools":     harnesscontract.RootModelTools(),
		"fixedJobModelTools": harnesscontract.FixedJobModelTools(),
	}); err != nil {
		panic(err)
	}
}
