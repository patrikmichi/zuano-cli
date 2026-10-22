package mcpwrap

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
)

// OfflineGovernor governs a server with no account: pins approved on the
// terminal, a local policy, local screening and a local hash-chained log. It
// has no approvals, since there is no second person to give one.
type OfflineGovernor struct {
	Name   string
	Pins   PinStore
	Policy UpstreamPolicy
	Audit  *AuditLog
	Logf   func(format string, args ...any)

	mu   sync.Mutex
	set  *PinSet
	seen map[string]bool
}

func (g *OfflineGovernor) logf(format string, args ...any) {
	if g.Logf != nil {
		g.Logf(format, args...)
	}
}

func (g *OfflineGovernor) audit(event string, fields map[string]any) {
	if g.Audit == nil {
		return
	}
	if err := g.Audit.Append(event, fields); err != nil {
		g.logf("the local audit log could not be written: %v", err)
	}
}

// reload takes up approvals made in another terminal while the server runs.
func (g *OfflineGovernor) reload() error {
	set, err := g.Pins.Load(g.Name)
	if err != nil {
		return err
	}
	g.set = set
	return nil
}

// Start loads the pins.
func (g *OfflineGovernor) Start(context.Context) error {
	set, err := g.Pins.Load(g.Name)
	if err != nil {
		return err
	}
	g.set, g.seen = set, map[string]bool{}
	g.audit("start", map[string]any{"upstream": g.Name})
	return nil
}

// Close does nothing; the log is written as events happen.
func (g *OfflineGovernor) Close() {}

// Tools records what the server shows and serves only what is pinned.
func (g *OfflineGovernor) Tools(_ context.Context, live []Definition, final bool) ([]Definition, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.reload(); err != nil {
		return nil, err
	}
	changes := g.set.Observe(live, g.seen, final)
	if final {
		g.seen = map[string]bool{}
	}
	if g.Policy.NewTools == "auto_read_only" {
		g.autoApprove(live, changes)
	}
	if len(changes) > 0 {
		if err := g.Pins.Save(g.set); err != nil {
			return nil, err
		}
	}
	for _, c := range changes {
		g.audit("definition_"+c.Kind, map[string]any{"tool": c.Tool, "oldHash": c.OldHash, "newHash": c.NewHash, "severity": c.Severity})
		g.logf("%s: tool %s %s (%s); review it with `zuano mcp changes`", g.Name, c.Tool, c.Kind, c.Severity)
	}
	var served []Definition
	for _, def := range live {
		pin := g.set.Tools[nameOf(def)]
		if pin == nil || g.Policy.Denied(pin.Name) {
			continue
		}
		if pin.State == StatePinned || (pin.State == StateQuarantined && g.Policy.OnDefinitionChange == "warn") {
			served = append(served, cleanDefinition(pin.Def).(map[string]any))
		}
	}
	return served, nil
}

// autoApprove pins the tools that appeared with a definition classified read.
func (g *OfflineGovernor) autoApprove(live []Definition, changes []ChangeRecord) {
	for _, c := range changes {
		if c.Kind != "added" || g.Policy.Denied(c.Tool) {
			continue
		}
		for _, def := range live {
			if nameOf(def) == c.Tool && g.Policy.Classify(def) == "read" && g.set.Approve(c.Tool) == nil {
				g.audit("auto_approved", map[string]any{"tool": c.Tool, "hash": c.NewHash})
			}
		}
	}
}

// Call allows a pinned tool the policy does not deny.
func (g *OfflineGovernor) Call(_ context.Context, tool string, args json.RawMessage) (Decision, error) {
	g.mu.Lock()
	err := g.reload()
	pin := g.set.Tools[tool]
	g.mu.Unlock()
	if err != nil {
		return Decision{Kind: "deny", Reason: "tool_not_pinned", Message: "the pins could not be read: " + err.Error()}, nil
	}
	d := Decision{Kind: "allow"}
	switch {
	case pin == nil || pin.State == StatePending || pin.State == StateRejected || pin.State == StateRemoved:
		d = Decision{Kind: "deny", Reason: "tool_not_pinned", Message: "approve it first with `zuano mcp pins approve " + g.Name + " " + tool + "`"}
	case pin.State == StateQuarantined && g.Policy.OnDefinitionChange != "warn":
		d = Decision{Kind: "deny", Reason: "tool_definition_changed", Message: "the server changed this tool; review it with `zuano mcp changes`"}
	case g.Policy.Denied(tool):
		d = Decision{Kind: "deny", Reason: "denied_by_policy", Message: "firewall.yaml denies this tool"}
	}
	risk := "write"
	if pin != nil {
		risk = g.Policy.Classify(pin.Def)
	}
	fields := map[string]any{"tool": tool, "risk": risk}
	if d.Kind == "allow" && g.Policy.NeedsApproval(risk) {
		d = Decision{Kind: "deny", Reason: "approval_unavailable", Message: "firewall.yaml asks for a person's approval of " + risk + " tools, and offline mode has no approvals"}
	}
	if d.Kind == "allow" {
		d = g.screenArguments(args, fields)
	}
	fields["decision"], fields["reason"] = d.Kind, d.Reason
	g.audit("call", fields)
	return d, nil
}

