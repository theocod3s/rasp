// Package auth defines the Credential interface and its implementations. The
// interface resolves per model call, which is the seam OAuth lands on later
// without a new call site (design §15).
//
// Does not contain: provider wire code, HTTP, and no choice about which
// credential a provider gets — that is configuration. Nothing here is ever
// logged.
package auth
