// Package selfupdate contains the release-selection, verification and
// replacement mechanics for the dropin-miner self-updater.
package selfupdate

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Version is the canonical, prefix-free X.Y.Z form used by release binaries.
// GitHub tags add a v through Tag; callers never compare raw version strings.
type Version struct {
	Major uint64
	Minor uint64
	Patch uint64
}

func (v Version) String() string {
	return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch)
}

func (v Version) Tag() string { return "v" + v.String() }

// Compare returns -1, 0 or 1 when v is less than, equal to or greater than o.
func (v Version) Compare(o Version) int {
	for _, pair := range [][2]uint64{{v.Major, o.Major}, {v.Minor, o.Minor}, {v.Patch, o.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	return 0
}

// ParseVersion accepts X.Y.Z and vX.Y.Z and returns the one canonical form.
// Pre-releases, build metadata, whitespace and leading zeroes are refused.
func ParseVersion(raw string) (Version, error) {
	if raw == "" {
		return Version{}, errors.New("empty version")
	}
	if strings.TrimSpace(raw) != raw {
		return Version{}, fmt.Errorf("version %q contains surrounding whitespace", raw)
	}
	bare := strings.TrimPrefix(raw, "v")
	fields := strings.Split(bare, ".")
	if len(fields) != 3 {
		return Version{}, fmt.Errorf("version %q is not X.Y.Z", raw)
	}
	var values [3]uint64
	for i, field := range fields {
		if field == "" {
			return Version{}, fmt.Errorf("version %q has an empty field", raw)
		}
		if len(field) > 1 && field[0] == '0' {
			return Version{}, fmt.Errorf("version %q has a leading zero", raw)
		}
		for _, r := range field {
			if r < '0' || r > '9' {
				return Version{}, fmt.Errorf("version %q is not a stable X.Y.Z release", raw)
			}
		}
		n, err := strconv.ParseUint(field, 10, 64)
		if err != nil {
			return Version{}, fmt.Errorf("version %q: %w", raw, err)
		}
		values[i] = n
	}
	return Version{Major: values[0], Minor: values[1], Patch: values[2]}, nil
}

// ParseReleaseTag accepts only the canonical GitHub form vX.Y.Z.
func ParseReleaseTag(raw string) (Version, error) {
	if !strings.HasPrefix(raw, "v") {
		return Version{}, fmt.Errorf("release tag %q is not vX.Y.Z", raw)
	}
	v, err := ParseVersion(raw)
	if err != nil {
		return Version{}, err
	}
	if raw != v.Tag() {
		return Version{}, fmt.Errorf("release tag %q is not canonical", raw)
	}
	return v, nil
}
