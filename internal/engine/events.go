package engine

import "time"

// Event is something a run reports to its sink. Events are for display only:
// nothing a run needs to remember is ever written through a sink.
type Event interface{ event() }

// Sink receives a run's events. Workers call it concurrently and wait for it,
// so it must be safe for concurrent use and return quickly.
type Sink func(Event)

// RunKind says whether a run organizes a source into the library or re-sorts
// the library in place.
type RunKind int

const (
	KindOrganize RunKind = iota
	KindReorganize
)

// RunStarted opens every run, before anything is walked.
type RunStarted struct {
	Kind        RunKind
	DryRun      bool
	Source      string
	Destination string
	Roots       []string // the directories the run walks
}

// Outcome says what happened to a file, or in a dry run what would.
type Outcome int

const (
	Organized Outcome = iota // moved into the library
	Untagged                 // moved to the untagged folder
	Other                    // non-audio file moved to the other folder
	Companion                // non-audio file moved next to its album
	Duplicate                // left alone: an identical copy is already at Destination
	InPlace                  // already at its destination
	Left                     // reorganize only: does not qualify, stays where it is
	Renamed                  // moved from a " (n)" name onto its clean name
)

// FilePlaced reports a file's outcome. For Renamed, Path is the " (n)" name
// the file had landed on.
type FilePlaced struct {
	Path        string
	Destination string
	Outcome     Outcome
}

// Operation names the step a FileError happened in.
type Operation int

const (
	OpWalk            Operation = iota // listing a path under a root
	OpWalkRoot                         // the walk of a root as a whole; Path is the root
	OpOpen                             // opening an audio file to read its tags
	OpRelPath                          // working out a path relative to the source
	OpResolve                          // picking and reserving a destination
	OpMove                             // moving a file to its destination
	OpRename                           // moving a file onto its clean name
	OpReadCue                          // reading a cue sheet
	OpSaveCueOriginal                  // keeping a cue sheet's original as .orig
	OpRewriteCue                       // writing a rewritten cue sheet
)

// FileError reports a failure affecting one file. Destination is set for the
// operations that have one.
type FileError struct {
	Path        string
	Destination string
	Op          Operation
	Err         error
}

// WarningKind says what a Warning is about.
type WarningKind int

const (
	WarnPermissions  WarningKind = iota // a copied file lost its permissions
	WarnModTime                         // a copied file lost its modification time
	WarnCueNotBeside                    // a cue's FILE Name is not next to it after the move
	WarnCueEncoding                     // a non-UTF-8 cue would need the non-ASCII Name
)

// Warning reports something that went less than right without failing the
// file. Cue warnings mean the cue sheet at Path was left unchanged.
type Warning struct {
	Kind WarningKind
	Path string
	Name string // cue warnings: the FILE name concerned
	Err  error  // copy warnings: the underlying error
}

// Rename is one FILE line of a cue sheet pointed at a new name.
type Rename struct {
	From, To string
}

// CueRewritten reports a cue sheet whose FILE lines were pointed at the new
// names of its audio. Path is where the cue sheet now lives.
type CueRewritten struct {
	Path    string
	Renames []Rename
}

// DirRemoved reports an emptied directory removed at the end of a run.
type DirRemoved struct {
	Path string
}

// Progress is sent periodically while a run is working.
type Progress struct {
	Counters Counters
	At       time.Time
}

// RunState says how a run ended.
type RunState int

const (
	StateOK          RunState = iota // finished without file errors
	StateErrors                      // finished, but some files failed
	StateInterrupted                 // cancelled before finishing
)

// RunFinished closes every run.
type RunFinished struct {
	Counters Counters
	State    RunState
}

func (RunStarted) event()   {}
func (FilePlaced) event()   {}
func (FileError) event()    {}
func (Warning) event()      {}
func (CueRewritten) event() {}
func (DirRemoved) event()   {}
func (Progress) event()     {}
func (RunFinished) event()  {}
