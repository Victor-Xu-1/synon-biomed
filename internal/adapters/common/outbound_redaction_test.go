package common

import (
	"errors"
	"strings"
	"testing"
)

func TestOutboundErrorsRedactStructuredAndExactSecrets(t *testing.T) {
	secret := "exact-secret-value"
	redacted := RedactOutboundText(`request failed Authorization: Bearer abc123 token=xyz password="pw" body=`+secret, secret)
	for _, forbidden := range []string{"abc123", "xyz", "pw", secret} {
		if strings.Contains(redacted, forbidden) {
			t.Fatalf("redacted error leaked %q: %s", forbidden, redacted)
		}
	}
	report, err := (&OutboundReport{}).Finish(errors.New("access_token=sensitive-token"))
	if err == nil || strings.Contains(report.Error, "sensitive-token") || report.DeliverySummary != "failed" {
		t.Fatalf("report = %#v err=%v", report, err)
	}
}
