package debugger

import (
	"gopher2600/debugger/commandline"
	"gopher2600/debugger/console"
	"gopher2600/debugger/reflection"
	"gopher2600/debugger/script"
	"gopher2600/disassembly"
	"gopher2600/errors"
	"gopher2600/gui"
	"gopher2600/gui/sdl"
	"gopher2600/hardware"
	"gopher2600/hardware/cpu/definitions"
	"gopher2600/hardware/memory"
	"gopher2600/setup"
	"gopher2600/symbols"
	"gopher2600/television"
	"gopher2600/television/renderers"
	"os"
	"os/signal"
	"strings"
	"syscall"
)

const defaultOnHalt = "CPU; TV"
const defaultOnStep = "LAST"

// Debugger is the basic debugging frontend for the emulation
type Debugger struct {
	vcs    *hardware.VCS
	disasm *disassembly.Disassembly

	// gui/tv
	digest *renderers.DigestTV
	gui    gui.GUI

	// whether the debugger is to continue with the debugging loop
	// set to false only when debugger is to finish
	running bool

	// continue emulation until a halt condition is encountered
	runUntilHalt bool

	// interface to the vcs memory with additional debugging functions
	// -- access to vcs memory from the debugger (eg. peeking and poking) is
	// most fruitfully performed through this structure
	dbgmem *memoryDebug

	// reflection is used to provideo additional information about the
	// emulation. it is inherently slow so can be turned on/off with the
	// reflectProcess switch
	reflectProcess bool
	relfectMonitor *reflection.Monitor

	// halt conditions
	breakpoints *breakpoints
	traps       *traps
	watches     *watches

	// note that the UI probably allows the user to manually break or trap at
	// will, with for example, ctrl-c

	// we accumulate break, trap and watch messsages until we can service them
	// if the strings are empty then no break/trap/watch event has occurred
	breakMessages string
	trapMessages  string
	watchMessages string

	// any error from previous emulation step
	lastStepError bool

	// single-fire step traps. these are used for the STEP command, allowing
	// things like "STEP FRAME".
	stepTraps *traps

	// step command to use when input is empty
	defaultStepCommand string

	// commandOnHalt says whether an sequence of commands should run automatically
	// when emulation halts. commandOnHaltPrev is the stored command sequence
	// used when ONHALT is called with no arguments
	// halt is a breakpoint or user intervention (ie. ctrl-c)
	commandOnHalt       string
	commandOnHaltStored string

	// similarly, commandOnStep is the sequence of commands to run afer every
	// cpu/video cycle
	commandOnStep       string
	commandOnStepStored string

	// whether to display the triggering of a known CPU bug. these are bugs
	// that are known about in the emulated hardware but which might catch an
	// unwary programmer by surprise
	reportCPUBugs bool

	// input loop fields. we're storing these here because inputLoop can be
	// called from within another input loop (via a video step callback) and we
	// want these properties to persist (when a video step input loop has
	// completed and we're back into the main input loop)
	inputloopHalt bool // whether to halt the current execution loop
	inputloopNext bool // execute a step once user input has returned a result

	// granularity of single stepping - every cpu instruction or every video cycle
	// -- also affects when emulation will halt on breaks, traps and watches.
	// if inputeveryvideocycle is true then the halt may occur mid-cpu-cycle
	inputEveryVideoCycle bool

	// console interface
	console console.UserInterface

	// buffer for user input
	input []byte

	// channel for communicating with the debugger from the ctrl-c goroutine
	intChan chan os.Signal

	// channel for communicating with the debugger from the gui goroutine
	guiChan chan gui.Event

	// record user input to a script file
	scriptScribe script.Scribe
}

