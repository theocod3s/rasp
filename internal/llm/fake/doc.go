// Package fake is a deterministic scripted llm.Provider for tests. It emits a
// pre-programmed event sequence, which is what lets the bulk of the suite prove
// loop control flow, tool dispatch, the invariants, termination and
// cancellation at zero API cost (design §13).
//
// Does not contain: any network, any real wire format, and no behaviour a real
// provider would have to be asked about. It must never be reachable from a
// production code path.
package fake
