package commandline

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

const cycleDuration = 500 * time.Millisecond

// TabCompletion keeps track of the most recent tab completion attempt
type TabCompletion struct {
	commands *Commands

	matches []string
	match   int

	lastCompletion string
}

// NewTabCompletion initialises a new TabCompletion instance
//
// completion works best if commands has been sorted
func NewTabCompletion(commands *Commands) *TabCompletion {
	tc := new(TabCompletion)
	tc.commands = commands
	tc.Reset()
	return tc
}

// Complete transforms the input such that the last word in the input is
// expanded to meet the closest match in the list of allowed strings.
func (tc *TabCompletion) Complete(input string) string {
	// split input tokens -- it's easier to work with tokens
	tokens := TokeniseInput(input)

	// common function that polishes off a successful Complete()
	endGuess := func() string {
		if tc.match >= 0 {
			tokens.ReplaceEnd(tc.matches[tc.match])
			tc.lastCompletion = fmt.Sprintf("%s ", tokens.String())
		} else {
			// no matches found so completion string is by definition, the same
			// as the input
			tc.lastCompletion = input
		}
		return tc.lastCompletion
	}

	// if the input argument is the same as what we returned last time, then
	// cycle through the options that were compiled last time
	if tc.lastCompletion == input && tc.match >= 0 {
		tc.match++
		if tc.match >= len(tc.matches) {
			tc.match = 0
		}
		return endGuess()
	}

	// new tabcompletion session
	tc.Reset()

	// no need to to anything if input ends with a space
	if strings.HasSuffix(input, " ") {
		return input
	}

	// get first token
	tok, ok := tokens.Get()
	if !ok {
		return input
	}
	tok = strings.ToUpper(tok)

	// look for match
	for i := range *tc.commands {
		n := (*tc.commands)[i]

		// if there is an exact match then recurse into the node looking for
		// where the last token coincides with the node tree
		if tok == n.tag {
			// we may have encountered partial matches earlier in the loop. now
			// that we have found an exact match however, we need to make sure
			// the match list is empty so that we don't erroneously trigger the
			// match-cycling branch above.
			tc.Reset()

			// recurse
			tokens.Unget()
			tc.buildMatches(n, tokens)

			return endGuess()
		}

		// if there is a partial match, then add the current node to the list
		// of matches
		if tokens.IsEnd() && len(tok) < len(n.tag) && tok == n.tag[:len(tok)] {
			tc.matches = append(tc.matches, n.tag)
			tc.match = 0
		}
	}

	return endGuess()
}

// Reset is used to clear an outstanding completion session. note that this
// only really needs to be called if the input argument to Complete() is not
// different to the previous return value from that function, and you want to
// start a new completion session.
func (tc *TabCompletion) Reset() {
	tc.matches = make([]string, 0)
	tc.match = -1
}

func (tc *TabCompletion) buildMatches(n *node, tokens *Tokens) {
	// if there is no more input then return true (validation has passed) if
	// the node is optional, false if it is required
	tok, ok := tokens.Get()
	if !ok {
		return
	}

	match := true
	switch n.tag {
	case "%V":
		_, err := strconv.ParseInt(tok, 0, 32)
		match = err == nil

	case "%I":
		_, err := strconv.ParseFloat(tok, 32)
		match = err == nil

	case "%S":
		// against expectations, string placeholders do not cause a match. if
		// they did then they would be acting in the same way as the %*
		// placeholder and any subsequent branches will not be considered at
		// all.
		match = false

	case "%F":
		// TODO: filename completion

		// see commentary for %S above
		match = false

	case "%*":
		// this placeholder indicates that the rest of the tokens can be
		// ignored.
		return

	default:
		// case sensitive matching
		tok = strings.ToUpper(tok)
		match = tok == n.tag
	}

	// if token doesn't match this node, check branches. if there are no
	// branches, return false (validation has failed)
	if !match {
		// if there is a partial match, then add the current node to the list
		// of matches
		if tokens.IsEnd() && len(tok) < len(n.tag) && tok == n.tag[:len(tok)] {
			tc.matches = append(tc.matches, n.tag)
			tc.match = 0
		}

		if n.branch == nil {
			return
		}

		// take a note of current token position. if the token wanders past
		// this point as a result of a branch then we can see that the branch
		// was deeper then just one token. if this is the case then we can see
		// that the branch was *partially* accepted and that we should not
		// proceed onto next-nodes from here.
		tokenAt := tokens.curr

		for bi := range n.branch {
			// we want to use the current token again so we unget() the last token
			// so that it is available at the beginning of the recursed validate()
			// function
			tokens.Unget()

			tc.buildMatches(n.branch[bi], tokens)
		}

		// the key to this condition is the tokenAt variable. see note above.
		if n.group == groupOptional && len(tc.matches) == 0 && tokenAt == tokens.curr {
			tokens.Unget()
		} else {
			return
		}
	}

	// token does match and there are no more tokens to consume so we can add
	// this successful token to the list of matches
	//
	// note that this is specific to tab-completion, validation has no
	// equivalent. the purpose of this is to cause the Complete() function
	// above to replace the last token with a normalised version of that token
	// and to suffix it with a space.
	if tokens.IsEnd() {
		tc.matches = append(tc.matches, tok)
		tc.match = 0
		return
	}

	// token does match this node. check nodes that follow on.
	for nx := range n.next {
		tc.buildMatches(n.next[nx], tokens)
	}

	return
}