// NewDebugger creates and initialises everything required for a new debugging
// session. Use the Start() method to actually begin the session.
func NewDebugger(tvType string) (*Debugger, error) {
	var err error

	dbg := new(Debugger)

	// prepare gui/tv
	btv, err := television.NewStellaTelevision(tvType)
	if err != nil {
		return nil, errors.New(errors.DebuggerError, err)
	}

	dbg.digest, err = renderers.NewDigestTV(tvType, btv)
	if err != nil {
		return nil, errors.New(errors.DebuggerError, err)
	}

	dbg.gui, err = sdl.NewGUI(tvType, 2.0, btv)
	if err != nil {
		return nil, errors.New(errors.DebuggerError, err)
	}
	dbg.gui.SetFeature(gui.ReqSetAllowDebugging, true)

	// create a new VCS instance
	dbg.vcs, err = hardware.NewVCS(dbg.gui)
	if err != nil {
		return nil, errors.New(errors.DebuggerError, err)
	}

	// create instance of disassembly -- the same base structure is used
	// for disassemblies subseuquent to the first one.
	dbg.disasm = &disassembly.Disassembly{}

	// set up debugging interface to memory
	dbg.dbgmem = &memoryDebug{mem: dbg.vcs.Mem, symtable: &dbg.disasm.Symtable}

	// set up reflection monitor
	dbg.reflectProcess = true
	dbg.relfectMonitor = reflection.NewMonitor(dbg.vcs, dbg.gui)

	// set up breakpoints/traps
	dbg.breakpoints = newBreakpoints(dbg)
	dbg.traps = newTraps(dbg)
	dbg.watches = newWatches(dbg)
	dbg.stepTraps = newTraps(dbg)
	dbg.defaultStepCommand = "STEP"

	// default ONHALT command sequence
	dbg.commandOnHaltStored = defaultOnHalt

	// default ONSTEP command sequnce
	dbg.commandOnStep = defaultOnStep
	dbg.commandOnStepStored = dbg.commandOnStep

	// allocate memory for user input
	dbg.input = make([]byte, 255)

	// make synchronisation channels
	dbg.intChan = make(chan os.Signal, 1)
	dbg.guiChan = make(chan gui.Event, 2)
	signal.Notify(dbg.intChan, os.Interrupt)

	// connect debugger to gui
	dbg.gui.SetEventChannel(dbg.guiChan)

	return dbg, nil
}

// Start the main debugger sequence
func (dbg *Debugger) Start(cons console.UserInterface, initScript string, cartload memory.CartridgeLoader) error {
	// prepare user interface
	if cons == nil {
		dbg.console = new(console.PlainTerminal)
	} else {
		dbg.console = cons
	}

	err := dbg.console.Initialise()
	if err != nil {
		return errors.New(errors.DebuggerError, err)
	}
	defer dbg.console.CleanUp()

	dbg.console.RegisterTabCompleter(commandline.NewTabCompletion(debuggerCommands))

	err = dbg.loadCartridge(cartload)
	if err != nil {
		return errors.New(errors.DebuggerError, err)
	}

	dbg.running = true

	// run initialisation script
	if initScript != "" {
		dbg.console.Disable(true)

		plb, err := script.StartPlayback(initScript)
		if err != nil {
			dbg.print(console.StyleError, "error running debugger initialisation script: %s\n", err)
		}

		err = dbg.inputLoop(plb, false)
		if err != nil {
			dbg.console.Disable(false)
			return errors.New(errors.DebuggerError, err)
		}

		dbg.console.Disable(false)
	}

	// prepare and run main input loop. inputLoop will not return until
	// debugger is to exit
	err = dbg.inputLoop(dbg.console, false)
	if err != nil {
		return errors.New(errors.DebuggerError, err)
	}
	return nil
}

// loadCartridge makes sure that the cartridge loaded into vcs memory and the
// available disassembly/symbols are in sync.
//
// NEVER call vcs.AttachCartridge except through this function
//
// this is the glue that hold the cartridge and disassembly packages
// together
func (dbg *Debugger) loadCartridge(cartload memory.CartridgeLoader) error {
	err := setup.AttachCartridge(dbg.vcs, cartload)
	if err != nil {
		return err
	}

	symtable, err := symbols.ReadSymbolsFile(cartload.Filename)
	if err != nil {
		dbg.print(console.StyleError, "%s", err)
		// continuing because symtable is always valid even if err non-nil
	}

	err = dbg.disasm.FromMemory(dbg.vcs.Mem.Cart, symtable)
	if err != nil {
		return err
	}

	err = dbg.vcs.TV.Reset()
	if err != nil {
		return err
	}

	return nil
}

// videoCycle() to be used with vcs.Step()
func (dbg *Debugger) videoCycle() error {
	// because we call this callback mid-instruction, the program counter
	// maybe in its non-final state - we don't want to break or trap in those
	// instances when the final effect of the instruction changes the program
	// counter to some other value (ie. a flow, subroutine or interrupt
	// instruction)
	if !dbg.vcs.CPU.LastResult.Final &&
		dbg.vcs.CPU.LastResult.Defn != nil {
		if dbg.vcs.CPU.LastResult.Defn.Effect == definitions.Flow ||
			dbg.vcs.CPU.LastResult.Defn.Effect == definitions.Subroutine ||
			dbg.vcs.CPU.LastResult.Defn.Effect == definitions.Interrupt {
			return nil
		}

		// display information about any CPU bugs that may have been triggered
		if dbg.reportCPUBugs && dbg.vcs.CPU.LastResult.Bug != "" {
			dbg.print(console.StyleInstrument, dbg.vcs.CPU.LastResult.Bug)
		}
	}

	dbg.breakMessages = dbg.breakpoints.check(dbg.breakMessages)
	dbg.trapMessages = dbg.traps.check(dbg.trapMessages)
	dbg.watchMessages = dbg.watches.check(dbg.watchMessages)

	if dbg.reflectProcess {
		return dbg.relfectMonitor.Check()
	}

	return nil
}

