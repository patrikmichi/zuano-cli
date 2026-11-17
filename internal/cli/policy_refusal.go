package cli

import (
	"encoding/json"

	"github.com/patrikmichi/zuano-cli/internal/client"
	"github.com/patrikmichi/zuano-cli/internal/textsafe"
)

const policyOffReason = "policy_off"

// refusalCodes maps the reasons a call or a request can be refused for to the
// exit status that ends the command. The reasons that map to 1 are failures
// whose code stays in the JSON.
var refusalCodes = map[string]int{
	policyOffReason:              exitDenied,
	"denied_by_policy":           exitDenied,
	"tool_not_pinned":            exitDenied,
	"meta_executor_blocked":      exitDenied,
	"source_not_preferred":       exitDenied,
	"agent_not_allowed":          exitDenied,
	"tool_definition_changed":    exitToolChanged,
	"upstream_quarantined":       exitQuarantined,
	"registry_server_deleted":    exitQuarantined,
	"agent_not_pinned":           exitDenied,
	"a2a_dlp_blocked":            exitDenied,
	"approval_denied":            exitDenied,
	"approval_expired":           exitExpired,
	"identity_suspended":         client.ExitSuspended,
	"identity_revoked":           client.ExitSuspended,
	"identity_expired":           client.ExitSuspended,
	"identity_refused":           client.ExitSuspended,
	"a2a_loop":                   exitA2AChainRefused,
	"a2a_depth_exceeded":         exitA2AChainRefused,
	"a2a_chain_limit":            exitA2AChainRefused,
	"a2a_scope_exceeded":         exitA2AChainRefused,
	"chain_killed":               exitChainKilled,
	"chain_budget_exhausted":     exitChainBudget,
	"agent_card_changed":         exitAgentCardChanged,
	"spec_invalid":               exitSpecInvalid,
	"spec_unsupported":           exitSpecUnsupported,
	"connector_slug_taken":       exitConnectorClash,
	"connector_version_conflict": exitConnectorClash,
	"quality_gate_failed":        exitQualityGate,
	"network_offline":            exitNetworkOffline,
	"egress_unavailable":         exitNetworkOffline,
	"request_too_large":          1,
	"response_too_large":         1,
	"spec_fetch_blocked":         1,
	"federated_mode_unsupported": 1,

	// Agent-to-agent refusals: the chain may not take another hop, the caller
	// lacks an authority, or the pipeline could not carry the call.
	"a2a_edge_denied":             exitA2AChainRefused,
	"a2a_token_invalid":           exitA2AChainRefused,
	"a2a_agent_identity_required": exitDenied,
	"a2a_delegate_scope_missing":  exitDenied,
	"a2a_skill_denied":            exitDenied,
	"approval_refused":            exitDenied,
	"a2a_part_unsupported":        1,
	"a2a_dispatch_unavailable":    1,
	"a2a_chain_unavailable":       1,
}

// policyRefusal recognises a call the organisation's or the platform's risk
// policy turned off, or the firewall or the catalog refused: a body, or an MCP
// result, whose reason is one of refusalCodes. It ends with the matching exit
// status and the gateway's sentence, which names the policy source and who can
// change it.
func policyRefusal(body []byte, env *mcpEnvelope) error {
	candidates := [][]byte{body}
	if env != nil && env.Result != nil && env.Result.IsError && len(env.Result.StructuredContent) > 0 {
		candidates = append(candidates, env.Result.StructuredContent)
	}
	for _, raw := range candidates {
		var doc struct {
			Reason  string `json:"reason"`
			Message string `json:"message"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(raw, &doc) != nil {
			continue
		}
		code, ok := refusalCodes[doc.Reason]
		if !ok {
			// A REST refusal may carry the reason as its error code.
			code, ok = refusalCodes[doc.Error]
		}
		if !ok {
			continue
		}
		msg := doc.Message
		if msg == "" {
			msg = doc.Error
		}
		if msg == "" {
			msg = "blocked by policy"
		}
		return &ExitError{Code: code, Msg: textsafe.Clean(msg)}
	}
	return nil
}
