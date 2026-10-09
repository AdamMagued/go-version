// Copyright IBM Corp. 2014, 2025
// SPDX-License-Identifier: MPL-2.0

package version

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

var (
	constraintRegexp             *regexp.Regexp
	constraintRegexpOnce         sync.Once
	constraintWildcardRegexp     *regexp.Regexp
	constraintWildcardRegexpOnce sync.Once
)

func getConstraintRegexp() *regexp.Regexp {
	constraintRegexpOnce.Do(func() {
		// This heavy lifting only happens the first time this function is called
		constraintRegexp = regexp.MustCompile(fmt.Sprintf(
			`^\s*(%s)\s*(%s)\s*$`,
			`<=|>=|!=|~>|<|>|=|`,
			VersionRegexpRaw,
		))
	})
	return constraintRegexp
}

func getConstraintWildcardRegexp() *regexp.Regexp {
	constraintWildcardRegexpOnce.Do(func() {
		constraintWildcardRegexp = regexp.MustCompile(fmt.Sprintf(
			`^\s*(%s)\s*v?((?:[0-9]+(?:\.[0-9]+)*\.(?:[xX*](?:\.[xX*])*))|[xX*](?:\.[xX*])*)\s*$`,
			`<=|>=|!=|~>|<|>|=|`,
		))
	})
	return constraintWildcardRegexp
}

// Constraint represents a single constraint for a version, such as
// ">= 1.0".
type Constraint struct {
	f           constraintFunc
	op          operator
	check       *Version
	original    string
	hasWildcard bool
	wildcardLen int
}

func (c *Constraint) Equals(con *Constraint) bool {
	return c.op == con.op &&
		c.hasWildcard == con.hasWildcard &&
		c.wildcardLen == con.wildcardLen &&
		c.check.Equal(con.check)
}

// Constraints is a slice of constraints. We make a custom type so that
// we can add methods to it.
type Constraints []*Constraint

type constraintFunc func(v, c *Version) bool

type constraintOperation struct {
	op operator
	f  constraintFunc
}

// NewConstraint will parse one or more constraints from the given
// constraint string. The string must be a comma-separated list of
// constraints.
func NewConstraint(v string) (Constraints, error) {
	vs := strings.Split(v, ",")
	result := make([]*Constraint, len(vs))
	for i, single := range vs {
		c, err := parseSingle(single)
		if err != nil {
			return nil, err
		}

		result[i] = c
	}

	return Constraints(result), nil
}

// MustConstraints is a helper that wraps a call to a function
// returning (Constraints, error) and panics if error is non-nil.
func MustConstraints(c Constraints, err error) Constraints {
	if err != nil {
		panic(err)
	}

	return c
}

// Check tests if a version satisfies all the constraints.
func (cs Constraints) Check(v *Version) bool {
	for _, c := range cs {
		if !c.Check(v) {
			return false
		}
	}

	return true
}

// Equals compares Constraints with other Constraints
// for equality. This may not represent logical equivalence
// of compared constraints.
// e.g. even though '>0.1,>0.2' is logically equivalent
// to '>0.2' it is *NOT* treated as equal.
//
// Missing operator is treated as equal to '=', whitespaces
// are ignored and constraints are sorted before comparison.
func (cs Constraints) Equals(c Constraints) bool {
	if len(cs) != len(c) {
		return false
	}

	// make copies to retain order of the original slices
	left := make(Constraints, len(cs))
	copy(left, cs)
	sort.Stable(left)
	right := make(Constraints, len(c))
	copy(right, c)
	sort.Stable(right)

	// compare sorted slices
	for i, con := range left {
		if !con.Equals(right[i]) {
			return false
		}
	}

	return true
}

func (cs Constraints) Len() int {
	return len(cs)
}

func (cs Constraints) Less(i, j int) bool {
	if cs[i].op < cs[j].op {
		return true
	}
	if cs[i].op > cs[j].op {
		return false
	}

	if !cs[i].check.Equal(cs[j].check) {
		return cs[i].check.LessThan(cs[j].check)
	}

	if cs[i].hasWildcard != cs[j].hasWildcard {
		return !cs[i].hasWildcard && cs[j].hasWildcard
	}

	return cs[i].wildcardLen < cs[j].wildcardLen
}

func (cs Constraints) Swap(i, j int) {
	cs[i], cs[j] = cs[j], cs[i]
}

