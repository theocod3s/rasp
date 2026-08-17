package llm

import "slices"

// Effort is how much depth a turn is asked for. The zero value is unset: an
// adapter sends no depth field at all rather than a default of its own choosing.
//
// The rungs are the union of Anthropic's output_config.effort and OpenAI's
// reasoning_effort, so neither side loses a level it accepts. A provider handed a
// rung it cannot express fails the request rather than substituting the nearest
// one it can send (decisions.md).
type Effort string

const (
	EffortNone    Effort = "none"
	EffortMinimal Effort = "minimal"
	EffortLow     Effort = "low"
	EffortMedium  Effort = "medium"
	EffortHigh    Effort = "high"
	EffortXHigh   Effort = "xhigh"
	EffortMax     Effort = "max"
)

// EffortLadder is every rung, shallowest first — the order Provider.Efforts
// publishes its own subset in. Fresh each call: an adapter filters this slice in
// place to build that subset, so a shared one would shrink for every later caller.
func EffortLadder() []Effort { return slices.Clone(ladder) }

var ladder = []Effort{
	EffortNone, EffortMinimal, EffortLow, EffortMedium, EffortHigh, EffortXHigh, EffortMax,
}
