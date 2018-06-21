package debugger

import (
	"fmt"
	"gopher2600/debugger/parser"
	"gopher2600/debugger/ui"
	"gopher2600/disassembly"
	"gopher2600/disassembly/symbols"
	"gopher2600/hardware"
	"gopher2600/hardware/cpu"
	"gopher2600/television"
	"os"
	"os/signal"
	"strconv"
	"strings"
)

// Debugger is the basic debugging frontend for the emulation
type Debugger struct {
	vcs     *hardware.VCS
	running bool
	input   []byte

	disasm disassembly.Disassembly

	breakpoints  *breakpoints
	traps        *traps
	runUntilHalt bool

	// commandOnHalt says whether an sequence of commands should run automatically
	// when emulation halts. commandOnHaltPrev is the stored command sequence
	// used when ONHALT is called with no arguments
	// halt is a breakpoint or user intervention (ie. ctrl-c)
	commandOnHalt       string
	commandOnHaltStored string

	// machineInfoVerbose controls the verbosity of commands that echo machine state
	machineInfoVerbose bool

	// input loop fields. we're storing these here because inputLoop can be
	// called from within another input loop (via a video step callback) and we
	// want these properties to persist
	inputloopHalt       bool // whether to halt the current execution loop
	inputloopNext       bool // execute a step once user input has returned a result
	inputloopVideoClock bool // step mode

	// the last result from vcs.Step() - could be a complete result or an
	// intermediate result when video-stepping
	lastResult *cpu.InstructionResult

	// user interface
	ui       ui.UserInterface
	uiSilent bool // controls whether UI is to remain silent
}

// NewDebugger is the preferred method of initialisation for the Debugger structure
func NewDebugger() (*Debugger, error) {
	var err error

	dbg := new(Debugger)

	dbg.ui = new(ui.PlainTerminal)
	if dbg.ui == nil {
		return nil, fmt.Errorf("error allocationg memory for UI")
	}

	// prepare hardware
	tv, err := television.NewSDLTV("NTSC", 3.0)
	if err != nil {
		return nil, err
	}
	dbg.vcs, err = hardware.New(tv)
	if err != nil {
		return nil, err
	}

	// allocate memory for user input
	dbg.input = make([]byte, 255)

	// set up breakpoints/traps
	dbg.breakpoints = newBreakpoints(dbg)
	dbg.traps = newTraps(dbg)

	// default ONHALT command squence
	dbg.commandOnHaltStored = "CPU; TIA; TV"

	return dbg, nil
}

// Start the main debugger sequence
func (dbg *Debugger) Start(interf ui.UserInterface, filename string) error {
	// prepare user interface
	if interf != nil {
		dbg.ui = interf
	}

	err := dbg.ui.Initialise()
	if err != nil {
		return err
	}
	defer dbg.ui.CleanUp()

	dbg.ui.RegisterTabCompleter(parser.NewTabCompletion(DebuggerCommands))

	if filename != "" {
		err = dbg.loadCartridge(filename)
		if err != nil {
			return err
		}
	} else {
		err = dbg.vcs.Reset()
		if err != nil {
			return err
		}
	}

	// register ctrl-c handler
	ctrlC := make(chan os.Signal)
	signal.Notify(ctrlC, os.Interrupt)
	go func() {
		for dbg.running {
			<-ctrlC
			if dbg.runUntilHalt {
				dbg.runUntilHalt = false
			} else {
				// TODO: interrupt os.Stdin.Read()
				dbg.running = false
			}
		}
	}()

	// prepare and run main input loop
	dbg.running = true
	err = dbg.inputLoop(true)
	if err != nil {
		return err
	}
	return nil
}

// loadCartridge makes sure that the cartridge loaded into vcs memory and the
// available disassembly/symbols are in sync. *never call vcs.AttachCartridge
// except through this funtion*
func (dbg *Debugger) loadCartridge(cartridgeFilename string) error {
	err := dbg.vcs.AttachCartridge(cartridgeFilename)
	if err != nil {
		return err
	}

	symbols, err := symbols.NewTable(cartridgeFilename)
	if err != nil {
		dbg.print(ui.Error, "%s", err)
	}

	err = dbg.disasm.ParseMemory(dbg.vcs.Mem, symbols)
	if err != nil {
		return err
	}

	return nil
}

