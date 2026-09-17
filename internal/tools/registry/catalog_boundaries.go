package registry

// Catalogs separates model-visible tools from service/API operation
// contracts. Combining these authorities makes internal controls look like
// model tools and inflates /api/tools.
type Catalogs struct {
	ModelTools *Registry
	Operations *Registry
}

// Default returns only statically registered model-visible tools. Task-owned
// model tools continue to be injected by their owning runtime.
func Default() *Registry {
	return DefaultCatalogs().ModelTools
}

// DefaultOperations returns exact non-model service and compatibility
// operation contracts. These contracts are not advertised as tools.
func DefaultOperations() *Registry {
	return DefaultCatalogs().Operations
}

func DefaultCatalogs() Catalogs {
	catalogs := Split(defaultCatalog())
	catalogs.ModelTools.AttachServiceOperations(catalogs.Operations)
	return catalogs
}

// Split partitions an explicitly supplied catalog. It is primarily used by
// focused tests and embedders that replace the default operation set.
func Split(source *Registry) Catalogs {
	model := cloneRegistryMetadata(source)
	operations := cloneRegistryMetadata(source)
	// A caller may pass an already-partitioned model registry whose attached
	// operation catalog remains part of the same authority. Re-splitting only
	// Names silently discarded every deferred Tool and forced downstream name
	// exceptions. AllNames preserves the complete registry without exposing the
	// operation partition through the model-facing Names method.
	for _, name := range source.AllNames() {
		tool, _ := source.Get(name)
		if tool.Exposure == ToolExposureDirect {
			model.tools[name] = tool
			continue
		}
		operations.tools[name] = tool
	}
	return Catalogs{ModelTools: model, Operations: operations}
}

func cloneRegistryMetadata(source *Registry) *Registry {
	return &Registry{
		tools:                 map[string]Tool{},
		articleFulltextClient: source.articleFulltextClient,
		bindingModeClient:     source.bindingModeClient,
		patentSearchClient:    source.patentSearchClient,
		webFetchOptions:       source.webFetchOptions,
		webSearchOptions:      source.webSearchOptions,
	}
}
