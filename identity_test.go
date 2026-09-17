package productidentity

import "testing"

func TestCurrentUsesRootProductIdentityAuthority(t *testing.T) {
	identity := Current()
	if identity.Schema != authoritySchema || identity.DisplayName != "Synon Biomed" || identity.Version != "0.1.1" || identity.MachineSlug != "synon-biomed" {
		t.Fatalf("identity = %#v", identity)
	}
	if identity.FullDisplay() != "Synon Biomed v0.1.1" || identity.SourcePackage() != "synon-biomed-v0.1.1" || identity.UserAgent() != "synon-biomed/0.1.1" {
		t.Fatalf("derived identity is inconsistent: %#v", identity)
	}
}

func TestDecodeProductIdentityFailsClosed(t *testing.T) {
	for _, raw := range []string{
		`{"schema":"synon.product-identity.v1","schema":"synon.product-identity.v1","display_name":"Synon Biomed","version":"0.1.1","machine_slug":"synon-biomed"}`,
		`{"schema":"synon.product-identity.v1","display_name":"Synon Biomed","version":"0.1.1","machine_slug":"synon-biomed","unknown":true}`,
		`{"schema":"synon.product-identity.v1","display_name":" Synon Biomed","version":"0.1.1","machine_slug":"synon-biomed"}`,
		`{"schema":"synon.product-identity.v1","display_name":"Synon Biomed","version":"01.0.0","machine_slug":"synon-biomed"}`,
		`{"schema":"synon.product-identity.v1","display_name":"Synon Biomed","version":"0.1.1","machine_slug":"Synon Biomed"}`,
		`{"schema":"synon.product-identity.v1","display_name":"Synon Biomed","version":"0.1.1","machine_slug":"synon-biomed"} true`,
	} {
		if _, err := decode([]byte(raw)); err == nil {
			t.Fatalf("decode(%s) succeeded", raw)
		}
	}
}
