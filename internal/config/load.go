package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// File is the name every config file goes by. `.json` rather than `.jsonc`
// deliberately: editors already apply JSONC tolerance by that name, and `.jsonc`
// would buy nothing but a filename nobody's tooling recognises (design §10).
const File = "config.json"

const (
	globalDir  = "rasp"  // under $XDG_CONFIG_HOME
	projectDir = ".rasp" // under the project root
)

// EnvBinding maps an environment variable onto a config key.
type EnvBinding struct {
	Var string
	Key string
}

// EnvBindings is the environment layer, in full. A fixed table rather than a
// naming convention, because no mechanical rule produces both
// `ANTHROPIC_API_KEY` and `providers.anthropic.api_key`.
//
// This layer and the flag layer both contribute strings, so a binding onto a key
// that holds a number refuses to start — `RASP_MAX_OUTPUT_TOKENS=8192` arrives as
// "a string where a whole number belongs". One needs the layer to coerce first,
// which nothing does yet.
func EnvBindings() []EnvBinding {
	return []EnvBinding{
		{Var: "RASP_MODEL", Key: "model"},
		{Var: "RASP_SMALL_MODEL", Key: "small_model"},
		{Var: "RASP_MODE", Key: "mode"},
		{Var: "ANTHROPIC_API_KEY", Key: "providers.anthropic.api_key"},
		{Var: "OPENROUTER_API_KEY", Key: "providers.openrouter.api_key"},
	}
}

// FlagBinding maps a command-line flag onto a config key.
type FlagBinding struct {
	Flag  string
	Key   string
	Usage string
}

// FlagBindings is the flag layer, in full. cmd/rasp registers its flags from
// this table rather than declaring them itself, so a flag cannot exist that the
// precedence chain does not know about.
//
// `--yolo` is deliberately absent: it arms the rung-0 permission bypass rather
// than setting a value, and putting it here would imply a lower layer could set
// it (design §10).
func FlagBindings() []FlagBinding {
	return []FlagBinding{
		{Flag: "model", Key: "model", Usage: "model to use, as provider/id"},
		{Flag: "mode", Key: "mode", Usage: "permission mode: plan, manual or auto"},
	}
}

// Sources says where a load should look; the zero value reads the real
// environment and filesystem.
type Sources struct {
	// GlobalPath is empty to derive it from $XDG_CONFIG_HOME, or ~/.config.
	GlobalPath string

	// ProjectDir holds .rasp/. Empty means the working directory.
	ProjectDir string

	// Getenv is nil for the process environment.
	Getenv func(string) (string, bool)

	// Flags are the flags that were actually set, keyed by name. One left at its
	// default must not appear: it would outrank every file while saying nothing.
	Flags map[string]string
}

// Result is a resolved configuration and the account of how it got that way.
type Result struct {
	Config Config

	// Origins gives the winning origin of every value in Config, keyed by key
	// path.
	Origins Origins

	// Values holds the resolved value at each of those key paths, so a caller can
	// print the configuration without walking the typed struct.
	Values map[string]any

	// Sources lists every place Load looked, in precedence order, including the
	// ones that held nothing.
	Sources []Source

	Warnings []Warning
}

// contribution is one tree and the origin every value in it came from. The
// environment and flag layers supply one per variable and per flag, so an origin
// can name `RASP_MODEL` rather than "the environment".
type contribution struct {
	tree   tree
	origin Origin
}