// videoCycleCallback() and noVideoCycleCallback() are wrapper functions to be
// used when calling vcs.Step() -- video stepping uses the former and cpu
// stepping uses the latter

func (dbg *Debugger) videoCycleCallback(result *cpu.InstructionResult) error {
	dbg.lastResult = result
	dbg.print(ui.VideoStep, "%s", dbg.lastResult.GetString(dbg.disasm.Symbols, symbols.StyleFull))
	return dbg.inputLoop(false)
}

func (dbg *Debugger) noVideoCycleCallback(result *cpu.InstructionResult) error {
	return nil
}

// inputLoop has two modes, defined by the mainLoop argument. when inputLoop is
// not a "mainLoop", the function will only loop for the duration of one cpu
// step. this is used to implement video-stepping.
func (dbg *Debugger) inputLoop(mainLoop bool) error {
	var err error

	for dbg.running {
		// return immediately if we're in a mid-cycle input loop and we don't want
		// to be
		//
		// the extra condition (dbg.inputLoopNext) is to prevent execution
		// continuing if we call "stepmode cpu" in the middle of a cpu-cycle
		if !mainLoop && !dbg.inputloopVideoClock && dbg.inputloopNext {
			return nil
		}

		// check for breakpoints and traps. check() functions echo all the
		// conditions that match
		if dbg.inputloopNext {
			bpCheck := dbg.breakpoints.check()
			trCheck := dbg.traps.check()
			dbg.inputloopHalt = bpCheck || trCheck
		}

		// if haltCommand mode and if run state is correct that print haltCommand
		// command(s)
		if dbg.commandOnHalt != "" {
			if (dbg.inputloopNext && !dbg.runUntilHalt) || dbg.inputloopHalt {
				// note this is parsing input, not reading input. we're passing the
				// parse function a prepared command sequence.
				_, _ = dbg.parseInput(dbg.commandOnHalt)
			}
		}

		// expand breakpoint to include step-once/many flag
		dbg.inputloopHalt = dbg.inputloopHalt || !dbg.runUntilHalt

		if dbg.inputloopHalt {
			// pause tv when emulation has halted
			err = dbg.vcs.TV.SetPause(true)
			if err != nil {
				return err
			}

			// reset run until break condition
			dbg.runUntilHalt = false

			// build prompt
			// - different prompt depending on whether a valid disassembly is available
			var prompt string
			if p, ok := dbg.disasm.Program[dbg.vcs.MC.PC.ToUint16()]; ok {
				prompt = strings.Trim(p.GetString(dbg.disasm.Symbols, symbols.StyleBrief), " ")
				prompt = fmt.Sprintf("[ %s ] > ", prompt)
			} else {
				prompt = fmt.Sprintf("[ %#04x ] > ", dbg.vcs.MC.PC.ToUint16())
			}
			// - additional annotation if we're not showing the prompt in the main loop
			if !mainLoop && !dbg.lastResult.Final {
				prompt = fmt.Sprintf("+ %s", prompt)
			}

			// get user input
			n, err := dbg.ui.UserRead(dbg.input, prompt)
			if err != nil {
				switch err.(type) {
				case *ui.UserInterrupt:
					dbg.print(ui.Feedback, err.Error())
					dbg.running = false
					return nil
				default:
					return err
				}
			}

			// parse user input
			dbg.inputloopNext, err = dbg.parseInput(string(dbg.input[:n-1]))
			if err != nil {
				dbg.print(ui.Error, "%s", err)
			}

			// prepare for next loop
			//  o forget about current break state
			//  o prepare for matching on next breakpoint
			dbg.inputloopHalt = false
			dbg.breakpoints.prepareBreakpoints()

			// make sure tv is unpaused if emulation is about to resume
			if dbg.inputloopNext {
				err = dbg.vcs.TV.SetPause(false)
				if err != nil {
					return err
				}
			}
		}

		// move emulation on one step if user has requested/implied it
		if dbg.inputloopNext {
			if mainLoop {
				if dbg.inputloopVideoClock {
					_, dbg.lastResult, err = dbg.vcs.Step(dbg.videoCycleCallback)
				} else {
					_, dbg.lastResult, err = dbg.vcs.Step(dbg.noVideoCycleCallback)
				}
				if err != nil {
					return err
				}
				dbg.print(ui.CPUStep, "%s", dbg.lastResult.GetString(dbg.disasm.Symbols, symbols.StyleFull))
			} else {
				return nil
			}
		}
	}

	return nil
}

