package cli

import "testing"

func TestReferencePagesDocumentEveryCommandAndFlag(t *testing.T) {
	assertDocumented(t, BillingCmd(), "zuano billing", "../../docs/reference/billing.md")
	assertDocumented(t, A2ACmd(), "zuano a2a", "../../docs/reference/a2a.md")
	assertDocumented(t, ComplianceCmd(), "zuano compliance", "../../docs/reference/compliance.md")
	assertDocumented(t, NetworksCmd(), "zuano networks", "../../docs/reference/networks.md")
	assertDocumented(t, PackCmd(), "zuano pack", "../../docs/reference/pack.md")
}