// Load resolves configuration through design §10's precedence chain: defaults,
// the global file, the project file, the environment, then flags, each layer
// deep-merged onto the one below. It errors only for configuration that cannot
// be honoured; everything softer arrives in Warnings.
func Load(src Sources) (*Result, error) {
	getenv := src.Getenv
	if getenv == nil {
		getenv = os.LookupEnv
	}

	globalPath := src.GlobalPath
	if globalPath == "" {
		var err error
		if globalPath, err = globalConfigPath(getenv); err != nil {
			return nil, err
		}
	}
	projectPath, err := projectConfigPath(src.ProjectDir)
	if err != nil {
		return nil, err
	}

	res := &Result{Origins: Origins{}}
	merged := tree{}

	for _, layer := range layers {
		var (
			contributions []contribution
			source        Source
			err           error // scoped to this layer, never carried in from the last
		)
		switch layer {
		case LayerDefault:
			origin := Origin{Layer: LayerDefault}
			contributions = []contribution{{tree: Defaults(), origin: origin}}
			source = Source{Origin: origin, Loaded: true}

		case LayerGlobal:
			contributions, source, err = fileLayer(globalPath, LayerGlobal)
		case LayerProject:
			contributions, source, err = fileLayer(projectPath, LayerProject)

		case LayerEnv:
			contributions, source = envLayer(getenv)

		case LayerFlag:
			contributions, source, err = flagLayer(src.Flags)
		}
		if err != nil {
			return nil, err
		}

		res.Sources = append(res.Sources, source)
		for _, c := range contributions {
			// Before the merge, so a rule about where a value came from can
			// still see where it came from.
			warnings, err := validate(c.tree, c.origin)
			if err != nil {
				return nil, err
			}
			res.Warnings = append(res.Warnings, warnings...)

			merge(merged, c.tree, c.origin, nil, res.Origins)
		}
	}

	// Once, against the merged tree, where the origin table can say which file a
	// bad key or value came from. encoding/json cannot: its errors are addressed
	// to Go field names and lose map keys entirely.
	unknown, mismatched := inspect(merged)
	if len(mismatched) > 0 {
		m := mismatched[0]
		origin, _ := res.Origins.At(m.key)
		return nil, &InvalidError{
			Origin: origin,
			Key:    m.key,
			Reason: fmt.Sprintf("want %s, got %s", m.want, m.got),
		}
	}

	// Dropped as well as reported, so Origins describes exactly what Config holds.
	for _, key := range unknown {
		origin, _ := res.Origins.At(key)
		res.Warnings = append(res.Warnings, Warning{
			Origin:  origin,
			Key:     key,
			Message: "unknown setting, ignored",
		})
		discard(merged, res.Origins, splitPath(key)...)
	}

	// After inspect, which is what makes every value here the sort it claims to be.
	if err := checkResolved(merged, res.Origins); err != nil {
		return nil, err
	}

	// Dropped rather than handed on: leaving it in Config would let a consumer
	// merge it into the impression of a constraint nothing enforces (design §10).
	discard(merged, res.Origins, "modes", ModeYolo)

	if err := decodeInto(merged, &res.Config); err != nil {
		return nil, err
	}
	res.Values = flatten(merged)
	sortWarnings(res.Warnings)
	return res, nil
}

// fileLayer reads one config file. A missing one is an ordinary outcome, so it
// is reported as a source that contributed nothing rather than an error.
func fileLayer(path string, layer Layer) ([]contribution, Source, error) {
	origin := Origin{Layer: layer, Detail: path}

	raw, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, Source{Origin: origin, Note: "not found"}, nil
	case err != nil:
		return nil, Source{}, fmt.Errorf("reading %s: %w", path, err)
	}

	t, err := decodeJSONC(raw, path)
	if err != nil {
		return nil, Source{}, err
	}
	return []contribution{{tree: t, origin: origin}}, Source{Origin: origin, Loaded: true}, nil
}

