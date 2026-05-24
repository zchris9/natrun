// Package promptio parses the model's reply, which is expected to be a single
// fenced block of the form:
//
//	```natrun
//	<html-or-fragment>
//	  ...the page HTML...
//	</html-or-fragment>
//	```
//
// We tolerate the model occasionally wrapping the block in prose or omitting
// the fence — anything we can recover, we recover; anything we can't is
// reported as an error so the handler can render a fallback page.
//
// In the formal model, the raw reply text is c_n's newest segment, and Parse
// is the projection P: text -> renderable o_n. No state is extracted; the
// model's reply is itself part of the carried context and rides forward
// verbatim into the next turn's input.
package promptio

import (
	"errors"
	"regexp"
	"strings"
)

// Result is what we extract from one model reply.
type Result struct {
	HTML string
}

var (
	fenceRE = regexp.MustCompile("(?s)```(?:natrun)?\\s*\\n(.*?)\\n```")
	htmlRE  = regexp.MustCompile(`(?s)<html-or-fragment>(.*?)</html-or-fragment>`)
)

// Parse extracts the HTML fragment from a model reply.
// Returns an error if no HTML fragment can be found at all.
func Parse(reply string) (Result, error) {
	body := extractFenced(reply)

	htmlMatch := htmlRE.FindStringSubmatch(body)
	if htmlMatch == nil {
		// Fall back: maybe the model skipped the tag and the body IS the HTML.
		// Only accept if it looks like HTML.
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "<") {
			return Result{HTML: trimmed}, nil
		}
		return Result{}, errors.New("promptio: no <html-or-fragment> in model reply")
	}

	return Result{HTML: strings.TrimSpace(htmlMatch[1])}, nil
}

// extractFenced returns the inside of the first ```...``` block, or the whole
// string if no fence is present.
func extractFenced(s string) string {
	if m := fenceRE.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	return s
}