// Returns the string format of the constraints
func (cs Constraints) String() string {
	csStr := make([]string, len(cs))
	for i, c := range cs {
		csStr[i] = c.String()
	}

	return strings.Join(csStr, ",")
}

// Check tests if a constraint is validated by the given version.
func (c *Constraint) Check(v *Version) bool {
	return c.f(v, c.check)
}

// Prerelease returns true if the version underlying this constraint
// contains a prerelease field.
func (c *Constraint) Prerelease() bool {
	return len(c.check.Prerelease()) > 0
}

func (c *Constraint) String() string {
	return c.original
}

func parseSingle(v string) (*Constraint, error) {
	if matches := getConstraintWildcardRegexp().FindStringSubmatch(v); matches != nil {
		return parseWildcardConstraint(v, matches)
	}

	matches := getConstraintRegexp().FindStringSubmatch(v)
	if matches == nil {
		return nil, fmt.Errorf("malformed constraint: %s", v)
	}

	check, err := NewVersion(matches[2])
	if err != nil {
		return nil, err
	}

	var cop constraintOperation
	switch matches[1] {
	case "=":
		cop = constraintOperation{op: equal, f: constraintEqual}
	case "!=":
		cop = constraintOperation{op: notEqual, f: constraintNotEqual}
	case ">":
		cop = constraintOperation{op: greaterThan, f: constraintGreaterThan}
	case "<":
		cop = constraintOperation{op: lessThan, f: constraintLessThan}
	case ">=":
		cop = constraintOperation{op: greaterThanEqual, f: constraintGreaterThanEqual}
	case "<=":
		cop = constraintOperation{op: lessThanEqual, f: constraintLessThanEqual}
	case "~>":
		cop = constraintOperation{op: pessimistic, f: constraintPessimistic}
	default:
		cop = constraintOperation{op: equal, f: constraintEqual}
	}

	return &Constraint{
		f:        cop.f,
		op:       cop.op,
		check:    check,
		original: v,
	}, nil
}

func parseWildcardConstraint(v string, matches []string) (*Constraint, error) {
	parts := strings.Split(matches[2], ".")
	var prefix []int64
	for _, part := range parts {
		if part == "*" || part == "x" || part == "X" {
			break
		}
		val, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("error parsing version: %s", err)
		}
		prefix = append(prefix, val)
	}

	minSegs := make([]int64, len(prefix))
	copy(minSegs, prefix)
	for len(minSegs) < 3 {
		minSegs = append(minSegs, 0)
	}
	minSegStrs := make([]string, len(minSegs))
	for i, s := range minSegs {
		minSegStrs[i] = strconv.FormatInt(s, 10)
	}
	minVersion, err := NewVersion(strings.Join(minSegStrs, "."))
	if err != nil {
		return nil, err
	}

	var nextVersion *Version
	if len(prefix) > 0 {
		nextSegs := make([]int64, len(prefix))
		copy(nextSegs, prefix)
		if nextSegs[len(nextSegs)-1] < math.MaxInt64 {
			nextSegs[len(nextSegs)-1]++
		}
		for len(nextSegs) < 3 {
			nextSegs = append(nextSegs, 0)
		}
		nextSegStrs := make([]string, len(nextSegs))
		for i, s := range nextSegs {
			nextSegStrs[i] = strconv.FormatInt(s, 10)
		}
		nextVersion, err = NewVersion(strings.Join(nextSegStrs, "."))
		if err != nil {
			return nil, err
		}
	}

	var cop constraintOperation
	switch matches[1] {
	case "=":
		cop = constraintOperation{
			op: equal,
			f: func(v, c *Version) bool {
				return prereleaseCheck(v, minVersion) && matchesPrefix(v, prefix)
			},
		}
	case "!=":
		cop = constraintOperation{
			op: notEqual,
			f: func(v, c *Version) bool {
				return !matchesPrefix(v, prefix)
			},
		}
	case ">":
		cop = constraintOperation{
			op: greaterThan,
			f: func(v, c *Version) bool {
				if len(prefix) == 0 {
					return false
				}
				return prereleaseCheck(v, nextVersion) && v.Compare(nextVersion) >= 0
			},
		}
	case "<":
		cop = constraintOperation{
			op: lessThan,
			f: func(v, c *Version) bool {
				if len(prefix) == 0 {
					return false
				}
				return prereleaseCheck(v, minVersion) && v.Compare(minVersion) < 0
			},
		}
	case ">=":
		cop = constraintOperation{
			op: greaterThanEqual,
			f: func(v, c *Version) bool {
				return prereleaseCheck(v, minVersion) && v.Compare(minVersion) >= 0
			},
		}
	case "<=":
		cop = constraintOperation{
			op: lessThanEqual,
			f: func(v, c *Version) bool {
				if len(prefix) == 0 {
					return prereleaseCheck(v, minVersion)
				}
				return prereleaseCheck(v, nextVersion) && v.Compare(nextVersion) < 0
			},
		}
	case "~>":
		cop = constraintOperation{
			op: pessimistic,
			f: func(v, c *Version) bool {
				return prereleaseCheck(v, minVersion) && matchesPrefix(v, prefix)
			},
		}
	default:
		cop = constraintOperation{
			op: equal,
			f: func(v, c *Version) bool {
				return prereleaseCheck(v, minVersion) && matchesPrefix(v, prefix)
			},
		}
	}

	return &Constraint{
		f:           cop.f,
		op:          cop.op,
		check:       minVersion,
		original:    v,
		hasWildcard: true,
		wildcardLen: len(prefix),
	}, nil
}

