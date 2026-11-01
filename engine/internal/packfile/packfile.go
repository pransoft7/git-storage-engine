// Package packfile finds and inspects the pack that receive-pack leaves in the
// quarantine directory.
package packfile

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Info describes a pack found in quarantine.
type Info struct {
	Path    string // absolute path of the .pack file
	SHA     string // checksum from the file name pack-<SHA>.pack
	Objects uint32 // object count from the pack header (0 = empty push)
}

// Find looks for the pack inside <quarantine>/pack. It returns nil when
// there is no pack to save i.e, quarantine path unset (e.g. a delete-only push) or
// no pack file present. More than one pack is an error.
func Find(quarantine string) (*Info, error) {
	if quarantine == "" {
		return nil, nil
	}
	matches, err := filepath.Glob(filepath.Join(quarantine, "pack", "pack-*.pack"))
	if err != nil {
		return nil, err
	}
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
	default:
		return nil, fmt.Errorf("packfile: expected one pack in quarantine, found %d", len(matches))
	}
	n, err := ReadHeader(matches[0])
	if err != nil {
		return nil, err
	}
	name := filepath.Base(matches[0])
	sha := strings.TrimSuffix(strings.TrimPrefix(name, "pack-"), ".pack")
	return &Info{Path: matches[0], SHA: sha, Objects: n}, nil
}

// ReadHeader validates the 12-byte pack header ("PACK", version, object count)
// and returns the object count.
func ReadHeader(path string) (uint32, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	var hdr [12]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return 0, fmt.Errorf("packfile: reading header of %s: %w", path, err)
	}
	if string(hdr[:4]) != "PACK" {
		return 0, fmt.Errorf("packfile: %s is not a pack (bad magic)", path)
	}
	if v := binary.BigEndian.Uint32(hdr[4:8]); v != 2 && v != 3 {
		return 0, fmt.Errorf("packfile: %s has unsupported version %d", path, v)
	}
	return binary.BigEndian.Uint32(hdr[8:12]), nil
}
