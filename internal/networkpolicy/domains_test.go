package networkpolicy

import "testing"

func TestNormalizePatternsAndSecurityBoundaries(t *testing.T) {
	domains, err := NormalizePatterns([]string{"Example.COM.", "*.example.org", "example.com"}, 64)
	if err != nil {
		t.Fatal(err)
	}
	if len(domains) != 2 || domains[0] != "example.com" || domains[1] != "*.example.org" {
		t.Fatalf("normalized domains = %#v", domains)
	}
	for _, value := range []string{"localhost", "127.0.0.1", "10.0.0.1", "service.local", "*.example.test"} {
		domain, err := NormalizePattern(value)
		if err == nil && !PrivateOrReserved(domain) {
			t.Fatalf("private/reserved domain %q was grantable as %q", value, domain)
		}
	}
	if _, err := NormalizePattern("8.8.8.8"); err == nil {
		t.Fatal("public IP literal was accepted as a domain pattern")
	}
	if conflict := ConflictingPattern("api.example.com", []string{"*.example.com"}); conflict != "*.example.com" {
		t.Fatalf("denied wildcard conflict = %q", conflict)
	}
	if conflict := ConflictingPattern("hooks.slack.com", BuiltInDeniedPatterns()); conflict == "" {
		t.Fatal("built-in exfiltration denylist did not cover hooks.slack.com")
	}
	if conflict := ConflictingPattern("s3.eu-west-1.amazonaws.com", BuiltInDeniedPatterns()); conflict != "s3.*.amazonaws.com" {
		t.Fatalf("middle-label wildcard conflict = %q", conflict)
	}
}

func TestNormalizePatternsEnforcesUniqueLimit(t *testing.T) {
	if _, err := NormalizePatterns([]string{"a.example.com", "b.example.com"}, 1); err == nil {
		t.Fatal("domain limit was not enforced")
	}
	if domains, err := NormalizePatterns([]string{"a.example.com", "A.EXAMPLE.COM"}, 1); err != nil || len(domains) != 1 {
		t.Fatalf("duplicate domain counted against limit: %#v err=%v", domains, err)
	}
}

func TestAllowsHostRequiresExplicitAllowAndLetsDenyWin(t *testing.T) {
	if !AllowsHost("api.example.com", []string{"*.example.com"}, nil) {
		t.Fatal("allowed public subdomain was rejected")
	}
	if AllowsHost("api.example.com", []string{"*.example.com"}, []string{"api.example.com"}) {
		t.Fatal("explicit deny did not override wildcard allow")
	}
	for _, host := range []string{"other.example.org", "localhost", "service.local", "hooks.slack.com"} {
		if AllowsHost(host, []string{"*.example.com", "hooks.slack.com"}, nil) {
			t.Fatalf("unsafe or unlisted host %q was allowed", host)
		}
	}
	if wildcard, err := NormalizePattern(PublicWildcard); err != nil || wildcard != PublicWildcard {
		t.Fatalf("public wildcard=%q err=%v", wildcard, err)
	}
	if !AllowsHost("public-data.example.org", []string{PublicWildcard}, nil) {
		t.Fatal("full-access public hostname was rejected")
	}
	for _, host := range []string{"localhost", "service.local"} {
		if AllowsHost(host, []string{PublicWildcard}, nil) {
			t.Fatalf("public wildcard bypassed protected destination %q", host)
		}
	}
	for _, host := range []string{"hooks.slack.com", "s3.amazonaws.com", "storage.googleapis.com"} {
		if !AllowsHost(host, []string{PublicWildcard}, nil) {
			t.Fatalf("explicit public grant restricted by hidden defaults: %s", host)
		}
		if AllowsHost(host, []string{PublicWildcard}, []string{host}) {
			t.Fatalf("operator deny overridden: %s", host)
		}
	}
}
