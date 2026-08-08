// Package retry implements the two-tier retry policy — transport-level and
// response-level — shared by every adapter.
//
// Does not contain: wire translation, and no knowledge of which provider it is
// retrying or what the messages say. It decides whether and when to try again,
// nothing more.
package retry
