package releasepkg

import (
	"errors"

	"synon-go/internal/compat/contracts"
)

// Schema 3 remains readable for installation, rollback and integrity checks.
// Historical scores are not generated for new packages or used as release approval.
func validateLegacyCoverage(coverage *Coverage) error {
	if coverage == nil {
		return errors.New("legacy release manifest is missing historical coverage")
	}
	summary := contracts.V11Summary()
	if coverage.Scope != "historical-non-web-compatibility" ||
		coverage.Baseline != "synonbiomed-v1.1" ||
		coverage.Authority != "strict-behavior-evidence" ||
		!coverage.CompatibilityBaselineEligible ||
		coverage.ServiceContracts != summary.ServiceMethodCount ||
		coverage.HTTPRoutes != summary.HTTPRouteCount ||
		coverage.RealtimeEvents != summary.EventTypeCount ||
		coverage.RealtimeQueries != summary.QueryKeyCount ||
		coverage.ServiceNotApplicable != 3 ||
		coverage.InScope != summary.ServiceMethodCount+summary.HTTPRouteCount+summary.EventTypeCount+summary.QueryKeyCount-coverage.ServiceNotApplicable ||
		coverage.Implemented != coverage.InScope || coverage.Missing != 0 ||
		coverage.ServiceImplemented+coverage.HTTPRoutesImplemented+coverage.RealtimeEventsImplemented+coverage.RealtimeQueriesImplemented != coverage.Implemented {
		return errors.New("legacy release manifest does not attest complete in-scope compatibility")
	}
	return nil
}
