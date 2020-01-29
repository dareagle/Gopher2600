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

package input

import (
	"gopher2600/errors"
	"gopher2600/hardware/memory/addresses"
)

// HandController represents the "joystick" port on the VCS. The different
// devices (joysticks, paddles, etc.) send events to the Handle() function.
//
// Note that handcontrollers need access to TIA memory as well as RIOT memory.
type HandController struct {
	port
	mem     *inputMemory
	control *ControlBits

	// controller types
	stick    stick
	paddle   paddle
	keyboard keyboard

	// data direction register
	ddr uint8
}

// the stick type implements the digital "joystick" controller
type stick struct {
	// address in RIOT memory for joystick direction input
	addr uint16

	// the address in TIA memory for joystick fire button
	buttonAddr uint16

	// values indicating joystick state
	axis   uint8
	button bool

	// hand controllers 0 and 1 write the axis value to different nibbles of the
	// addr. transform allows us to transform that value with the help of
	// stickMask
	transform func(uint8) uint8

	// because the two hand controllers share the same stick address, each
	// controller needs to mask off the other hand controller's bits, or put
	// another way, the bits we need to preserve during the write
	addrMask uint8
}

// the paddle type implements the "paddle" hand controller
type paddle struct {
	addr       uint16
	buttonAddr uint16
	buttonMask uint8

	charge     uint8
	resistance float32
	ticks      float32
}

// the keyboard type implements the "keyboard" or "keypad" controller
type keyboard struct {
	addr uint16
	key  rune
}

// NewHandController0 is the preferred method of creating a new instance of
// HandController for representing hand controller zero
func NewHandController0(mem *inputMemory, control *ControlBits) *HandController {
	hc := &HandController{
		mem:     mem,
		control: control,
		stick: stick{
			addr:       addresses.SWCHA,
			buttonAddr: addresses.INPT4,
			axis:       0xf0,
			transform:  func(n uint8) uint8 { return n },
			addrMask:   0x0f,
		},
		paddle: paddle{
			addr:       addresses.INPT0,
			buttonAddr: addresses.SWCHA,
			buttonMask: 0x7f,
			resistance: 0.0,
		},
		keyboard: keyboard{
			addr: addresses.INPT0,
		},
		ddr: 0x00,
	}

	hc.port = port{
		id:     HandControllerZeroID,
		handle: hc.Handle,
	}

	return hc
}

// NewHandController1 is the preferred method of creating a new instance of
// HandController for representing hand controller one
func NewHandController1(mem *inputMemory, control *ControlBits) *HandController {
	hc := &HandController{
		mem:     mem,
		control: control,
		stick: stick{
			addr:       addresses.SWCHA,
			buttonAddr: addresses.INPT5,
			axis:       0xf0,
			transform:  func(n uint8) uint8 { return n >> 4 },
			addrMask:   0xf0,
		},
		paddle: paddle{
			addr:       addresses.INPT1,
			buttonAddr: addresses.SWCHA,
			buttonMask: 0xbf,
			resistance: 0.0,
		},
		keyboard: keyboard{
			addr: addresses.INPT1,
		},
		ddr: 0x00,
	}

	hc.port = port{
		id:     HandControllerOneID,
		handle: hc.Handle,
	}

	return hc
}

// String implements the Port interface
func (hc *HandController) String() string {
	return "nothing yet"
}

