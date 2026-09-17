package buildinfo

import productidentity "synon-go"

type Info struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	MachineSlug   string `json:"machineSlug"`
	SourcePackage string `json:"sourcePackage"`
}

func Release() Info {
	identity := productidentity.Current()
	return Info{
		Name:          identity.DisplayName,
		Version:       identity.Version,
		MachineSlug:   identity.MachineSlug,
		SourcePackage: identity.SourcePackage(),
	}
}

func UserAgent() string {
	return productidentity.Current().UserAgent()
}