// inputLoop has two modes, defined by the videoCycle argument.  when
// videoCycle is true then user will be prompted every video cycle, as opposed
// to only every cpu instruction.
//
// inputter is an instance of type UserInput. this will usually be dbg.ui but
// it could equally be an instance of debuggingScript.
func (dbg *Debugger) inputLoop(inputter console.UserInput, videoCycle bool) error {
	var err error

	// videoCycleWithInput() to be used with vcs.Step() instead of videoCycle()
	// when in video-step mode
	videoCycleWithInput := func() error {
		dbg.videoCycle()
		if dbg.commandOnStep != "" {
			_, err := dbg.parseInput(dbg.commandOnStep, false, true)
			if err != nil {
				dbg.print(console.StyleError, "%s", err)
			}
		}
		return dbg.inputLoop(inputter, true)
	}

	for {
		err = dbg.checkInterruptsAndEvents()
		if err != nil {
			return err
		}

		if !dbg.running {
			break // for loop
		}

		// this extra test is to prevent the video input loop from continuing
		// if step granularity has been switched to every cpu instruction - the
		// input loop will unravel and execution will continue in the main
		// inputLoop
		if videoCycle && !dbg.inputEveryVideoCycle && dbg.inputloopNext {
			return nil
		}

		// check for step-traps
		stepTrapMessage := dbg.stepTraps.check("")
		if stepTrapMessage != "" {
			dbg.stepTraps.clear()
		}

		// check for breakpoints and traps
		dbg.breakMessages = dbg.breakpoints.check(dbg.breakMessages)
		dbg.trapMessages = dbg.traps.check(dbg.trapMessages)
		dbg.watchMessages = dbg.watches.check(dbg.watchMessages)

		// check for halt conditions
		dbg.inputloopHalt = stepTrapMessage != "" ||
			dbg.breakMessages != "" ||
			dbg.trapMessages != "" ||
			dbg.watchMessages != "" ||
			dbg.lastStepError

		// reset last step error
		dbg.lastStepError = false

		// if commandOnHalt is defined and if run state is correct then run
		// commandOnHalt command(s)
		if dbg.commandOnHalt != "" {
			if (dbg.inputloopNext && !dbg.runUntilHalt) || dbg.inputloopHalt {
				_, err = dbg.parseInput(dbg.commandOnHalt, false, true)
				if err != nil {
					dbg.print(console.StyleError, "%s", err)
				}
			}
		}

		// print and reset accumulated break and trap messages
		dbg.print(console.StyleFeedback, dbg.breakMessages)
		dbg.print(console.StyleFeedback, dbg.trapMessages)
		dbg.print(console.StyleFeedback, dbg.watchMessages)
		dbg.breakMessages = ""
		dbg.trapMessages = ""
		dbg.watchMessages = ""

		// expand inputloopHalt to include step-once/many flag
		dbg.inputloopHalt = dbg.inputloopHalt || !dbg.runUntilHalt

		// enter halt state
		if dbg.inputloopHalt {
			// pause tv when emulation has halted
			err = dbg.gui.SetFeature(gui.ReqSetPause, true)
			if err != nil {
				return err
			}

			dbg.runUntilHalt = false

			// get user input
			n, err := inputter.UserRead(dbg.input, dbg.buildPrompt(videoCycle), dbg.guiChan, dbg.guiEventHandler)
			if err != nil {
				if !errors.IsAny(err) {
					return err
				}

				switch err.(errors.AtariError).Errno {
				case errors.UserInterrupt:
					if dbg.scriptScribe.IsActive() {
						dbg.parseInput("SCRIPT END", false, false)
						continue // for loop
					} else {

						// be polite and ask if the user really wants to
						// quit
						confirm := make([]byte, 1)
						_, err := inputter.UserRead(confirm,
							console.Prompt{
								Content: "really quit (y/n) ",
								Style:   console.StylePromptConfirm},
							nil, nil)

						if err != nil {
							if errors.Is(err, errors.UserInterrupt) {
								// treat another ctrl-c press to indicate 'yes'
								confirm[0] = 'y'
							} else {
								dbg.print(console.StyleError, err.Error())
							}
						}

						if confirm[0] == 'y' || confirm[0] == 'Y' {
							dbg.parseInput("EXIT", false, false)
						}
					}

				case errors.UserSuspend:
					// ctrl-z like process suspension
					p, err := os.FindProcess(os.Getppid())
					if err != nil {
						dbg.print(console.StyleError, "debugger doesn't seem to have a parent process")
					} else {
						// send TSTP signal to parent proces
						p.Signal(syscall.SIGTSTP)
					}

				case errors.ScriptEnd:
					// convert ScriptEnd errors to a simple print call.
					// unless we're in a video cycle input loop, in which
					// case don't print anything

					if !videoCycle {
						// !!TODO: prevent printing of ScriptEnd error for
						// initialisation script
						dbg.print(console.StyleFeedback, err.Error())
					}
					return nil

				default:
					return err
				}
			}

			err = dbg.checkInterruptsAndEvents()
			if err != nil {
				dbg.print(console.StyleError, err.Error())
			}
			if !dbg.running {
				break // for loop
			}

			// parse user input
			dbg.inputloopNext, err = dbg.parseInput(string(dbg.input[:n-1]), inputter.IsInteractive(), false)
			if err != nil {
				dbg.print(console.StyleError, "%s", err)
			}

			// prepare for next loop
			dbg.inputloopHalt = false

			// make sure tv is unpaused if emulation is about to resume
			if dbg.inputloopNext {
				err = dbg.gui.SetFeature(gui.ReqSetPause, false)
				if err != nil {
					return err
				}
			}
		}

		// move emulation on one step if user has requested/implied it
		if dbg.inputloopNext {
			if !videoCycle {
				if dbg.inputEveryVideoCycle {
					err = dbg.vcs.Step(videoCycleWithInput)
				} else {
					err = dbg.vcs.Step(dbg.videoCycle)
				}

				if err != nil {
					if !errors.IsAny(err) {
						return err
					}

					// do not exit input loop when error is a of a known type
					// set lastStepError instead and allow emulation to
					// halt
					dbg.lastStepError = true
					dbg.print(console.StyleError, "%s", err)

				} else {
					// check validity of instruction result
					if dbg.vcs.CPU.LastResult.Final {
						err := dbg.vcs.CPU.LastResult.IsValid()
						if err != nil {
							dbg.print(console.StyleError, "%s", dbg.vcs.CPU.LastResult.Defn)
							dbg.print(console.StyleError, "%s", dbg.vcs.CPU.LastResult)
							return errors.New(errors.DebuggerError, err)
						}
					}
				}

				if dbg.commandOnStep != "" {
					_, err := dbg.parseInput(dbg.commandOnStep, false, true)
					if err != nil {
						dbg.print(console.StyleError, "%s", err)
					}
				}
			} else {
				return nil
			}
		}
	}

	return nil
}

