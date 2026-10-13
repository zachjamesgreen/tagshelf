// Package fingerprint computes the AcoustID fingerprint of an audio file:
// what AcoustID's own tool, fpcalc, computes, by the same code.
//
// The code is Chromaprint and the audio parts of FFmpeg, compiled to one
// WebAssembly module, fp.wasm, which this package carries and runs with
// wazero, the engine that runs the tag library too. Nothing is installed or
// looked for on the machine. The module sees one folder at a time, the one
// the file is in, and can only read it. How it is built, and how to use a
// build of your own, is in module/README.md.
package fingerprint

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

//go:embed fp.wasm
var embedded []byte

// ModuleEnv names the environment variable that has tagshelf run another
// build of the module than the one it carries: the path of a fp.wasm.
const ModuleEnv = "TAGSHELF_FINGERPRINT_MODULE"

// Seconds is how much of a file is listened to, from its start: what
// AcoustID's own tool takes.
const Seconds = 120

// Print is the fingerprint of an audio file.
type Print struct {
	// Fingerprint is the text AcoustID is asked with.
	Fingerprint string
	// Duration is the length of the whole file in whole seconds, which
	// AcoustID is asked with too.
	Duration int
}

// engine is the module compiled, once for the life of the program.
type engine struct {
	rt  wazero.Runtime
	mod wazero.CompiledModule
}

var (
	once   sync.Once
	shared *engine
	broken error
)

// load compiles the module the first time it is wanted.
func load(ctx context.Context) (*engine, error) {
	once.Do(func() {
		code := embedded
		if path := os.Getenv(ModuleEnv); path != "" {
			data, err := os.ReadFile(path)
			if err != nil {
				broken = fmt.Errorf("reading the fingerprint module %s names: %w", ModuleEnv, err)
				return
			}
			code = data
		}
		// The compiling outlives the request that first asked for it.
		bg := context.WithoutCancel(ctx)
		rt := wazero.NewRuntimeWithConfig(bg, wazero.NewRuntimeConfig().WithCloseOnContextDone(true))
		if _, err := wasi_snapshot_preview1.Instantiate(bg, rt); err != nil {
			rt.Close(bg)
			broken = fmt.Errorf("starting the fingerprint module: %w", err)
			return
		}
		mod, err := rt.CompileModule(bg, code)
		if err != nil {
			rt.Close(bg)
			broken = fmt.Errorf("compiling the fingerprint module: %w", err)
			return
		}
		shared = &engine{rt: rt, mod: mod}
	})
	return shared, broken
}

// Of computes the fingerprint of the audio file at path. It reads the file
// and nothing else, and changes nothing. Cancelling ctx stops it.
func Of(ctx context.Context, path string) (Print, error) {
	e, err := load(ctx)
	if err != nil {
		return Print{}, err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return Print{}, err
	}
	if info, err := os.Stat(abs); err != nil {
		return Print{}, err
	} else if info.IsDir() {
		return Print{}, fmt.Errorf("%s is a folder", abs)
	}
	var out, problems bytes.Buffer
	cfg := wazero.NewModuleConfig().
		WithName(""). // one of many at a time
		WithArgs("fp", "/in/"+filepath.Base(abs), strconv.Itoa(Seconds)).
		WithStdout(&out).WithStderr(&problems).
		WithFSConfig(wazero.NewFSConfig().WithReadOnlyDirMount(filepath.Dir(abs), "/in"))
	mod, err := e.rt.InstantiateModule(ctx, e.mod, cfg)
	if mod != nil {
		mod.Close(context.WithoutCancel(ctx))
	}
	if ctx.Err() != nil {
		return Print{}, ctx.Err()
	}
	var exit *sys.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 0 {
		err = nil
	}
	if err != nil {
		if why := strings.TrimPrefix(strings.TrimSpace(problems.String()), "ERROR: "); why != "" {
			return Print{}, fmt.Errorf("listening to %s: %s", filepath.Base(abs), firstLine(why))
		}
		return Print{}, fmt.Errorf("listening to %s: %w", filepath.Base(abs), err)
	}
	return parse(out.String(), filepath.Base(abs))
}

// parse reads what the module printed.
func parse(out, name string) (Print, error) {
	var p Print
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		switch key {
		case "DURATION":
			n, err := strconv.Atoi(value)
			if err != nil {
				return Print{}, fmt.Errorf("listening to %s: the module gave the length %q", name, value)
			}
			p.Duration = n
		case "FINGERPRINT":
			p.Fingerprint = value
		}
	}
	if p.Fingerprint == "" {
		return Print{}, fmt.Errorf("listening to %s: the module gave no fingerprint", name)
	}
	if p.Duration <= 0 {
		// AcoustID is asked with the length too: without one the fingerprint
		// is of no use.
		return Print{}, fmt.Errorf("listening to %s: the file does not say how long it is", name)
	}
	return p, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimSpace(line)
}
