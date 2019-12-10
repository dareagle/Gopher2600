package paths

import (
	"os"
	"path"
)

// the base path for all resources. note that we don't use this value directly
// except in the getBasePath() function. that function should be used instead.
const baseResourcePath = ".gopher2600"

// ResourcePath returns the resource string (representing the resource to be
// loaded) prepended with operating system specific details.
func ResourcePath(resource ...string) string {
	var p []string

	p = make([]string, 0, len(resource)+1)
	p = append(p, getBasePath())
	p = append(p, resource...)

	return path.Join(p...)
}

// getBasePath() returns baseResourcePath with the user's home directory
// prepended if it the unadorned baseResourcePath cannot be found in the
// current directory.
//
// note that we're not checking for the existance of the resource requested by
// the caller, or even the existance of 'baseResourcePath' in the home
// directory.
//
// note: this is a UNIX thing. there's no real reason why any other OS should
// implement this.
func getBasePath() string {
	if _, err := os.Stat(baseResourcePath); err == nil {
		return baseResourcePath
	}

	home, err := os.UserConfigDir()
	if err != nil {
		return baseResourcePath
	}
	return path.Join(home, baseResourcePath[1:])
}