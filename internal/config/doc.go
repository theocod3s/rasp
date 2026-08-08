// Package config loads, merges and validates configuration through the
// precedence chain — defaults, global, project, env, flags — and performs the
// shell expansion that keeps secrets out of the file. It records where every
// resolved value came from, so `rasp config check` can name each origin.
//
// Does not contain: the behaviour any setting controls. It resolves values and
// never acts on them.
package config