// parseInput splits the input into individual commands. each command is then
// passed to parseCommand for final processing
func (dbg *Debugger) parseInput(input string) (bool, error) {
	var cont bool
	var err error

	commands := strings.Split(input, ";")
	for i := 0; i < len(commands); i++ {
		cont, err = dbg.parseCommand(commands[i])
		if err != nil {
			return false, err
		}
	}

	return cont, nil
}

// parseCommand scans user input for valid commands and acts upon it. commands
// that cause the emulation to move forward (RUN, STEP) return true for the
// first return value. other commands return false and act upon the command
// immediately. note that the empty string is the same as the STEP command
func (dbg *Debugger) parseCommand(input string) (bool, error) {
	// TODO: categorise commands into script-safe and non-script-safe

	// remove leading/trailing space
	input = strings.TrimSpace(input)

	// if the input is empty then return true, indicating that the emulation
	// should "step" forward once
	if input == "" {
		return true, nil
	}

	// divide user input into parts and convert to upper-case for easy parsing
	// input is unchanged in case we need the original user-case
	parts := strings.Fields(input)

	// normalise variations in syntax
	for i := 0; i < len(parts); i++ {
		// normalise hex notation
		if parts[i][0] == '$' {
			parts[i] = fmt.Sprintf("0x%s", parts[i][1:])
		}
	}

	// normalise case of first entry in parts list (the command)
	parts[0] = strings.ToUpper(parts[0])

	if err := DebuggerCommands.CheckCommandInput(parts); err != nil {
		return false, err
	}

	// most commands do not cause the emulator to step forward
	stepNext := false

	// implement debugging command
	switch parts[0] {
	default:
		return false, fmt.Errorf("%s is not yet implemented", parts[0])

		// control of the debugger
	case KeywordHelp:
		if len(parts) == 1 {
			for k := range DebuggerCommands {
				dbg.print(ui.Help, k)
			}
		} else {
			s := strings.ToUpper(parts[1])
			txt, prs := Help[s]
			if prs == false {
				dbg.print(ui.Help, "no help for %s", s)
			} else {
				dbg.print(ui.Help, txt)
			}
		}

	case KeywordInsert:
		err := dbg.loadCartridge(parts[1])
		if err != nil {
			return false, err
		}
		dbg.print(ui.Feedback, "machine reset with new cartridge (%s)", parts[1])

	case KeywordScript:
		err := dbg.RunScript(parts[1], false)
		if err != nil {
			return false, err
		}

	case KeywordBreak:
		err := dbg.breakpoints.parseBreakpoint(parts)
		if err != nil {
			return false, err
		}

	case KeywordTrap:
		err := dbg.traps.parseTrap(parts)
		if err != nil {
			return false, err
		}

	case KeywordList:
		if len(parts) > 1 {
			switch strings.ToUpper(parts[1]) {
			case "BREAKS":
				dbg.breakpoints.list()
			case "TRAPS":
				dbg.traps.list()
			}
		}

	case KeywordClear:
		if len(parts) > 1 {
			switch strings.ToUpper(parts[1]) {
			case "BREAKS":
				dbg.breakpoints.clear()
				dbg.print(ui.Feedback, "breakpoints cleared")
			case "TRAPS":
				dbg.traps.clear()
				dbg.print(ui.Feedback, "traps cleared")
			}
		}

	case KeywordOnHalt:
		if len(parts) < 2 {
			dbg.commandOnHalt = dbg.commandOnHaltStored
		} else {
			if strings.ToUpper(parts[1]) == "OFF" {
				dbg.commandOnHalt = ""
				dbg.print(ui.Feedback, "no auto-command on halt")
				return false, nil
			}

			// TODO: implement syntax checking when specifying ONHALT commands before
			// committing to the new sequnce

			// use remaininder of command line to form the ONHALT command sequence
			dbg.commandOnHalt = strings.Join(parts[1:], " ")

			// we can't use semi-colons when specifying the sequence so allow use of
			// commas to act as an alternative
			dbg.commandOnHalt = strings.Replace(dbg.commandOnHalt, ",", ";", -1)

			// store the new command so we can reuse it
			dbg.commandOnHaltStored = dbg.commandOnHalt
		}

		dbg.print(ui.Feedback, "auto-command on halt: %s", dbg.commandOnHalt)

	case KeywordMemMap:
		dbg.print(ui.MachineInfo, "%v", dbg.vcs.Mem.MemoryMap())

	case KeywordQuit:
		dbg.running = false

	case KeywordReset:
		err := dbg.vcs.Reset()
		if err != nil {
			return false, err
		}
		dbg.print(ui.Feedback, "machine reset")

	case KeywordRun:
		dbg.runUntilHalt = true
		stepNext = true

	case KeywordStep:
		stepNext = true
		if len(parts) > 1 {
			switch parts[1] {
			case "CPU":
				dbg.inputloopVideoClock = false
			case "VIDEO":
				dbg.inputloopVideoClock = true
			}
		}

	case KeywordStepMode:
		if len(parts) > 1 {
			switch strings.ToUpper(parts[1]) {
			case "CPU":
				dbg.inputloopVideoClock = false
			case "VIDEO":
				dbg.inputloopVideoClock = true
			default:
				return false, fmt.Errorf("unknown step mode (%s)", parts[1])
			}
		}
		var stepMode string
		if dbg.inputloopVideoClock {
			stepMode = "video"
		} else {
			stepMode = "cpu"
		}
		dbg.print(ui.Feedback, "step mode: %s", stepMode)

	case KeywordTerse:
		dbg.machineInfoVerbose = false
		dbg.print(ui.Feedback, "verbosity: terse")

	case KeywordVerbose:
		dbg.machineInfoVerbose = true
		dbg.print(ui.Feedback, "verbosity: verbose")

	case KeywordVerbosity:
		if dbg.machineInfoVerbose {
			dbg.print(ui.Feedback, "verbosity: verbose")
		} else {
			dbg.print(ui.Feedback, "verbosity: terse")
		}

	case KeywordDebuggerState:
		_, err := dbg.parseInput("VERBOSITY; STEPMODE; ONHALT")
		if err != nil {
			return false, err
		}

	// information about the machine (chips)

	case KeywordCPU:
		dbg.printMachineInfo(dbg.vcs.MC)

	case KeywordPeek:
		for i := 1; i < len(parts); i++ {
			addr, err := strconv.ParseUint(parts[i], 0, 16)
			if err != nil {
				dbg.print(ui.Error, "bad argument to PEEK (%s)", parts[i])
				continue
			}

			// peform peek
			val, mappedAddress, areaName, addressLabel, err := dbg.vcs.Mem.Peek(uint16(addr))
			if err != nil {
				dbg.print(ui.Error, "%s", err)
				continue
			}

			// format results
			s := fmt.Sprintf("0x%04x", addr)
			if uint64(mappedAddress) != addr {
				s = fmt.Sprintf("%s =0x%04x", s, mappedAddress)
			}
			s = fmt.Sprintf("%s -> 0x%02x :: %s", s, val, areaName)
			if addressLabel != "" {
				s = fmt.Sprintf("%s [%s]", s, addressLabel)
			}
			dbg.print(ui.MachineInfo, s)
		}

	case KeywordRIOT:
		dbg.printMachineInfo(dbg.vcs.RIOT)

	case KeywordTIA:
		dbg.printMachineInfo(dbg.vcs.TIA)

	case KeywordTV:
		dbg.printMachineInfo(dbg.vcs.TV)

	// information about the machine (sprites, playfield)
	case KeywordPlayer:
		// TODO: argument to print either player 0 or player 1
		dbg.printMachineInfo(dbg.vcs.TIA.Video.Player0)
		dbg.printMachineInfo(dbg.vcs.TIA.Video.Player1)

	case KeywordMissile:
		// TODO: argument to print either missile 0 or missile 1
		dbg.printMachineInfo(dbg.vcs.TIA.Video.Missile0)
		dbg.printMachineInfo(dbg.vcs.TIA.Video.Missile1)

	case KeywordBall:
		dbg.printMachineInfo(dbg.vcs.TIA.Video.Ball)

	case KeywordPlayfield:
		dbg.printMachineInfo(dbg.vcs.TIA.Video.Playfield)

	// tv control

	case KeywordDisplay:
		visibility := true
		if len(parts) > 1 {
			switch parts[1] {
			case "OFF":
				visibility = false
			}
		}
		err := dbg.vcs.TV.SetVisibility(visibility)
		if err != nil {
			return false, err
		}
	}

	return stepNext, nil
}