// screenArguments applies the profile to what leaves: a block ends the call, a
// redaction replaces the arguments.
func (g *OfflineGovernor) screenArguments(args json.RawMessage, fields map[string]any) Decision {
	if g.Policy.Profile == nil || len(args) == 0 {
		return Decision{Kind: "allow"}
	}
	var value any
	if json.Unmarshal(args, &value) != nil {
		return Decision{Kind: "allow"}
	}
	v := g.Policy.Profile.Evaluate("outbound", value)
	if len(v.Summary) > 0 {
		fields["dlp"] = v.Summary
	}
	switch v.Action {
	case dlpBlock:
		return Decision{Kind: "deny", Reason: "dlp_blocked", Message: "the arguments hold data the DLP profile keeps from leaving (" + v.Describe() + ")"}
	case dlpApproval:
		return Decision{Kind: "deny", Reason: "approval_unavailable", Message: "the DLP profile asks for a person's approval of these arguments (" + v.Describe() + "), and offline mode has no approvals"}
	case dlpRedact:
		redacted, err := json.Marshal(v.Value)
		if err != nil {
			return Decision{Kind: "deny", Reason: "dlp_blocked", Message: "the arguments could not be redacted"}
		}
		return Decision{Kind: "redact", Arguments: redacted}
	}
	return Decision{Kind: "allow"}
}

// resultTexts collects the text blocks of a tool result.
func resultTexts(result json.RawMessage) []string {
	var body struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Message string `json:"message"`
	}
	if json.Unmarshal(result, &body) != nil {
		return []string{string(result)}
	}
	var out []string
	for _, c := range body.Content {
		if c.Type == "text" && c.Text != "" {
			out = append(out, c.Text)
		}
	}
	if body.Message != "" {
		out = append(out, body.Message)
	}
	return out
}

// screenResult applies the profile to what comes back; done is false when the
// result passes as it is.
func (g *OfflineGovernor) screenResult(tool string, content json.RawMessage) (ResultDecision, bool) {
	if g.Policy.Profile == nil {
		return ResultDecision{}, false
	}
	var value any
	if json.Unmarshal(content, &value) != nil {
		return ResultDecision{}, false
	}
	v := g.Policy.Profile.Evaluate("inbound", value)
	if len(v.Summary) == 0 {
		return ResultDecision{}, false
	}
	g.audit("result_dlp", map[string]any{"tool": tool, "dlp": v.Summary})
	switch v.Action {
	case dlpBlock, dlpApproval:
		return ResultDecision{Kind: "withhold", Message: "it holds data the DLP profile keeps from the agent (" + v.Describe() + ")"}, true
	case dlpRedact:
		if redacted, err := json.Marshal(v.Value); err == nil {
			return ResultDecision{Kind: "redact", Content: redacted}, true
		}
		return ResultDecision{Kind: "withhold", Message: "it could not be redacted"}, true
	}
	return ResultDecision{}, false
}

// Result withholds a result that tells the model what to do, and strips
// invisible characters from one that is let through.
func (g *OfflineGovernor) Result(_ context.Context, _ Decision, tool string, result json.RawMessage, isErr bool) (ResultDecision, error) {
	var findings []Finding
	for _, t := range resultTexts(result) {
		findings = append(findings, ScreenText(t)...)
	}
	worst := MaxSeverity(findings)
	g.audit("result", map[string]any{"tool": tool, "error": isErr, "findings": len(findings), "severity": worst})
	if worst == SevCritical {
		return ResultDecision{Kind: "withhold", Message: "it contains instructions aimed at the model (" + findings[0].Rule + ")"}, nil
	}
	content := result
	if cleaned := StripInvisible(string(result)); cleaned != string(result) && json.Valid([]byte(cleaned)) {
		content = json.RawMessage(cleaned)
	}
	if rd, done := g.screenResult(tool, content); done {
		return rd, nil
	}
	if string(content) != string(result) {
		return ResultDecision{Kind: "redact", Content: content}, nil
	}
	if worst != "" {
		g.logf("%s: the result of %s was flagged (%s) but let through", g.Name, tool, strings.ToLower(worst))
	}
	return ResultDecision{Kind: "allow"}, nil
}
