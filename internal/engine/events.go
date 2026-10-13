package engine

import (
	"fmt"
	"time"
)

// Event is something a run reports to its sink. Events are for display only:
// nothing a run needs to remember is ever written through a sink.
type Event interface{ event() }

// Sink receives a run's events. Workers call it concurrently and wait for it,
// so it must be safe for concurrent use and return quickly.
type Sink func(Event)

// RunKind says whether a run organizes a source into the library, re-sorts
// the library in place, or only brings the index up to date.
type RunKind int

const (
	KindOrganize RunKind = iota
	KindReorganize
	KindIndex
	KindTUIAction // something a person did in the TUI, such as resolving duplicates
	KindUndo      // the undoing of an earlier run
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
	Duplicate                // moved to the duplicates folder: another copy is kept
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
	OpRecord                           // recording a move or the run in the database
	OpIndex                            // reading an audio file into the index
	OpReview                           // carrying out a person's decision on a duplicate group
	OpSaveTags                         // saving tags a person chose to a file
	OpUndo                             // undoing what a run did to a file
	OpCover                            // finding or writing an album's cover
)

var opNames = [...]string{"walk", "walk-root", "open", "rel-path", "resolve", "move", "rename", "read-cue", "save-cue-original", "rewrite-cue", "record", "index", "review", "save-tags", "undo", "cover"}

func (op Operation) String() string {
	if op < 0 || int(op) >= len(opNames) {
		return fmt.Sprintf("Operation(%d)", int(op))
	}
	return opNames[op]
}

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
	WarnLinkSkipped                     // a link or junction was left where it is
	// A temporary copy a crashed tag save or move left behind was removed,
	// or with Err set could not be.
	WarnStaleCopy
	// Something that is not a file was left where it is: a named pipe, a
	// socket or a device.
	WarnSpecialSkipped
	// The length, bit rate and other audio details of a file could not be
	// read into the index; Err says why. Its tags and its hash may have been.
	WarnDetailsUnread
	// Path and the name Name beside it differ only in letter case, which a
	// file system that tells them apart allows. Path is the one used.
	WarnCaseTwins
)

// Warning reports something that went less than right without failing the
// file. Cue warnings mean the cue sheet at Path was left unchanged.
type Warning struct {
	Kind WarningKind
	Path string
	Name string // cue warnings: the FILE name concerned; twins: the other name
	Err  error  // copy, stale copy and details warnings: the underlying error
}

// Rename is one FILE line of a cue sheet pointed at a new name.
type Rename struct {
	From, To string
}

// CoverWritten reports a cover file written into the album folder Dir, or in
// a dry run one that would be.
type CoverWritten struct {
	Dir, Path string
	// From says where the picture came from: CoverFromFile or
	// CoverFromRelease.
	From string
	// Replaced is the image file whose place it took, empty when the folder
	// had no cover.
	Replaced string
	// Asked is set in a dry run for a cover the Cover Art Archive would be
	// asked for: whether it has one is not known.
	Asked bool
}

// CoverSkipped reports an album folder a cover art run left without a cover,
// and why.
type CoverSkipped struct {
	Dir, Why string
	// Embedding is set when it is the folder's audio files that were left
	// without a picture, not the folder without a cover file.
	Embedding bool
}

// CoverEmbedded reports a picture put into the audio file at Path, or in a
// dry run one that would be.
type CoverEmbedded struct {
	Path string
	// From says where the picture came from: CoverFromFolder, CoverFromFile
	// or CoverFromRelease.
	From string
	// Replaced is set when the file held a picture, which gave way.
	Replaced bool
	// Asked is set in a dry run for a picture the Cover Art Archive would be
	// asked for: whether it has one is not known.
	Asked bool
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

// Phase is one step of a run. Phases run one after another, in the order
// listed; a run skips the ones it has no business with.
type Phase int

const (
	PhaseWalk          Phase = iota // listing the files under the roots
	PhaseIndex                      // reading new and changed audio files into the index
	PhaseAudio                      // placing audio files
	PhaseCompanions                 // placing non-audio files
	PhaseRetrySuffixed              // reorganize only: moving " (n)" files onto clean names
	PhaseCue                        // rewriting the FILE lines of moved cue sheets
	PhaseCleanup                    // removing emptied directories
	PhaseCovers                     // a cover art run: seeing to album folders
)

var phaseNames = [...]string{"walk", "index", "audio", "companions", "retry-suffixed", "cue", "cleanup", "covers"}

func (p Phase) String() string {
	if p < 0 || int(p) >= len(phaseNames) {
		return fmt.Sprintf("Phase(%d)", int(p))
	}
	return phaseNames[p]
}

// PhaseStarted opens a phase. Files and Bytes are the phase's totals, or 0
// when they are not known up front. First is set on an index phase that
// starts from an empty index, which reads every file once.
type PhaseStarted struct {
	Phase Phase
	Files int64
	Bytes int64
	First bool
}

// Progress is sent periodically while a run is working. Files and Bytes count
// what the current phase has done, against its totals from PhaseStarted. Rate
// and ByteRate are per second since the phase started. ETA is 0 when unknown;
// it goes by bytes while indexing, which reads every byte, and by files
// otherwise.
type Progress struct {
	Counters   Counters
	Phase      Phase
	Files      int64
	FilesTotal int64
	Bytes      int64
	BytesTotal int64
	Rate       float64
	ByteRate   float64
	ETA        time.Duration
	At         time.Time
}

// RunState says how a run ended.
type RunState int

const (
	StateOK          RunState = iota // finished without file errors
	StateErrors                      // finished, but some files failed
	StateInterrupted                 // cancelled before finishing
	StateNotStarted                  // the run could not be recorded, so nothing was touched
)

// RunFinished closes every run. Err says why a run could not start.
// NeedReview counts the duplicate groups in the library waiting for a person
// to review them; a dry run does not count them.
type RunFinished struct {
	// RunID is the run's row in the library's database, 0 for a run that
	// was not recorded.
	RunID      int64
	Counters   Counters
	State      RunState
	Err        error
	NeedReview int
	// NotUndone is set by Undo when the run it was to undo is not marked
	// undone and can be undone again, and says why. It is "" when that run
	// is now undone, and for every run that is not an undo.
	NotUndone string
}

func (RunStarted) event()    {}
func (FilePlaced) event()    {}
func (FileError) event()     {}
func (Warning) event()       {}
func (CueRewritten) event()  {}
func (CoverWritten) event()  {}
func (CoverSkipped) event()  {}
func (CoverEmbedded) event() {}
func (DirRemoved) event()    {}
func (PhaseStarted) event()  {}
func (Progress) event()      {}
func (RunFinished) event()   {}
