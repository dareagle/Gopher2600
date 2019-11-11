package screendigest

import (
	"crypto/sha1"
	"fmt"
	"gopher2600/errors"
	"gopher2600/television"
	"strings"
)

// SHA1 is an implementation of the television.Renderer interface with an
// embedded television for convenience. It generates a sha1 value of the
// image every frame. it does not display the image anywhere.
//
// Note that the use of sha1 is fine for this application because this is not a
// cryptographic task.
type SHA1 struct {
	television.Television
	digest    [sha1.Size]byte
	frameData []byte
	frameNum  int
}

// NewSHA1 initialises a new instance of DigestTV. For convenience, the
// television argument can be nil, in which case an instance of
// StellaTelevision will be created.
func NewSHA1(tvType string, tv television.Television) (*SHA1, error) {
	var err error

	// set up digest tv
	dig := new(SHA1)

	// create or attach television implementation
	if tv == nil {
		dig.Television, err = television.NewStellaTelevision(tvType)
		if err != nil {
			return nil, err
		}
	} else {
		// check that the quoted tvType matches the specification of the
		// supplied BasicTelevision instance. we don't really need this but
		// becuase we're implying that tvType is required, even when an
		// instance of BasicTelevision has been supplied, the caller may be
		// expecting an error
		tvType = strings.ToUpper(tvType)
		if tvType != "AUTO" && tvType != tv.GetSpec().ID {
			return nil, errors.New(errors.ScreenDigest, "trying to piggyback a tv of a different spec")
		}
		dig.Television = tv
	}

	// register ourselves as a television.Renderer
	dig.AddPixelRenderer(dig)

	// set attributes that depend on the television specification
	dig.Resize(-1, -1)

	return dig, nil
}

func (dig SHA1) String() string {
	return fmt.Sprintf("%x", dig.digest)
}

// ResetDigest resets the current digest value to 0
func (dig *SHA1) ResetDigest() {
	for i := range dig.digest {
		dig.digest[i] = 0
	}
}

// Resize implements television.Television interface
func (dig *SHA1) Resize(_, _ int) error {
	dig.frameData = make([]byte, len(dig.digest)+((television.ClocksPerScanline+1)*(dig.GetSpec().ScanlinesTotal+1)*3))
	return nil
}

// NewFrame implements television.Renderer interface
func (dig *SHA1) NewFrame(frameNum int) error {
	// chain fingerprints by copying the value of the last fingerprint
	// to the head of the screen data
	n := copy(dig.frameData, dig.digest[:])
	if n != len(dig.digest) {
		return errors.New(errors.ScreenDigest, fmt.Sprintf("unexpected amount of data copied"))
	}
	dig.digest = sha1.Sum(dig.frameData)
	dig.frameNum = frameNum
	return nil
}

// NewScanline implements television.Renderer interface
func (dig *SHA1) NewScanline(scanline int) error {
	return nil
}

// SetPixel implements television.Renderer interface
func (dig *SHA1) SetPixel(x, y int, red, green, blue byte, vblank bool) error {
	// preserve the first few bytes for a chained fingerprint
	offset := television.ClocksPerScanline * y * 3
	offset += x * 3

	if offset >= len(dig.frameData) {
		return errors.New(errors.ScreenDigest, fmt.Sprintf("the coordinates (%d, %d) passed to SetPixel will cause an invalid access of the frameData array", x, y))
	}

	dig.frameData[offset] = red
	dig.frameData[offset+1] = green
	dig.frameData[offset+2] = blue

	return nil
}

// SetAltPixel implements television.Renderer interface
func (dig *SHA1) SetAltPixel(x, y int, red, green, blue byte, vblank bool) error {
	return nil
}
