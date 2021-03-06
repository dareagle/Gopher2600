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

package riot

import (
	"strings"

	"github.com/jetsetilly/gopher2600/hardware/memory/bus"
	"github.com/jetsetilly/gopher2600/hardware/riot/input"
	"github.com/jetsetilly/gopher2600/hardware/riot/timer"
)

// RIOT represents the PIA 6532 found in the VCS
type RIOT struct {
	mem bus.ChipBus

	Timer *timer.Timer
	Input *input.Input
}

// NewRIOT is the preferred method of initialisation for the RIOT type
func NewRIOT(mem bus.ChipBus, tiaMem bus.ChipBus) (*RIOT, error) {
	var err error

	riot := &RIOT{mem: mem}
	riot.Timer = timer.NewTimer(mem)
	riot.Input, err = input.NewInput(mem, tiaMem)
	if err != nil {
		return nil, err
	}

	return riot, nil
}

func (riot RIOT) String() string {
	s := strings.Builder{}
	s.WriteString(riot.Timer.String())
	return s.String()
}

// Update checks for the most recent write by the CPU to the RIOT memory
// registers
func (riot *RIOT) Update() {
	serviceMemory, data := riot.mem.ChipRead()
	if !serviceMemory {
		return
	}

	serviceMemory = riot.Timer.Update(data)
	if !serviceMemory {
		return
	}

	_ = riot.Input.Update(data)
}

// Step moves the state of the RIOT forward one video cycle
func (riot *RIOT) Step() {
	riot.Update()
	riot.Timer.Step()
	riot.Input.Step()
}