// envLayer collects the environment variables the bindings name, one
// contribution each so every value's origin names the variable that set it.
//
// Set-but-empty counts as unset: an exported empty value is almost always an
// unset shell variable expanded into an export line, and letting that outrank a
// file the user did write reads it wrongly every time.
func envLayer(getenv func(string) (string, bool)) ([]contribution, Source) {
	var (
		contributions []contribution
		set           []string
	)
	for _, b := range EnvBindings() {
		val, ok := getenv(b.Var)
		if !ok || val == "" {
			continue
		}
		t := tree{}
		setPath(t, val, strings.Split(b.Key, ".")...)
		contributions = append(contributions, contribution{
			tree:   t,
			origin: Origin{Layer: LayerEnv, Detail: b.Var},
		})
		set = append(set, b.Var)
	}

	if len(set) == 0 {
		// A source that found nothing still says what it looked for: "why is my
		// key not being picked up" is the question.
		var candidates []string
		for _, b := range EnvBindings() {
			candidates = append(candidates, b.Var)
		}
		return nil, Source{
			Origin: Origin{Layer: LayerEnv, Detail: strings.Join(candidates, ", ")},
			Note:   "no variables set",
		}
	}
	return contributions, Source{
		Origin: Origin{Layer: LayerEnv, Detail: strings.Join(set, ", ")},
		Loaded: true,
	}
}

// flagLayer collects the flags that were set, one contribution each so an origin
// can name `--mode`, which is what the user would change.
func flagLayer(flags map[string]string) ([]contribution, Source, error) {
	byFlag := map[string]FlagBinding{}
	for _, b := range FlagBindings() {
		byFlag[b.Flag] = b
	}

	var (
		contributions []contribution
		set           []string
	)
	for _, name := range slices.Sorted(maps.Keys(flags)) {
		b, ok := byFlag[name]
		if !ok {
			return nil, Source{}, fmt.Errorf("--%s is not a configuration flag", name)
		}
		// Empty means "not set" for the same reason it does in the environment:
		// `rasp --model "$MODEL"` with MODEL unset is the same accident as an
		// exported empty variable.
		if flags[name] == "" {
			continue
		}
		t := tree{}
		setPath(t, flags[name], strings.Split(b.Key, ".")...)
		contributions = append(contributions, contribution{
			tree:   t,
			origin: Origin{Layer: LayerFlag, Detail: name},
		})
		set = append(set, "--"+name)
	}

	if len(set) == 0 {
		// Reached when no flag was given, and when every one given was empty.
		var candidates []string
		for _, b := range FlagBindings() {
			candidates = append(candidates, "--"+b.Flag)
		}
		return nil, Source{
			Origin: Origin{Layer: LayerFlag, Detail: strings.Join(candidates, ", ")},
			Note:   "none set",
		}, nil
	}

	return contributions, Source{
		Origin: Origin{Layer: LayerFlag, Detail: strings.Join(set, ", ")},
		Loaded: true,
	}, nil
}

// GlobalPath returns the global config file's location.
func GlobalPath() (string, error) { return globalConfigPath(os.LookupEnv) }

// globalConfigPath follows the XDG base directory spec on every platform, because
// design §10 names `~/.config/rasp/config.json` outright. os.UserConfigDir would
// answer `~/Library/Application Support` on macOS, which is a different file from
// the one the documentation tells people to edit.
func globalConfigPath(getenv func(string) (string, bool)) (string, error) {
	if dir, ok := getenv("XDG_CONFIG_HOME"); ok && dir != "" {
		return filepath.Join(dir, globalDir, File), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating the global config: %w", err)
	}
	return filepath.Join(home, ".config", globalDir, File), nil
}

// ProjectPath returns the project config file's location under dir, or under the
// working directory when dir is empty.
func ProjectPath(dir string) (string, error) { return projectConfigPath(dir) }

func projectConfigPath(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("locating the project config: %w", err)
		}
		dir = wd
	}
	return filepath.Join(dir, projectDir, File), nil
}

// flatten renders the merged tree as key path to value, matching Origins.
func flatten(t tree) map[string]any {
	values := map[string]any{}
	var walk func(val any, path []string)
	walk = func(val any, path []string) {
		sub, isObj := val.(tree)
		if !isObj || len(sub) == 0 {
			if len(path) > 0 {
				values[joinPath(path)] = val
			}
			return
		}
		for _, key := range sortedKeys(sub) {
			walk(sub[key], childPath(path, key))
		}
	}
	walk(t, nil)
	return values
}