// Handle implements Port interface
func (hc *HandController) Handle(event Event, value EventValue) error {
	switch event {

	// do nothing at all if event is a NoEvent
	case NoEvent:
		return nil

	case Left:
		b, ok := value.(bool)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "bool")
		}
		if b {
			hc.stick.axis ^= 0x40
		} else {
			hc.stick.axis |= 0x40
		}
		hc.mem.riot.InputDeviceWrite(hc.stick.addr, hc.stick.transform(hc.stick.axis), hc.stick.addrMask)

	case Right:
		b, ok := value.(bool)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "bool")
		}
		if b {
			hc.stick.axis ^= 0x80
		} else {
			hc.stick.axis |= 0x80
		}
		hc.mem.riot.InputDeviceWrite(hc.stick.addr, hc.stick.transform(hc.stick.axis), hc.stick.addrMask)

	case Up:
		b, ok := value.(bool)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "bool")
		}
		if b {
			hc.stick.axis ^= 0x10
		} else {
			hc.stick.axis |= 0x10
		}
		hc.mem.riot.InputDeviceWrite(hc.stick.addr, hc.stick.transform(hc.stick.axis), hc.stick.addrMask)

	case Down:
		b, ok := value.(bool)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "bool")
		}
		if b {
			hc.stick.axis ^= 0x20
		} else {
			hc.stick.axis |= 0x20
		}
		hc.mem.riot.InputDeviceWrite(hc.stick.addr, hc.stick.transform(hc.stick.axis), hc.stick.addrMask)

	case Fire:
		b, ok := value.(bool)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "bool")
		}

		// record state of fire button regardless of latch bit. we need to know
		// the physical state for when the latch bit is unset
		hc.stick.button = b

		if hc.stick.button {
			hc.mem.tia.InputDeviceWrite(hc.stick.buttonAddr, 0x00, 0x00)
		} else if !hc.control.latchFireButton {
			// only release button (in memory) if latch bit is not set
			hc.mem.tia.InputDeviceWrite(hc.stick.buttonAddr, 0x80, 0x00)
		}

	case PaddleFire:
		b, ok := value.(bool)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "bool")
		}

		var v uint8

		if b {
			v = 0x00
		} else {
			v = 0xff
		}
		hc.mem.riot.InputDeviceWrite(hc.paddle.buttonAddr, v, hc.paddle.buttonMask)

	case PaddleSet:
		f, ok := value.(float32)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "float32")
		}

		hc.paddle.resistance = 1.0 - f

	case KeyboardDown:
		v, ok := value.(rune)
		if !ok {
			return errors.New(errors.BadInputEventType, event, "rune")
		}

		if v != '1' && v != '2' && v != '3' && v != '4' && v != '5' && v != '6' && v != '7' && v != '8' && v != '9' && v != '*' && v != '#' {
			return errors.New(errors.BadInputEventType, event, "numeric rune or '*' or '#'")
		}

		hc.keyboard.key = v

	case KeyboardUp:
		if value != nil {
			return errors.New(errors.BadInputEventType, event, "nil")
		}

		hc.keyboard.key = ' '

	case Unplug:
		return errors.New(errors.InputDeviceUnplugged, hc.id)

	// return now if there is no event to process
	default:
		return errors.New(errors.UnknownInputEvent, hc.id, event)
	}

	// record event with the EventRecorder
	if hc.recorder != nil {
		return hc.recorder.RecordEvent(hc.id, event, value)
	}

	return nil
}

func (hc *HandController) step() {
	hc.recharge()
}

// VBLANK bit 6 has been set. joystick button will latch (will not cause a
// Fire=false signal when fire button is released)
func (hc *HandController) unlatch() {
	// only unlatch if button is not pressed
	if !hc.stick.button {
		hc.mem.tia.InputDeviceWrite(hc.stick.buttonAddr, 0x80, 0x00)
	}
}

// VBLANK bit 7 has been set. input capacitor is grounded.
func (hc *HandController) ground() {
	hc.paddle.charge = 0
	hc.mem.riot.InputDeviceWrite(hc.paddle.addr, hc.paddle.charge, 0x00)
}

// the rate at which the controller capacitor fills. if the paddle resistor can
// take a value between 0.0 and 1.0 then the maximum number of ticks required
// to increase the capacitor charge by 1 is 100. The maximum charge is 255 so
// it takes a maximum of 25500 ticks to fill the capacitor.
//
// no idea if this value is correct but it feels good during play so I'm going
// to go with it for now.
//
// !!TODO: accurate paddle timings and sensitivity
const paddleSensitivity = 0.01

// recharge is called every video step via Input.Step()
func (hc *HandController) recharge() {
	// from Stella Programmer's Guide:
	//
	// "B. Dumped Input Ports (I0 through I3)
	//
	// These 4 input ports are normally used to read paddle position from an
	// external potentiometer-capacitor circuit. In order to discharge these
	// capacitors each of these input ports has a large transistor, which may be
	// turned on (grounding the input ports) by writing into bit 7 of the register
	// VBLANK. When this control bit is cleared the potentiometers begin to
	// recharge the capacitors and the microprocessor measures the time required
	// to detect a logic 1 at each input port."
	if hc.paddle.charge < 255 {
		hc.paddle.ticks += paddleSensitivity
		if hc.paddle.ticks >= hc.paddle.resistance {
			hc.paddle.ticks = 0
			hc.paddle.charge++
			hc.mem.tia.InputDeviceWrite(hc.paddle.addr, hc.paddle.charge, 0x00)
		}
	}
}
