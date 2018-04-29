package mflib

import (
	"headlessVCS/hardware/cpu"
	"headlessVCS/hardware/cpu/register"
	"reflect"
	"testing"
)

// Assert can be used to test equality between one value and another. no return
// value but Go testing harness will raise an Error is assertion fails
func Assert(t *testing.T, r, x interface{}) {
	t.Helper()
	switch r := r.(type) {

	default:
		t.Errorf("assert failed (unknown type [%s])", reflect.TypeOf(r))

	case cpu.StatusRegister:
		if r.ToBits() != x.(string) {
			t.Errorf("assert StatusRegister failed (%s  - wanted %s)", r.ToBits(), x.(string))
		}

	case *register.Register:
		switch x := x.(type) {
		default:
			t.Errorf("assert failed (unknown type [%s])", reflect.TypeOf(x))

		case int:
			if r.ToUint16() != uint16(x) {
				t.Errorf("assert Register failed (%d  - wanted %d", r.ToUint16(), x)
			}
		case string:
			if r.ToBits() != x {
				t.Errorf("assert Register failed (%s  - wanted %s", r.ToBits(), x)
			}
		}

	case bool:
		if r != x.(bool) {
			t.Errorf("assert Bool failed (%v  - wanted %v", r, x.(bool))
		}

	case int:
		if r != x.(int) {
			t.Errorf("assert Int failed (%d  - wanted %d)", r, x.(int))
		}
	}

}
