// This file is part of Gopher2600.
//
// Gopher2600 is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// Gopher2600 is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with Gopher2600.  If not, see <https://www.gnu.org/licenses/>.
//
// *** NOTE: all historical versions of this file, as found in any
// git repository, are also covered by the licence, even when this
// notice is not present ***

package debugger

import (
	"bytes"
	"fmt"
	"gopher2600/cartridgeloader"
	"gopher2600/debugger/script"
	"gopher2600/debugger/terminal"
	"gopher2600/debugger/terminal/commandline"
	"gopher2600/disassembly"
	"gopher2600/errors"
	"gopher2600/gui"
	"gopher2600/hardware/cpu/registers"
	"gopher2600/hardware/memory/addresses"
	"gopher2600/hardware/memory/memorymap"
	"gopher2600/hardware/riot/input"
	"gopher2600/patch"
	"gopher2600/symbols"
	"sort"
	"strconv"
	"strings"
)

var debuggerCommands *commandline.Commands
var scriptUnsafeCommands *commandline.Commands

// this init() function "compiles" the commandTemplate above into a more
// usuable form. It will cause the program to fail if the template is invalid.
func init() {
	var err error

	// parse command template
	debuggerCommands, err = commandline.ParseCommandTemplate(commandTemplate)
	if err != nil {
		panic(err)
	}

	err = debuggerCommands.AddHelp(cmdHelp, helps)
	if err != nil {
		panic(err)
	}
	sort.Stable(debuggerCommands)

	scriptUnsafeCommands, err = commandline.ParseCommandTemplate(scriptUnsafeTemplate)
	if err != nil {
		panic(err)
	}
	sort.Stable(scriptUnsafeCommands)
}

