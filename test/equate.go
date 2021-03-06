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

package test

import (
	"reflect"
	"testing"
)

// Equate is used to test equality between one value and another. Generally,
// both values must be of the same type but if a is of type uint16, b can be
// uint16 or int. The reason for this is that a literal number value is of type
// int. It is very convenient to write something like this, without having to
// cast the expected number value:
//
//	var r uint16
//	r = someFunction()
//	test.Equate(t, r, 10)
//
// This is by no means a comprehensive comparison function. With a bit more
// work with the reflect package we could generalise the testing a lot more. At
// is is however, it's good enough.
func Equate(t *testing.T, value, expectedValue interface{}) {
	t.Helper()

	switch value := value.(type) {

	default:
		t.Fatalf("unhandled type for Equate() function (%T))", value)

	case nil:
		if value != nil {
			t.Errorf("equation of type %T failed (%d  - wanted nil)", value, value)
		}

	case int:
		if reflect.TypeOf(value) != reflect.TypeOf(expectedValue) {
			t.Fatalf("values for Equate() are not the same type (%T and %T)", value, expectedValue)
		}

		if value != expectedValue.(int) {
			t.Errorf("equation of type %T failed (%d  - wanted %d)", value, value, expectedValue.(int))
		}

	case uint16:
		switch expectedValue := expectedValue.(type) {
		case int:
			if value != uint16(expectedValue) {
				t.Errorf("equation of type %T failed (%d  - wanted %d)", value, value, expectedValue)
			}
		case uint16:
			if value != expectedValue {
				t.Errorf("equation of type %T failed (%d  - wanted %d)", value, value, expectedValue)
			}
		default:
			t.Fatalf("values for Equate() are not the same compatible (%T and %T)", value, expectedValue)
		}

	case string:
		if reflect.TypeOf(value) != reflect.TypeOf(expectedValue) {
			t.Fatalf("values for Equate() are not the same type (%T and %T)", value, expectedValue)
		}

		if value != expectedValue.(string) {
			t.Errorf("equation of type %T failed (%s  - wanted %s)", value, value, expectedValue.(string))
		}

	case bool:
		if reflect.TypeOf(value) != reflect.TypeOf(expectedValue) {
			t.Fatalf("values for Equate() are not the same type (%T and %T)", value, expectedValue)
		}

		if value != expectedValue.(bool) {
			t.Errorf("equation of type %T failed (%v  - wanted %v", value, value, expectedValue.(bool))
		}
	}
}