func matchesPrefix(v *Version, prefix []int64) bool {
	if len(prefix) == 0 {
		return true
	}
	vSegs := v.Segments64()
	for i, p := range prefix {
		var vSeg int64
		if i < len(vSegs) {
			vSeg = vSegs[i]
		}
		if vSeg != p {
			return false
		}
	}
	return true
}

func prereleaseCheck(v, c *Version) bool {
	switch vPre, cPre := v.Prerelease() != "", c.Prerelease() != ""; {
	case cPre && vPre:
		// A constraint with a pre-release can only match a pre-release version
		// with the same base segments.
		return v.equalSegments(c)

	case !cPre && vPre:
		// A constraint without a pre-release can only match a version without a
		// pre-release.
		return false

	case cPre && !vPre:
		// OK, except with the pessimistic operator
	case !cPre && !vPre:
		// OK
	}
	return true
}

//-------------------------------------------------------------------
// Constraint functions
//-------------------------------------------------------------------

type operator rune

const (
	equal            operator = '='
	notEqual         operator = '≠'
	greaterThan      operator = '>'
	lessThan         operator = '<'
	greaterThanEqual operator = '≥'
	lessThanEqual    operator = '≤'
	pessimistic      operator = '~'
)

func constraintEqual(v, c *Version) bool {
	return v.Equal(c)
}

func constraintNotEqual(v, c *Version) bool {
	return !v.Equal(c)
}

func constraintGreaterThan(v, c *Version) bool {
	return prereleaseCheck(v, c) && v.Compare(c) == 1
}

func constraintLessThan(v, c *Version) bool {
	return prereleaseCheck(v, c) && v.Compare(c) == -1
}

func constraintGreaterThanEqual(v, c *Version) bool {
	return prereleaseCheck(v, c) && v.Compare(c) >= 0
}

func constraintLessThanEqual(v, c *Version) bool {
	return prereleaseCheck(v, c) && v.Compare(c) <= 0
}

func constraintPessimistic(v, c *Version) bool {
	// Using a pessimistic constraint with a pre-release, restricts versions to pre-releases
	if !prereleaseCheck(v, c) || (c.Prerelease() != "" && v.Prerelease() == "") {
		return false
	}

	// If the version being checked is naturally less than the constraint, then there
	// is no way for the version to be valid against the constraint
	if v.LessThan(c) {
		return false
	}
	// We'll use this more than once, so grab the length now so it's a little cleaner
	// to write the later checks
	cs := len(c.segments)

	// If the version being checked has less specificity than the constraint, then there
	// is no way for the version to be valid against the constraint
	if cs > len(v.segments) {
		return false
	}

	// Check the segments in the constraint against those in the version. If the version
	// being checked, at any point, does not have the same values in each index of the
	// constraints segments, then it cannot be valid against the constraint.
	for i := 0; i < c.si-1; i++ {
		if v.segments[i] != c.segments[i] {
			return false
		}
	}

	// Check the last part of the segment in the constraint. If the version segment at
	// this index is less than the constraints segment at this index, then it cannot
	// be valid against the constraint
	if c.segments[cs-1] > v.segments[cs-1] {
		return false
	}

	// If nothing has rejected the version by now, it's valid
	return true
}
