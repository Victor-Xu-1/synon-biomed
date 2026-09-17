package main

import (
	"fmt"

	productidentity "synon-go"
)

func main() {
	identity := productidentity.Current()
	fmt.Printf("%s\t%s\n", identity.MachineSlug, identity.Version)
}
