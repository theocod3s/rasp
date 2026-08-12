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

// File is the name every config file goes by. It stays `.json` and not
// `.jsonc` deliberately: editors in this ecosystem already apply JSONC
// tolerance to files by that name, and a `.jsonc` suffix would buy nothing but
// a filename nobody's tooling recognises (design §10).
const File = "config.json"

// Directories holding the two config files.
const (
	globalDir  = "rasp"  // under $XDG_CONFIG_HOME
	projectDir = ".rasp" // under the project root
)

// EnvBinding maps an environment variable onto a config key.
type EnvBinding struct {
	Var string
	Key string
}

// EnvBindings is the environment layer, in full.
//
// It is a fixed table rather than a naming convention because a config key is
// not an environment variable's namespace: `ANTHROPIC_API_KEY` is the name the
// rest of the world already uses for a value rasp keeps under
// `providers.anthropic.api_key`, and no mechanical rule produces both. The
// provider entries grow as the adapters land.
func EnvBindings() []EnvBinding {
	return []EnvBinding{
		{Var: "RASP_MODEL", Key: "model"},
		{Var: "RASP_SMALL_MODEL", Key: "small_model"},
		{Var: "RASP_MODE", Key: "mode"},
		{Var: "ANTHROPIC_API_KEY", Key: "providers.anthropic.api_key"},
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
// `--yolo` is deliberately absent, and not by oversight: it arms the rung-0
// permission bypass rather than setting a value, and putting it here would
// imply a lower layer could set it — which is the very thing the yolo rule in
// validate.go exists to prevent (design §10).
func FlagBindings() []FlagBinding {
	return []FlagBinding{
		{Flag: "model", Key: "model", Usage: "model to use, as provider/id"},
		{Flag: "mode", Key: "mode", Usage: "permission mode: plan, manual or auto"},
	}
}

// Sources says where a load should look. The zero value reads the real
// environment and filesystem; tests fill it in to read neither.
type Sources struct {
	// GlobalPath is the global config file. Empty derives it from
	// $XDG_CONFIG_HOME, or ~/.config below that.
	GlobalPath string

	// ProjectDir is the directory holding .rasp/. Empty means the working
	// directory.
	ProjectDir string

	// Getenv reads the environment. Nil means the process environment.
	Getenv func(string) (string, bool)

	// Flags are the command-line flags that were actually set, keyed by flag
	// name. A flag left at its default must not appear here — it would
	// otherwise outrank every file in the chain while saying nothing.
	Flags map[string]string
}

// Result is a resolved configuration together with the account of how it got
// that way.
type Result struct {
	Config Config

	// Origins gives the winning origin of every resolved value, keyed by key
	// path. Every value in Config has an entry.
	Origins Origins

	// Values holds the resolved value at each of those key paths, so a caller
	// can print the configuration without walking the typed struct.
	Values map[string]any

	// Sources lists every place Load looked, in precedence order, including
	// the ones that held nothing.
	Sources []Source

	// Warnings are problems worth saying out loud that are not worth refusing
	// to start over.
	Warnings []Warning
}

// contribution is one tree and the single origin every value in it came from.
// A layer supplies one — or, for the environment and the flags, one per
// variable and per flag, so an origin can name `RASP_MODEL` rather than "the
// environment".
type contribution struct {
	tree   tree
	origin Origin
}

// Load resolves configuration through design §10's precedence chain: built-in
// defaults, then the global file, the project file, the environment and
// finally command-line flags, each layer deep-merged onto the one below.
//
// It returns an error only for configuration that cannot be honoured — a file
// that will not parse, a mode that does not exist, a project file reaching for
// yolo. Everything softer arrives in Warnings.
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
			// Per-layer validation runs before the merge, so a rule about
			// where a value came from can still see where it came from. A
			// project file asking for yolo is rejected even when a later layer
			// would have overridden it — the file is still asking, and the
			// next run without that flag is the one that gets it.
			warnings, err := validate(c.tree, c.origin)
			if err != nil {
				return nil, err
			}
			res.Warnings = append(res.Warnings, warnings...)

			merge(merged, c.tree, c.origin, nil, res.Origins)
		}
	}

	// What will not survive the decode is inspected once against the merged
	// tree, because a key is unknown and a value is the wrong sort wherever
	// they were written — and by now the origin table can say which file that
	// was. encoding/json cannot: its errors are addressed to Go field names
	// and lose map keys entirely, so `"max_total_tools": "sixty"` would be
	// reported without naming the config file it came from.
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

	// Unknown keys are dropped as well as reported, so that what Origins
	// describes is exactly what Config holds — a report listing a setting rasp
	// is ignoring, beside the settings it is honouring, would be a worse
	// answer than the warning it already gave.
	for _, key := range unknown {
		origin, _ := res.Origins.At(key)
		res.Warnings = append(res.Warnings, Warning{
			Origin:  origin,
			Key:     key,
			Message: "unknown setting, ignored",
		})
		discard(merged, res.Origins, splitPath(key)...)
	}

	// A yolo preset override is ignored entirely rather than half-applied, so
	// it is dropped here instead of handed on. Leaving it in Config would let
	// a consumer merge it and create the impression of a constraint that is
	// not being enforced (design §10).
	discard(merged, res.Origins, "modes", ModeYolo)

	if err := decodeInto(merged, &res.Config); err != nil {
		return nil, err
	}
	res.Values = flatten(merged)
	sortWarnings(res.Warnings)
	return res, nil
}

// fileLayer reads one config file. A file that is not there is an ordinary
// outcome — most people have no project config, and many have no global one —
// so it is reported as a source that contributed nothing rather than an error.
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
// A variable that is set but empty counts as unset. Exporting an empty value is
// almost always an accident — an unset shell variable expanded into an export
// line — and letting it outrank a file the user did write would be the wrong
// reading of it every time.
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
		// A source that found nothing still says what it looked for. "Why is
		// my key not being picked up" is the question, and a bare "nothing
		// set" answers none of it.
		var candidates []string
		for _, b := range EnvBindings() {
			candidates = append(candidates, b.Var)
		}
		return nil, Source{
			Origin: Origin{Layer: LayerEnv, Detail: strings.Join(candidates, ", ")},
			Note:   "no variables set",
		}
	}
	// The layer's own entry lists what it read; each value's origin names one.
	return contributions, Source{
		Origin: Origin{Layer: LayerEnv, Detail: strings.Join(set, ", ")},
		Loaded: true,
	}
}

// flagLayer collects the flags that were set, one contribution each so an
// origin can name `--mode` — which is what the user would change.
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
		// An empty value means "not set" here for the same reason it does in
		// the environment: `rasp --model "$MODEL"` with MODEL unset is the
		// same accident as an exported empty variable, and it would otherwise
		// outrank every file in the chain while carrying no instruction.
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
		// Reached both when no flag was given and when every one given was
		// empty. A source that found nothing still says what it looked for.
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

// globalConfigPath follows the XDG base directory spec on every platform,
// because design §10 names `~/.config/rasp/config.json` outright.
// os.UserConfigDir would answer `~/Library/Application Support` on macOS,
// which is a different file from the one the documentation tells people to
// edit.
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

// ProjectPath returns the project config file's location under dir, or under
// the working directory when dir is empty.
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

// flatten renders the merged tree as key path to value, matching the paths in
// Origins one for one.
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
