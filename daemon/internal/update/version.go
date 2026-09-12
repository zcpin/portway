// Package update implements release downloads and transactional portable updates.
package update

import (
	"fmt"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?$`)

type semVersion struct {
	core []string
	pre  []string
}

func parseVersion(s string) (semVersion, error) {
	m := versionPattern.FindStringSubmatch(s)
	if m == nil || len(s) > 200 {
		return semVersion{}, fmt.Errorf("invalid semantic version: %q", s)
	}
	v := semVersion{core: m[1:4]}
	if m[4] != "" {
		v.pre = strings.Split(m[4], ".")
		for _, part := range v.pre {
			if numeric(part) && len(part) > 1 && part[0] == '0' {
				return semVersion{}, fmt.Errorf("leading zero in prerelease: %q", s)
			}
		}
	}
	return v, nil
}

func numeric(s string) bool {
	for _, ch := range s {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return s != ""
}

func compareNumber(a, b string) int {
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return strings.Compare(a, b)
}

func (v semVersion) compare(other semVersion) int {
	for i := range v.core {
		if c := compareNumber(v.core[i], other.core[i]); c != 0 {
			return c
		}
	}
	if len(v.pre) == 0 && len(other.pre) != 0 {
		return 1
	}
	if len(v.pre) != 0 && len(other.pre) == 0 {
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(other.pre); i++ {
		a, b := v.pre[i], other.pre[i]
		c := strings.Compare(a, b)
		switch {
		case numeric(a) && numeric(b):
			c = compareNumber(a, b)
		case numeric(a):
			c = -1
		case numeric(b):
			c = 1
		}
		if c != 0 {
			return c
		}
	}
	if len(v.pre) < len(other.pre) {
		return -1
	}
	if len(v.pre) > len(other.pre) {
		return 1
	}
	return 0
}