// parseCommand/enactCommand scans user input for a valid command and acts upon
// it. see parseInput for explanation of args.
func (dbg *Debugger) parseCommand(cmd string, scribe bool, echo bool) (bool, error) {
	// tokenise input
	tokens := commandline.TokeniseInput(cmd)

	// if there are no tokens in the input then continue with onEmptyInput
	if tokens.Remaining() == 0 {
		return dbg.parseCommand(onEmptyInput, true, false)
	}

	// check validity of tokenised input
	err := debuggerCommands.ValidateTokens(tokens)
	if err != nil {
		// print normalised input and return error
		dbg.printLine(terminal.StyleInput, tokens.String())
		return false, err
	}

	// print normalised input if this is command from an interactive source
	// and not an auto-command
	if echo {
		dbg.printLine(terminal.StyleInput, tokens.String())
	}

	// test to see if command is allowed when recording/playing a script
	if dbg.scriptScribe.IsActive() && scribe {
		tokens.Reset()

		err := scriptUnsafeCommands.ValidateTokens(tokens)

		// fail when the tokens DO match the scriptUnsafe template (ie. when
		// there is no err from the validate function)
		if err == nil {
			return false, errors.New(errors.CommandError, fmt.Sprintf("'%s' is unsafe to use in scripts", tokens.String()))
		}

		// record command if it auto is false (is not a result of an "auto" command
		// eg. ONHALT). if there's an error then the script will be rolled back and
		// the write removed.
		dbg.scriptScribe.WriteInput(tokens.String())
	}

	// check first token. if this token makes sense then we will consume the
	// rest of the tokens appropriately
	tokens.Reset()
	command, _ := tokens.Get()

	switch command {
	default:
		return false, errors.New(errors.CommandError, fmt.Sprintf("%s is not yet implemented", command))

	case cmdHelp:
		keyword, ok := tokens.Get()
		if ok {
			dbg.printLine(terminal.StyleHelp, debuggerCommands.Help(keyword))
		} else {
			dbg.printLine(terminal.StyleHelp, debuggerCommands.HelpOverview())
		}

		// help can be called during script recording but we don't want to
		// include it
		dbg.scriptScribe.Rollback()

		return false, nil

	case cmdQuit:
		if dbg.scriptScribe.IsActive() {
			dbg.printLine(terminal.StyleFeedback, "ending script recording")

			// QUIT when script is being recorded is the same as SCRIPT END
			//
			// we don't want the QUIT command to appear in the script so
			// rollback last entry before we commit it in EndSession()
			dbg.scriptScribe.Rollback()
			dbg.scriptScribe.EndSession()
		} else {
			dbg.running = false
		}

	case cmdReset:
		err := dbg.vcs.Reset()
		if err != nil {
			return false, err
		}
		err = dbg.tv.Reset()
		if err != nil {
			return false, err
		}
		dbg.printLine(terminal.StyleFeedback, "machine reset")

	case cmdRun:
		dbg.runUntilHalt = true
		return true, nil

	case cmdHalt:
		dbg.haltImmediately = true

	case cmdStep:
		mode, _ := tokens.Get()
		mode = strings.ToUpper(mode)
		switch mode {
		case "":
			// calling step with no argument is the normal case
		case "CPU":
			// changes quantum
			dbg.quantum = QuantumCPU
		case "VIDEO":
			// changes quantum
			dbg.quantum = QuantumVideo
		default:
			// does not change quantum
			tokens.Unget()
			err := dbg.stepTraps.parseTrap(tokens)
			if err != nil {
				return false, errors.New(errors.CommandError, fmt.Sprintf("unknown step mode (%s)", mode))
			}
			dbg.runUntilHalt = true
		}

		return true, nil

	case cmdQuantum:
		mode, ok := tokens.Get()
		if ok {
			mode = strings.ToUpper(mode)
			switch mode {
			case "CPU":
				dbg.quantum = QuantumCPU
			case "VIDEO":
				dbg.quantum = QuantumVideo
			default:
				// already caught by command line ValidateTokens()
			}
		}
		dbg.printLine(terminal.StyleFeedback, "set to %s", dbg.quantum)

	case cmdScript:
		option, _ := tokens.Get()
		switch strings.ToUpper(option) {
		case "RECORD":
			var err error
			saveFile, _ := tokens.Get()
			err = dbg.scriptScribe.StartSession(saveFile)
			if err != nil {
				return false, err
			}

			// we don't want SCRIPT RECORD command to appear in the
			// script
			dbg.scriptScribe.Rollback()

			return false, nil

		case "END":
			dbg.scriptScribe.Rollback()
			err := dbg.scriptScribe.EndSession()
			return false, err

		default:
			// run a script
			scr, err := script.RescribeScript(option)
			if err != nil {
				return false, err
			}

			if dbg.scriptScribe.IsActive() {
				// if we're currently recording a script we want to write this
				// command to the new script file but indicate that we'll be
				// entering a new script and so don't want to repeat the
				// commands from that script
				dbg.scriptScribe.StartPlayback()

				defer func() {
					dbg.scriptScribe.EndPlayback()
				}()
			}

			err = dbg.inputLoop(scr, false)
			if err != nil {
				return false, err
			}
		}

	case cmdInsert:
		cart, _ := tokens.Get()
		err := dbg.loadCartridge(cartridgeloader.Loader{Filename: cart})
		if err != nil {
			return false, err
		}
		dbg.printLine(terminal.StyleFeedback, "machine reset with new cartridge (%s)", cart)

	case cmdCartridge:
		arg, ok := tokens.Get()
		if ok {
			switch arg {
			case "ANALYSIS":
				dbg.printLine(terminal.StyleFeedback, dbg.disasm.Analysis.String())
			case "BANK":
				bank, _ := tokens.Get()
				n, _ := strconv.Atoi(bank)
				err := dbg.vcs.Mem.Cart.SetBank(dbg.vcs.CPU.PC.Address(), n)
				if err != nil {
					return false, err
				}

				err = dbg.vcs.CPU.LoadPCIndirect(addresses.Reset)
				if err != nil {
					return false, err
				}
			}
		} else {
			dbg.printInstrument(dbg.vcs.Mem.Cart)
		}

	case cmdPatch:
		f, _ := tokens.Get()
		patched, err := patch.CartridgeMemory(dbg.vcs.Mem.Cart, f)
		if err != nil {
			dbg.printLine(terminal.StyleError, "%v", err)
			if patched {
				dbg.printLine(terminal.StyleError, "error during patching. cartridge might be unusable.")
			}
			return false, nil
		}
		if patched {
			dbg.printLine(terminal.StyleFeedback, "cartridge patched")
		}

	case cmdDisassembly:
		bytecode := false
		bank := -1

		arg, ok := tokens.Get()
		if ok {
			switch arg {
			case "BYTECODE":
				bytecode = true
			default:
				bank, _ = strconv.Atoi(arg)
			}
		}

		var err error

		attr := disassembly.WriteAttr{ByteCode: bytecode}
		s := &bytes.Buffer{}

		if bank == -1 {
			err = dbg.disasm.Write(s, attr)
		} else {
			err = dbg.disasm.WriteBank(s, attr, bank)
		}

		if err != nil {
			return false, err
		}

		dbg.printLine(terminal.StyleFeedback, s.String())

	case cmdGrep:
		scope := disassembly.GrepAll

		s, _ := tokens.Get()
		switch strings.ToUpper(s) {
		case "MNEMONIC":
			scope = disassembly.GrepMnemonic
		case "OPERAND":
			scope = disassembly.GrepOperand
		default:
			tokens.Unget()
		}

		search, _ := tokens.Get()
		output := strings.Builder{}
		err := dbg.disasm.Grep(&output, scope, search, false)
		if err != nil {
			return false, nil
		}
		if output.Len() == 0 {
			dbg.printLine(terminal.StyleError, "%s not found in disassembly", search)
		} else {
			dbg.printLine(terminal.StyleFeedback, output.String())
		}

	case cmdSymbol:
		tok, _ := tokens.Get()
		switch strings.ToUpper(tok) {
		case "LIST":
			option, ok := tokens.Get()
			if ok {
				switch strings.ToUpper(option) {
				default:
					// already caught by command line ValidateTokens()

				case "LOCATIONS":
					dbg.disasm.Symtable.ListLocations(dbg.printStyle(terminal.StyleFeedback))

				case "READ":
					dbg.disasm.Symtable.ListReadSymbols(dbg.printStyle(terminal.StyleFeedback))

				case "WRITE":
					dbg.disasm.Symtable.ListWriteSymbols(dbg.printStyle(terminal.StyleFeedback))
				}
			} else {
				dbg.disasm.Symtable.ListSymbols(dbg.printStyle(terminal.StyleFeedback))
			}

		default:
			symbol := tok
			table, symbol, address, err := dbg.disasm.Symtable.SearchSymbol(symbol, symbols.UnspecifiedSymTable)
			if err != nil {
				if errors.Is(err, errors.SymbolUnknown) {
					dbg.printLine(terminal.StyleFeedback, "%s -> not found", symbol)
					return false, nil
				}
				return false, err
			}

			option, ok := tokens.Get()
			if ok {
				switch strings.ToUpper(option) {
				default:
					// already caught by command line ValidateTokens()

				case "ALL", "MIRRORS":
					dbg.printLine(terminal.StyleFeedback, "%s -> %#04x", symbol, address)

					// find all instances of symbol address in memory space
					// assumption: the address returned by SearchSymbol is the
					// first address in the complete list
					for m := address + 1; m < memorymap.OriginCart; m++ {
						ai := dbg.dbgmem.mapAddress(m, table == symbols.ReadSymTable)
						if ai.mappedAddress == address {
							dbg.printLine(terminal.StyleFeedback, "%s (%s) -> %#04x", symbol, table, m)
						}
					}
				}
			} else {
				dbg.printLine(terminal.StyleFeedback, "%s (%s) -> %#04x", symbol, table, address)
			}
		}

	case cmdOnHalt:
		if tokens.Remaining() == 0 {
			if dbg.commandOnHalt == "" {
				dbg.printLine(terminal.StyleFeedback, "auto-command on halt: OFF")
			} else {
				dbg.printLine(terminal.StyleFeedback, "auto-command on halt: %s", dbg.commandOnHalt)
			}
			return false, nil
		}

		// !!TODO: non-interactive check of tokens against scriptUnsafeTemplate
		var newOnHalt string

		option, _ := tokens.Get()
		switch strings.ToUpper(option) {
		case "OFF":
			newOnHalt = ""
		case "ON":
			newOnHalt = dbg.commandOnHaltStored
		default:
			// token isn't one we recognise so push it back onto the token queue
			tokens.Unget()

			// use remaininder of command line to form the ONHALT command sequence
			newOnHalt = tokens.Remainder()
			tokens.End()

			// we can't use semi-colons when specifying the sequence so allow use of
			// commas to act as an alternative
			newOnHalt = strings.Replace(newOnHalt, ",", ";", -1)
		}

		dbg.commandOnHalt = newOnHalt

		// display the new/restored ONHALT command(s)
		if newOnHalt == "" {
			dbg.printLine(terminal.StyleFeedback, "auto-command on halt: OFF")
		} else {
			dbg.printLine(terminal.StyleFeedback, "auto-command on halt: %s", dbg.commandOnHalt)

			// store the new command so we can reuse it after an ONHALT OFF
			//
			// !!TODO: normalise case of specified command sequence
			dbg.commandOnHaltStored = newOnHalt
		}

		return false, nil

	case cmdOnStep:
		if tokens.Remaining() == 0 {
			if dbg.commandOnStep == "" {
				dbg.printLine(terminal.StyleFeedback, "auto-command on step: OFF")
			} else {
				dbg.printLine(terminal.StyleFeedback, "auto-command on step: %s", dbg.commandOnStep)
			}
			return false, nil
		}

		// !!TODO: non-interactive check of tokens against scriptUnsafeTemplate
		var newOnStep string

		option, _ := tokens.Get()
		switch strings.ToUpper(option) {
		case "OFF":
			newOnStep = ""
		case "ON":
			newOnStep = dbg.commandOnStepStored
		default:
			// token isn't one we recognise so push it back onto the token queue
			tokens.Unget()

			// use remaininder of command line to form the ONSTEP command sequence
			newOnStep = tokens.Remainder()
			tokens.End()

			// we can't use semi-colons when specifying the sequence so allow use of
			// commas to act as an alternative
			newOnStep = strings.Replace(newOnStep, ",", ";", -1)
		}

		dbg.commandOnStep = newOnStep

		// display the new/restored ONSTEP command(s)
		if newOnStep == "" {
			dbg.printLine(terminal.StyleFeedback, "auto-command on step: OFF")
		} else {
			dbg.printLine(terminal.StyleFeedback, "auto-command on step: %s", dbg.commandOnStep)

			// store the new command so we can reuse it after an ONSTEP OFF
			// !!TODO: normalise case of specified command sequence
			dbg.commandOnStepStored = newOnStep
		}

		return false, nil

	case cmdLast:
		s := strings.Builder{}

		d, err := dbg.disasm.FormatResult(dbg.vcs.CPU.LastResult)
		if err != nil {
			return false, err
		}

		option, ok := tokens.Get()
		if ok {
			switch strings.ToUpper(option) {
			case "DEFN":
				if dbg.vcs.CPU.LastResult.Defn == nil {
					dbg.printLine(terminal.StyleFeedback, "no instruction decoded yet")
				} else {
					dbg.printLine(terminal.StyleFeedback, "%s", dbg.vcs.CPU.LastResult.Defn)
				}
				return false, nil

			case "BYTECODE":
				s.WriteString(dbg.disasm.GetField(disassembly.FldBytecode, d))
			}
		}

		s.WriteString(dbg.disasm.GetField(disassembly.FldAddress, d))
		s.WriteString(" ")
		s.WriteString(dbg.disasm.GetField(disassembly.FldMnemonic, d))
		s.WriteString(" ")
		s.WriteString(dbg.disasm.GetField(disassembly.FldOperand, d))
		s.WriteString(" ")
		s.WriteString(dbg.disasm.GetField(disassembly.FldActualCycles, d))
		s.WriteString(" ")
		s.WriteString(dbg.disasm.GetField(disassembly.FldActualNotes, d))

		if dbg.vcs.CPU.LastResult.Final {
			dbg.printLine(terminal.StyleCPUStep, s.String())
		} else {
			dbg.printLine(terminal.StyleVideoStep, s.String())
		}

	case cmdMemMap:
		dbg.printLine(terminal.StyleInstrument, "%v", memorymap.Summary())

	case cmdCPU:
		action, ok := tokens.Get()
		if ok {
			switch strings.ToUpper(action) {
			case "SET":
				target, _ := tokens.Get()
				value, _ := tokens.Get()

				target = strings.ToUpper(target)
				if target == "PC" {
					// program counter can be a 16 bit number
					v, err := strconv.ParseUint(value, 0, 16)
					if err != nil {
						dbg.printLine(terminal.StyleError, "value must be a positive 16 number")
					}

					dbg.vcs.CPU.PC.Load(uint16(v))
				} else {
					// 6507 registers are 8 bit
					v, err := strconv.ParseUint(value, 0, 8)
					if err != nil {
						dbg.printLine(terminal.StyleError, "value must be a positive 8 number")
					}

					var reg *registers.Register
					switch strings.ToUpper(target) {
					case "A":
						reg = dbg.vcs.CPU.A
					case "X":
						reg = dbg.vcs.CPU.X
					case "Y":
						reg = dbg.vcs.CPU.Y
					case "SP":
						reg = dbg.vcs.CPU.SP
					}

					reg.Load(uint8(v))
				}

			default:
				// already caught by command line ValidateTokens()
			}
		} else {
			dbg.printInstrument(dbg.vcs.CPU)
		}

	case cmdPeek:
		// get first address token
		a, ok := tokens.Get()

		for ok {
			// perform peek
			ai, err := dbg.dbgmem.peek(a)
			if err != nil {
				dbg.printLine(terminal.StyleError, "%s", err)
			} else {
				dbg.printLine(terminal.StyleInstrument, ai.String())
			}

			// loop through all addresses
			a, ok = tokens.Get()
		}

	case cmdPoke:
		// get address token
		a, _ := tokens.Get()

		// convert address
		ai := dbg.dbgmem.mapAddress(a, false)
		if ai == nil {
			// using poke error because hexload is basically the same as poking
			dbg.printLine(terminal.StyleError, errors.New(errors.UnpokeableAddress, a).Error())
			return false, nil
		}
		addr := ai.mappedAddress

		// get (first) value token
		v, ok := tokens.Get()

		for ok {
			val, err := strconv.ParseUint(v, 0, 8)
			if err != nil {
				dbg.printLine(terminal.StyleError, "value must be an 8 bit number (%s)", v)
				v, ok = tokens.Get()
				continue // for loop (without advancing address)
			}

			// perform individual poke
			ai, err := dbg.dbgmem.poke(addr, uint8(val))
			if err != nil {
				dbg.printLine(terminal.StyleError, "%s", err)
			} else {
				dbg.printLine(terminal.StyleInstrument, ai.String())
			}

			// loop through all values
			v, ok = tokens.Get()
			addr++
		}

	case cmdRAM:
		option, ok := tokens.Get()
		if ok {
			option = strings.ToUpper(option)
			switch option {
			case "CART":
				cartRAM := dbg.vcs.Mem.Cart.RAM()
				if len(cartRAM) > 0 {
					// !!TODO: better okation of cartridge RAM
					dbg.printLine(terminal.StyleInstrument, fmt.Sprintf("%v", dbg.vcs.Mem.Cart.RAM()))
				} else {
					dbg.printLine(terminal.StyleFeedback, "cartridge does not contain any additional RAM")
				}

			}
		} else {
			dbg.printInstrument(dbg.vcs.Mem.RAM)
		}

	case cmdTimer:
		dbg.printInstrument(dbg.vcs.RIOT.Timer)

	case cmdTIA:
		option, ok := tokens.Get()
		if ok {
			option = strings.ToUpper(option)
			switch option {
			case "DELAYS":
				// for convience asking for TIA delays also prints delays for
				// the sprites
				dbg.printInstrument(dbg.vcs.TIA.Delay)
				dbg.printInstrument(dbg.vcs.TIA.Video.Player0.Delay)
				dbg.printInstrument(dbg.vcs.TIA.Video.Player1.Delay)
				dbg.printInstrument(dbg.vcs.TIA.Video.Missile0.Delay)
				dbg.printInstrument(dbg.vcs.TIA.Video.Missile1.Delay)
				dbg.printInstrument(dbg.vcs.TIA.Video.Ball.Delay)
			}
		} else {
			dbg.printInstrument(dbg.vcs.TIA)
		}

	case cmdAudio:
		dbg.printInstrument(dbg.vcs.TIA.Audio)

	case cmdTV:
		option, ok := tokens.Get()
		if ok {
			option = strings.ToUpper(option)
			switch option {
			case "SPEC":
				dbg.printLine(terminal.StyleInstrument, dbg.tv.GetSpec().ID)
			default:
				// already caught by command line ValidateTokens()
			}
		} else {
			dbg.printInstrument(dbg.tv)
		}

	// information about the machine (sprites, playfield)
	case cmdPlayer:
		plyr := -1

		arg, _ := tokens.Get()
		switch arg {
		case "0":
			plyr = 0
		case "1":
			plyr = 1
		}

		switch plyr {
		case 0:
			dbg.printInstrument(dbg.vcs.TIA.Video.Player0)

		case 1:
			dbg.printInstrument(dbg.vcs.TIA.Video.Player1)

		default:
			dbg.printInstrument(dbg.vcs.TIA.Video.Player0)
			dbg.printInstrument(dbg.vcs.TIA.Video.Player1)
		}

	case cmdMissile:
		miss := -1

		arg, _ := tokens.Get()
		switch arg {
		case "0":
			miss = 0
		case "1":
			miss = 1
		}

		switch miss {
		case 0:
			dbg.printInstrument(dbg.vcs.TIA.Video.Missile0)

		case 1:
			dbg.printInstrument(dbg.vcs.TIA.Video.Missile1)

		default:
			dbg.printInstrument(dbg.vcs.TIA.Video.Missile0)
			dbg.printInstrument(dbg.vcs.TIA.Video.Missile1)
		}

	case cmdBall:
		dbg.printInstrument(dbg.vcs.TIA.Video.Ball)

	case cmdPlayfield:
		dbg.printInstrument(dbg.vcs.TIA.Video.Playfield)

	case cmdDisplay:
		var err error

		action, _ := tokens.Get()
		action = strings.ToUpper(action)
		switch action {
		case "ON":
			err = dbg.scr.SetFeature(gui.ReqSetVisibility, true)
			if err != nil {
				return false, err
			}
		case "OFF":
			err = dbg.scr.SetFeature(gui.ReqSetVisibility, false)
			if err != nil {
				return false, err
			}

		case "MASK":
			err = dbg.scr.SetFeature(gui.ReqSetMasking, false)
			if err != nil {
				return false, err
			}

		case "UNMASK":
			err = dbg.scr.SetFeature(gui.ReqSetMasking, true)
			if err != nil {
				return false, err
			}

		case "SCALE":
			scl, ok := tokens.Get()
			if !ok {
				return false, errors.New(errors.CommandError, fmt.Sprintf("value required for %s %s", cmdDisplay, action))
			}

			scale, err := strconv.ParseFloat(scl, 32)
			if err != nil {
				return false, errors.New(errors.CommandError, fmt.Sprintf("%s %s value not valid (%s)", cmdDisplay, action, scl))
			}

			err = dbg.scr.SetFeature(gui.ReqSetScale, float32(scale))
			return false, err
		case "ALT":
			action, _ := tokens.Get()
			action = strings.ToUpper(action)
			switch action {
			case "OFF":
				err = dbg.scr.SetFeature(gui.ReqSetAltColors, false)
				if err != nil {
					return false, err
				}
			case "ON":
				err = dbg.scr.SetFeature(gui.ReqSetAltColors, true)
				if err != nil {
					return false, err
				}
			default:
				err = dbg.scr.SetFeature(gui.ReqToggleAltColors)
				if err != nil {
					return false, err
				}
			}
		case "OVERLAY":
			action, _ := tokens.Get()
			action = strings.ToUpper(action)
			switch action {
			case "OFF":
				err = dbg.scr.SetFeature(gui.ReqSetOverlay, false)
				if err != nil {
					return false, err
				}
			case "ON":
				err = dbg.scr.SetFeature(gui.ReqSetOverlay, true)
				if err != nil {
					return false, err
				}
			default:
				err = dbg.scr.SetFeature(gui.ReqToggleOverlay)
				if err != nil {
					return false, err
				}
			}
		default:
			err = dbg.scr.SetFeature(gui.ReqToggleVisibility)
			if err != nil {
				return false, err
			}
		}

	case cmdPanel:
		mode, _ := tokens.Get()
		switch strings.ToUpper(mode) {
		case "TOGGLE":
			arg, _ := tokens.Get()
			switch strings.ToUpper(arg) {
			case "P0":
				dbg.vcs.Panel.Handle(input.PanelTogglePlayer0Pro, nil)
			case "P1":
				dbg.vcs.Panel.Handle(input.PanelTogglePlayer1Pro, nil)
			case "COL":
				dbg.vcs.Panel.Handle(input.PanelToggleColor, nil)
			}
		case "SET":
			arg, _ := tokens.Get()
			switch strings.ToUpper(arg) {
			case "P0PRO":
				dbg.vcs.Panel.Handle(input.PanelSetPlayer0Pro, true)
			case "P1PRO":
				dbg.vcs.Panel.Handle(input.PanelSetPlayer1Pro, true)
			case "P0AM":
				dbg.vcs.Panel.Handle(input.PanelSetPlayer0Pro, false)
			case "P1AM":
				dbg.vcs.Panel.Handle(input.PanelSetPlayer1Pro, false)
			case "COL":
				dbg.vcs.Panel.Handle(input.PanelSetColor, true)
			case "BW":
				dbg.vcs.Panel.Handle(input.PanelSetColor, false)
			}
		}
		dbg.printInstrument(dbg.vcs.Panel)

	case cmdStick:
		var err error

		stick, _ := tokens.Get()
		action, _ := tokens.Get()

		var event input.Event
		var value input.EventValue

		switch strings.ToUpper(action) {
		case "FIRE":
			event = input.Fire
			value = true
		case "UP":
			event = input.Up
			value = true
		case "DOWN":
			event = input.Down
			value = true
		case "LEFT":
			event = input.Left
			value = true
		case "RIGHT":
			event = input.Right
			value = true

		case "NOFIRE":
			event = input.Fire
			value = false
		case "NOUP":
			event = input.Up
			value = false
		case "NODOWN":
			event = input.Down
			value = false
		case "NOLEFT":
			event = input.Left
			value = false
		case "NORIGHT":
			event = input.Right
			value = false
		}

		n, _ := strconv.Atoi(stick)
		switch n {
		case 0:
			err = dbg.vcs.HandController0.Handle(event, value)
		case 1:
			err = dbg.vcs.HandController1.Handle(event, value)
		}

		if err != nil {
			return false, err
		}

	case cmdKeypad:
		var err error

		pad, _ := tokens.Get()
		key, _ := tokens.Get()

		n, _ := strconv.Atoi(pad)
		switch n {
		case 0:
			if strings.ToUpper(key) == "NONE" {
				err = dbg.vcs.HandController0.Handle(input.KeypadUp, nil)
			} else {
				err = dbg.vcs.HandController0.Handle(input.KeypadDown, rune(key[0]))
			}
		case 1:
			if strings.ToUpper(key) == "NONE" {
				err = dbg.vcs.HandController1.Handle(input.KeypadUp, nil)
			} else {
				err = dbg.vcs.HandController1.Handle(input.KeypadDown, rune(key[0]))
			}
		}

		if err != nil {
			return false, err
		}

	case cmdBreak:
		err := dbg.breakpoints.parseBreakpoint(tokens)
		if err != nil {
			return false, errors.New(errors.CommandError, err)
		}

	case cmdTrap:
		err := dbg.traps.parseTrap(tokens)
		if err != nil {
			return false, errors.New(errors.CommandError, err)
		}

	case cmdWatch:
		err := dbg.watches.parseWatch(tokens)
		if err != nil {
			return false, errors.New(errors.CommandError, err)
		}

	case cmdList:
		list, _ := tokens.Get()
		list = strings.ToUpper(list)
		switch list {
		case "BREAKS":
			dbg.breakpoints.list()
		case "TRAPS":
			dbg.traps.list()
		case "WATCHES":
			dbg.watches.list()
		case "ALL":
			dbg.breakpoints.list()
			dbg.traps.list()
			dbg.watches.list()
		default:
			// already caught by command line ValidateTokens()
		}

	case cmdDrop:
		drop, _ := tokens.Get()

		s, _ := tokens.Get()
		num, err := strconv.Atoi(s)
		if err != nil {
			return false, errors.New(errors.CommandError, fmt.Sprintf("drop attribute must be a number (%s)", s))
		}

		drop = strings.ToUpper(drop)
		switch drop {
		case "BREAK":
			err := dbg.breakpoints.drop(num)
			if err != nil {
				return false, err
			}
			dbg.printLine(terminal.StyleFeedback, "breakpoint #%d dropped", num)
		case "TRAP":
			err := dbg.traps.drop(num)
			if err != nil {
				return false, err
			}
			dbg.printLine(terminal.StyleFeedback, "trap #%d dropped", num)
		case "WATCH":
			err := dbg.watches.drop(num)
			if err != nil {
				return false, err
			}
			dbg.printLine(terminal.StyleFeedback, "watch #%d dropped", num)
		default:
			// already caught by command line ValidateTokens()
		}

	case cmdClear:
		clear, _ := tokens.Get()
		clear = strings.ToUpper(clear)
		switch clear {
		case "BREAKS":
			dbg.breakpoints.clear()
			dbg.printLine(terminal.StyleFeedback, "breakpoints cleared")
		case "TRAPS":
			dbg.traps.clear()
			dbg.printLine(terminal.StyleFeedback, "traps cleared")
		case "WATCHES":
			dbg.watches.clear()
			dbg.printLine(terminal.StyleFeedback, "watches cleared")
		case "ALL":
			dbg.breakpoints.clear()
			dbg.traps.clear()
			dbg.watches.clear()
			dbg.printLine(terminal.StyleFeedback, "breakpoints, traps and watches cleared")
		default:
			// already caught by command line ValidateTokens()
		}

	}

	return false, nil
}