// parseInput splits the input into individual commands. each command is then
// passed to parseCommand for final processing
//
// interactive argument should be true only for input that has immediately come from
// the user. only interactive input will be added to a new script file.
//
// returns "step" status - whether or not the input should cause the emulation
// to continue at least one step (a command in the input may have set the
// runUntilHalt flag)
func (dbg *Debugger) parseInput(input string, interactive bool, auto bool) (bool, error) {
	var result parseCommandResult
	var err error
	var step bool

	// ignore comments
	if strings.HasPrefix(input, "#") {
		return false, nil
	}

	// divide input if necessary
	commands := strings.Split(input, ";")
	for i := 0; i < len(commands); i++ {

		// try to record command now if it is not a result of an "autocommand"
		// (ONSTEP, ONHALT). if there's an error as a result of parsing, it
		// will be rolled back before committing
		if !auto {
			dbg.scriptScribe.WriteInput(commands[i])
		}

		// parse command. format of command[i] wil be normalised
		result, err = dbg.parseCommand(&commands[i], interactive)
		if err != nil {
			dbg.scriptScribe.Rollback()
			return false, err
		}

		// the result from parseCommand() tells us what to do next
		switch result {
		case doNothing:
			// most commands don't require us to do anything
			break

		case stepContinue:
			// emulation should continue to next step
			step = true

		case emptyInput:
			// input was empty. if this was an interactive input then try the
			// default step command
			if interactive {
				return dbg.parseInput(dbg.defaultStepCommand, interactive, auto)
			}
			return false, nil

		case setDefaultStep:
			// command has reset what the default step command shoudl be
			dbg.defaultStepCommand = commands[i]
			step = true

		case scriptRecordStarted:
			// command has caused input script recording to begin. rollback the
			// call to recordCommand() above because we don't want to record
			// the fact that we've starting recording in the script itsel
			dbg.scriptScribe.Rollback()

		case scriptRecordEnded:
			// nothing special required when script recording has completed
		}

	}

	return step, nil
}
