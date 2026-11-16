// Package mq provides a security-hardened, generic MongoDB query, update, and sort builder.
package mq

import "errors"

const (
	// DefaultMaxDepth is the default maximum nesting depth for logical and $elemMatch filters.
	DefaultMaxDepth = 10

	// DefaultMaxInElements is the default maximum number of elements permitted in $in, $nin, and $all slices.
	DefaultMaxInElements = 1000

	// DefaultMaxPatternLength is the default maximum length in bytes for unescaped string match patterns.
	DefaultMaxPatternLength = 256

	// MaxFieldNameLength is the maximum permitted length in bytes for a MongoDB field path.
	MaxFieldNameLength = 255
)

var (
	// ErrEmptyFilter is returned when Build is called on a zero-value or empty Filter without MatchAll.
	ErrEmptyFilter = errors.New("mq: filter is empty; use MatchAll() to intentionally match all documents")

	// ErrInvalidFieldName is returned when a field path is empty, exceeds MaxFieldNameLength,
	// contains control characters, starts with '$', or contains invalid dot-path segments.
	ErrInvalidFieldName = errors.New("mq: invalid or unsafe field name")

	// ErrInvalidScalarValue is returned when an untrusted or non-scalar value (such as a map, slice, or arbitrary struct)
	// is supplied to a scalar operator.
	ErrInvalidScalarValue = errors.New("mq: value is not a valid scalar primitive")

	// ErrEmptySlice is returned when an empty slice is passed to In, Nin, All, or ScalarElemMatch.
	ErrEmptySlice = errors.New("mq: slice operand must not be empty")

	// ErrMaxElementsExceeded is returned when a slice operand exceeds Options.MaxInElements.
	ErrMaxElementsExceeded = errors.New("mq: slice operand exceeds maximum allowed elements")

	// ErrEmptyLogicalGroup is returned when And, Or, or Nor is constructed with zero child filters.
	ErrEmptyLogicalGroup = errors.New("mq: logical operator requires at least one child filter")

	// ErrMaxDepthExceeded is returned when filter nesting exceeds Options.MaxDepth.
	ErrMaxDepthExceeded = errors.New("mq: filter nesting depth exceeds maximum allowed limit")

	// ErrEmptyElemMatch is returned when ElemMatch is given an empty Filter, MatchAll(), or zero ScalarOps.
	ErrEmptyElemMatch = errors.New("mq: $elemMatch requires a non-empty predicate and cannot use MatchAll")

	// ErrNegativeArraySize is returned when Size is called with a negative length.
	ErrNegativeArraySize = errors.New("mq: array $size must be non-negative")

	// ErrInvalidPattern is returned when a string matching pattern (Prefix, Suffix, Contains, ExactCaseInsensitive) is empty
	// or contains null bytes.
	ErrInvalidPattern = errors.New("mq: string match pattern must not be empty or contain null bytes")

	// ErrPatternTooLong is returned when a string matching pattern exceeds Options.MaxPatternLength.
	ErrPatternTooLong = errors.New("mq: string match pattern exceeds maximum allowed length")

	// ErrEmptyUpdate is returned when Build is called on an Update builder with no operations.
	ErrEmptyUpdate = errors.New("mq: update document must contain at least one operation")

	// ErrDuplicateUpdateField is returned when the same field path is modified multiple times in a single Update builder.
	ErrDuplicateUpdateField = errors.New("mq: duplicate or conflicting field path in update document")

	// ErrEmptySort is returned when Build is called on a Sort builder with no sort fields.
	ErrEmptySort = errors.New("mq: sort document must contain at least one field")

	// ErrDuplicateSortField is returned when the same field path is added more than once to a Sort builder.
	ErrDuplicateSortField = errors.New("mq: duplicate field path in sort document")
)

// Options configures structural and resource bounds when compiling a Filter.
type Options struct {
	// MaxDepth is the maximum allowed nesting depth of logical combinators ($and, $or, $nor) and $elemMatch.
	// If <= 0, DefaultMaxDepth (10) is used.
	MaxDepth int

	// MaxInElements is the maximum allowed slice length for $in, $nin, and $all operators.
	// If <= 0, DefaultMaxInElements (1000) is used.
	MaxInElements int

	// MaxPatternLength is the maximum allowed byte length for Prefix, Suffix, Contains, and ExactCaseInsensitive patterns.
	// If <= 0, DefaultMaxPatternLength (256) is used.
	MaxPatternLength int
}

// DefaultOptions returns the default security limits for filter compilation.
func DefaultOptions() Options {
	return Options{
		MaxDepth:         DefaultMaxDepth,
		MaxInElements:    DefaultMaxInElements,
		MaxPatternLength: DefaultMaxPatternLength,
	}
}

func (o Options) normalized() Options {
	res := o
	if res.MaxDepth <= 0 {
		res.MaxDepth = DefaultMaxDepth
	}
	if res.MaxInElements <= 0 {
		res.MaxInElements = DefaultMaxInElements
	}
	if res.MaxPatternLength <= 0 {
		res.MaxPatternLength = DefaultMaxPatternLength
	}
	return res
}
