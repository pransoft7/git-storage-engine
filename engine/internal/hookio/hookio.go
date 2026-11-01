// Package hookio parses what Git feeds to hooks on stdin.
package hookio

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"toyengine/internal/model"
)

// ParseUpdates reads lines of the form "<old> <new> <ref>" (the format shared by
// pre-receive and reference-transaction) and returns them in order. Values are
// kept verbatim: reference-transaction may emit "ref:<target>" for symbolic refs
// and the HEAD line appears on the first push to an unborn branch, so callers
// decide what to ignore.
func ParseUpdates(r io.Reader) ([]model.RefUpdate, error) {
	var out []model.RefUpdate
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		f := strings.Fields(line)
		if len(f) != 3 {
			return nil, fmt.Errorf("hookio: malformed update line %q", line)
		}
		out = append(out, model.RefUpdate{Old: f[0], New: f[1], Ref: f[2]})
	}
	return out, sc.Err()
}
