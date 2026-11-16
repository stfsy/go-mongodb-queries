package mq

import (
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type filterKind uint8

const (
	kindEmpty filterKind = iota
	kindMatchAll
	kindScalarOp
	kindSliceOp
	kindExists
	kindExistsNotNull
	kindElemMatchFilter
	kindElemMatchScalar
	kindSize
	kindExprArraySizeField
	kindExprArraySizeConst
	kindExprFieldCompare
	kindRegex
	kindLogical
	kindNot
)

type regexFlag uint8

const (
	regexFlagAnchorStart regexFlag = 1 << iota
	regexFlagAnchorEnd
	regexFlagCaseInsensitive
)

// ScalarOp represents an operator-wrapped scalar predicate (e.g., {"$gte": 80})
// used inside ScalarElemMatch and Not.
type ScalarOp[T Scalar] struct {
	op  string
	val T
}

// OpEq creates a {"$eq": val} scalar operator predicate.
func OpEq[T Scalar](val T) ScalarOp[T] {
	return ScalarOp[T]{op: "$eq", val: val}
}

// OpNe creates a {"$ne": val} scalar operator predicate.
func OpNe[T Scalar](val T) ScalarOp[T] {
	return ScalarOp[T]{op: "$ne", val: val}
}

// OpGt creates a {"$gt": val} scalar operator predicate.
func OpGt[T Scalar](val T) ScalarOp[T] {
	return ScalarOp[T]{op: "$gt", val: val}
}

// OpGte creates a {"$gte": val} scalar operator predicate.
func OpGte[T Scalar](val T) ScalarOp[T] {
	return ScalarOp[T]{op: "$gte", val: val}
}

// OpLt creates a {"$lt": val} scalar operator predicate.
func OpLt[T Scalar](val T) ScalarOp[T] {
	return ScalarOp[T]{op: "$lt", val: val}
}

// OpLte creates a {"$lte": val} scalar operator predicate.
func OpLte[T Scalar](val T) ScalarOp[T] {
	return ScalarOp[T]{op: "$lte", val: val}
}

func isValidScalarOp(op string) bool {
	switch op {
	case "$eq", "$ne", "$gt", "$gte", "$lt", "$lte":
		return true
	default:
		return false
	}
}

// Filter is an immutable, security-hardened MongoDB query AST node.
// A zero-value Filter is invalid and returns ErrEmptyFilter on Build().
// To intentionally match all documents in a collection, use MatchAll().
type Filter struct {
	field      string
	str        string
	op         string
	val        any
	err        error
	children   []Filter
	num        int
	kind       filterKind
	regexFlags regexFlag
}

// Build validates the entire Filter AST using DefaultOptions and compiles it into a bson.D document.
func (f Filter) Build() (bson.D, error) {
	return f.BuildWithOptions(DefaultOptions())
}

// BuildWithOptions validates the entire Filter AST using the provided Options and compiles it into a bson.D document.
func (f Filter) BuildWithOptions(opts Options) (bson.D, error) {
	cfg := opts.normalized()
	doc, err := f.buildAtDepth(0, cfg)
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// MatchStage compiles f using DefaultOptions and wraps it in a MongoDB $match aggregation stage:
// bson.D{{Key: "$match", Value: filterDoc}}.
func MatchStage(f Filter) (bson.D, error) {
	return MatchStageWithOptions(f, DefaultOptions())
}

// MatchStageWithOptions compiles f using the provided Options and wraps it in a MongoDB $match aggregation stage:
// bson.D{{Key: "$match", Value: filterDoc}}.
func MatchStageWithOptions(f Filter, opts Options) (bson.D, error) {
	doc, err := f.BuildWithOptions(opts)
	if err != nil {
		return nil, err
	}
	return bson.D{{Key: "$match", Value: doc}}, nil
}

func (f *Filter) buildAtDepth(depth int, cfg Options) (bson.D, error) {
	if f.err != nil {
		return nil, f.err
	}
	if depth > cfg.MaxDepth {
		return nil, fmt.Errorf("%w: depth %d exceeds max %d", ErrMaxDepthExceeded, depth, cfg.MaxDepth)
	}

	switch f.kind {
	case kindEmpty:
		return nil, ErrEmptyFilter

	case kindMatchAll:
		return bson.D{}, nil

	case kindScalarOp:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		return bson.D{{
			Key:   f.field,
			Value: bson.D{{Key: f.op, Value: f.val}},
		}}, nil

	case kindSliceOp:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if f.num == 0 {
			return nil, fmt.Errorf("%w: %s on field %q", ErrEmptySlice, f.op, f.field)
		}
		if f.num > cfg.MaxInElements {
			return nil, fmt.Errorf("%w: %s on field %q has %d elements (max %d)", ErrMaxElementsExceeded, f.op, f.field, f.num, cfg.MaxInElements)
		}
		return bson.D{{
			Key:   f.field,
			Value: bson.D{{Key: f.op, Value: f.val}},
		}}, nil

	case kindExists:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		return bson.D{{
			Key:   f.field,
			Value: bson.D{{Key: "$exists", Value: f.val}},
		}}, nil

	case kindExistsNotNull:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if depth+1 > cfg.MaxDepth {
			return nil, fmt.Errorf("%w: depth %d exceeds max %d", ErrMaxDepthExceeded, depth+1, cfg.MaxDepth)
		}
		return bson.D{{
			Key: "$and",
			Value: bson.A{
				bson.D{{Key: f.field, Value: bson.D{{Key: "$exists", Value: true}}}},
				bson.D{{Key: f.field, Value: bson.D{{Key: "$ne", Value: nil}}}},
			},
		}}, nil

	case kindElemMatchFilter:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if len(f.children) != 1 {
			return nil, ErrEmptyElemMatch
		}
		sub := &f.children[0]
		if sub.err != nil {
			return nil, sub.err
		}
		if sub.kind == kindEmpty || sub.kind == kindMatchAll {
			return nil, fmt.Errorf("%w: field %q", ErrEmptyElemMatch, f.field)
		}
		subDoc, err := sub.buildAtDepth(depth+1, cfg)
		if err != nil {
			return nil, err
		}
		return bson.D{{
			Key:   f.field,
			Value: bson.D{{Key: "$elemMatch", Value: subDoc}},
		}}, nil

	case kindElemMatchScalar:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		scalarOps, ok := f.val.(bson.D)
		if !ok || len(scalarOps) == 0 {
			return nil, fmt.Errorf("%w: field %q has no scalar operators", ErrEmptyElemMatch, f.field)
		}
		for _, elem := range scalarOps {
			if !isValidScalarOp(elem.Key) {
				return nil, fmt.Errorf("%w: invalid scalar operator %q on field %q", ErrInvalidScalarValue, elem.Key, f.field)
			}
		}
		if depth+1 > cfg.MaxDepth {
			return nil, fmt.Errorf("%w: depth %d exceeds max %d", ErrMaxDepthExceeded, depth+1, cfg.MaxDepth)
		}
		return bson.D{{
			Key:   f.field,
			Value: bson.D{{Key: "$elemMatch", Value: scalarOps}},
		}}, nil

	case kindSize:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if f.num < 0 {
			return nil, fmt.Errorf("%w: field %q size %d", ErrNegativeArraySize, f.field, f.num)
		}
		return bson.D{{
			Key:   f.field,
			Value: bson.D{{Key: "$size", Value: f.num}},
		}}, nil

	case kindExprArraySizeField:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		err = ValidateField(f.str)
		if err != nil {
			return nil, err
		}
		sizeExpr := bson.D{{
			Key:   "$size",
			Value: bson.D{{Key: "$ifNull", Value: bson.A{"$" + f.field, bson.A{}}}},
		}}
		return bson.D{{
			Key: "$expr",
			Value: bson.D{{
				Key:   f.op,
				Value: bson.A{sizeExpr, "$" + f.str},
			}},
		}}, nil

	case kindExprArraySizeConst:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if f.num < 0 {
			return nil, fmt.Errorf("%w: field %q size %d", ErrNegativeArraySize, f.field, f.num)
		}
		sizeExpr := bson.D{{
			Key:   "$size",
			Value: bson.D{{Key: "$ifNull", Value: bson.A{"$" + f.field, bson.A{}}}},
		}}
		return bson.D{{
			Key: "$expr",
			Value: bson.D{{
				Key:   f.op,
				Value: bson.A{sizeExpr, f.num},
			}},
		}}, nil

	case kindExprFieldCompare:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		err = ValidateField(f.str)
		if err != nil {
			return nil, err
		}
		return bson.D{{
			Key: "$expr",
			Value: bson.D{{
				Key:   f.op,
				Value: bson.A{"$" + f.field, "$" + f.str},
			}},
		}}, nil

	case kindRegex:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if len(f.str) == 0 {
			return nil, fmt.Errorf("%w: field %q", ErrInvalidPattern, f.field)
		}
		if strings.IndexByte(f.str, 0) != -1 {
			return nil, fmt.Errorf("%w: field %q pattern contains null byte", ErrInvalidPattern, f.field)
		}
		if len(f.str) > cfg.MaxPatternLength {
			return nil, fmt.Errorf("%w: field %q pattern length %d exceeds max %d", ErrPatternTooLong, f.field, len(f.str), cfg.MaxPatternLength)
		}
		escaped := buildSafeRegexPattern(f.str, f.regexFlags)
		var regexDoc bson.D
		if f.regexFlags&regexFlagCaseInsensitive != 0 {
			regexDoc = bson.D{
				{Key: "$regex", Value: escaped},
				{Key: "$options", Value: "i"},
			}
		} else {
			regexDoc = bson.D{
				{Key: "$regex", Value: escaped},
			}
		}
		return bson.D{{
			Key:   f.field,
			Value: regexDoc,
		}}, nil

	case kindLogical:
		if len(f.children) == 0 {
			return nil, fmt.Errorf("%w: %s", ErrEmptyLogicalGroup, f.op)
		}
		nextDepth := depth + 1
		if nextDepth > cfg.MaxDepth {
			return nil, fmt.Errorf("%w: depth %d exceeds max %d", ErrMaxDepthExceeded, nextDepth, cfg.MaxDepth)
		}
		clauses := make(bson.A, 0, len(f.children))
		for i := range f.children {
			child := &f.children[i]
			if child.err != nil {
				return nil, child.err
			}
			if child.kind == kindEmpty || child.kind == kindMatchAll {
				return nil, fmt.Errorf("%w: child %d in %s", ErrEmptyFilter, i, f.op)
			}
			childDoc, err := child.buildAtDepth(nextDepth, cfg)
			if err != nil {
				return nil, err
			}
			clauses = append(clauses, childDoc)
		}
		return bson.D{{
			Key:   f.op,
			Value: clauses,
		}}, nil

	case kindNot:
		err := ValidateField(f.field)
		if err != nil {
			return nil, err
		}
		if !isValidScalarOp(f.op) {
			return nil, fmt.Errorf("%w: invalid scalar operator %q on field %q", ErrInvalidScalarValue, f.op, f.field)
		}
		if depth+1 > cfg.MaxDepth {
			return nil, fmt.Errorf("%w: depth %d exceeds max %d", ErrMaxDepthExceeded, depth+1, cfg.MaxDepth)
		}
		return bson.D{{
			Key: f.field,
			Value: bson.D{{
				Key:   "$not",
				Value: bson.D{{Key: f.op, Value: f.val}},
			}},
		}}, nil

	default:
		return nil, ErrEmptyFilter
	}
}

func isRegexSpecial(b byte) bool {
	switch b {
	case '\\', '.', '+', '*', '?', '(', ')', '|', '[', ']', '{', '}', '^', '$':
		return true
	default:
		return false
	}
}

func buildSafeRegexPattern(raw string, flags regexFlag) string {
	anchorStart := flags&regexFlagAnchorStart != 0
	anchorEnd := flags&regexFlagAnchorEnd != 0

	extra := 0
	for i := 0; i < len(raw); i++ {
		if isRegexSpecial(raw[i]) {
			extra++
		}
	}
	if extra == 0 && !anchorStart && !anchorEnd {
		return raw
	}

	totalLen := len(raw) + extra
	if anchorStart {
		totalLen++
	}
	if anchorEnd {
		totalLen++
	}

	var sb strings.Builder
	sb.Grow(totalLen)
	if anchorStart {
		sb.WriteByte('^')
	}
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if isRegexSpecial(c) {
			sb.WriteByte('\\')
		}
		sb.WriteByte(c)
	}
	if anchorEnd {
		sb.WriteByte('$')
	}
	return sb.String()
}

// MatchAll explicitly constructs an empty filter document (bson.D{}) that matches all documents in a collection.
// MatchAll cannot be used as a sub-filter inside ElemMatch.
func MatchAll() Filter {
	return Filter{kind: kindMatchAll}
}

// Eq creates a scalar equality filter wrapped in $eq: {field: {$eq: val}}.
func Eq[T Scalar](field string, val T) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$eq",
		val:   val,
	}
}

// Ne creates a scalar inequality filter wrapped in $ne: {field: {$ne: val}}.
func Ne[T Scalar](field string, val T) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$ne",
		val:   val,
	}
}

// Gt creates a greater-than filter wrapped in $gt: {field: {$gt: val}}.
func Gt[T Scalar](field string, val T) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$gt",
		val:   val,
	}
}

// Gte creates a greater-than-or-equal filter wrapped in $gte: {field: {$gte: val}}.
func Gte[T Scalar](field string, val T) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$gte",
		val:   val,
	}
}

// Lt creates a less-than filter wrapped in $lt: {field: {$lt: val}}.
func Lt[T Scalar](field string, val T) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$lt",
		val:   val,
	}
}

// Lte creates a less-than-or-equal filter wrapped in $lte: {field: {$lte: val}}.
func Lte[T Scalar](field string, val T) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$lte",
		val:   val,
	}
}

// EqNil creates an explicit null-equality filter: {field: {$eq: nil}}.
func EqNil(field string) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$eq",
		val:   nil,
	}
}

// NeNil creates an explicit null-inequality filter: {field: {$ne: nil}}.
func NeNil(field string) Filter {
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$ne",
		val:   nil,
	}
}

// EqAny creates a scalar equality filter from a dynamic `any` value after strict runtime scalar validation.
// Maps (bson.M, map[string]any), slices, nil, pointers, and unapproved structs are rejected with ErrInvalidScalarValue.
func EqAny(field string, val any) Filter {
	err := validateDynamicScalar(val)
	if err != nil {
		return Filter{err: err}
	}
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$eq",
		val:   val,
	}
}

// NeAny creates a scalar inequality filter from a dynamic `any` value after strict runtime scalar validation.
func NeAny(field string, val any) Filter {
	err := validateDynamicScalar(val)
	if err != nil {
		return Filter{err: err}
	}
	return Filter{
		kind:  kindScalarOp,
		field: field,
		op:    "$ne",
		val:   val,
	}
}

// In creates a bounded $in filter: {field: {$in: vals}}.
// Returns ErrEmptySlice on Build() if vals is empty, or ErrMaxElementsExceeded if len(vals) > MaxInElements.
func In[T Scalar](field string, vals []T) Filter {
	var copied []T
	if len(vals) > 0 {
		copied = make([]T, len(vals))
		copy(copied, vals)
	}
	return Filter{
		kind:  kindSliceOp,
		field: field,
		op:    "$in",
		val:   copied,
		num:   len(copied),
	}
}

// Nin creates a bounded $nin filter: {field: {$nin: vals}}.
// Returns ErrEmptySlice on Build() if vals is empty, or ErrMaxElementsExceeded if len(vals) > MaxInElements.
func Nin[T Scalar](field string, vals []T) Filter {
	var copied []T
	if len(vals) > 0 {
		copied = make([]T, len(vals))
		copy(copied, vals)
	}
	return Filter{
		kind:  kindSliceOp,
		field: field,
		op:    "$nin",
		val:   copied,
		num:   len(copied),
	}
}

// All creates a bounded $all array filter: {field: {$all: vals}}.
// Returns ErrEmptySlice on Build() if vals is empty, or ErrMaxElementsExceeded if len(vals) > MaxInElements.
func All[T Scalar](field string, vals []T) Filter {
	var copied []T
	if len(vals) > 0 {
		copied = make([]T, len(vals))
		copy(copied, vals)
	}
	return Filter{
		kind:  kindSliceOp,
		field: field,
		op:    "$all",
		val:   copied,
		num:   len(copied),
	}
}

// Exists creates a strict boolean existence filter: {field: {$exists: true}}.
// Note: In MongoDB, $exists: true also matches documents where the field value is explicitly null.
// Use ExistsNotNull(field) to require both existence and a non-null value.
func Exists(field string) Filter {
	return Filter{
		kind:  kindExists,
		field: field,
		val:   true,
	}
}

// NotExists creates a strict boolean non-existence filter: {field: {$exists: false}}.
func NotExists(field string) Filter {
	return Filter{
		kind:  kindExists,
		field: field,
		val:   false,
	}
}

// ExistsNotNull creates a composite filter requiring that field exists and is not null:
// {$and: [{field: {$exists: true}}, {field: {$ne: nil}}]}.
func ExistsNotNull(field string) Filter {
	return Filter{
		kind:  kindExistsNotNull,
		field: field,
	}
}

// ElemMatch creates a validated $elemMatch filter for an array of embedded documents:
// {arrayField: {$elemMatch: subFilter}}.
// Rejects empty sub-filters and MatchAll() with ErrEmptyElemMatch.
func ElemMatch(arrayField string, subFilter Filter) Filter {
	return Filter{
		kind:     kindElemMatchFilter,
		field:    arrayField,
		children: []Filter{subFilter},
	}
}

// ScalarElemMatch creates a validated $elemMatch filter for an array of primitive scalar values:
// {arrayField: {$elemMatch: {$op1: val1, ...}}}.
// Requires at least one ScalarOp (e.g., OpGte, OpLt).
func ScalarElemMatch[T Scalar](arrayField string, ops ...ScalarOp[T]) Filter {
	if len(ops) == 0 {
		return Filter{
			kind:  kindElemMatchScalar,
			field: arrayField,
		}
	}
	doc := make(bson.D, 0, len(ops))
	for _, op := range ops {
		doc = append(doc, bson.E{Key: op.op, Value: op.val})
	}
	return Filter{
		kind:  kindElemMatchScalar,
		field: arrayField,
		val:   doc,
	}
}

// Size creates an array length filter: {arrayField: {$size: size}}.
// Returns ErrNegativeArraySize on Build() if size < 0.
func Size(arrayField string, size int) Filter {
	return Filter{
		kind:  kindSize,
		field: arrayField,
		num:   size,
	}
}

// ArraySizeLtField creates a safe $expr filter checking that the length of arrayField
// (treating missing or null arrayField as empty []) is strictly less than the numeric document field limitField:
// {$expr: {$lt: [{$size: {$ifNull: ["$" + arrayField, []]}}, "$" + limitField]}}.
func ArraySizeLtField(arrayField, limitField string) Filter {
	return Filter{
		kind:  kindExprArraySizeField,
		field: arrayField,
		str:   limitField,
		op:    "$lt",
	}
}

// ArraySizeLt creates a safe $expr filter checking that the length of arrayField
// (treating missing or null as []) is strictly less than size:
// {$expr: {$lt: [{$size: {$ifNull: ["$" + arrayField, []]}}, size]}}.
func ArraySizeLt(arrayField string, size int) Filter {
	return Filter{
		kind:  kindExprArraySizeConst,
		field: arrayField,
		op:    "$lt",
		num:   size,
	}
}

// ArraySizeLte creates a safe $expr filter checking that the length of arrayField
// (treating missing or null as []) is less than or equal to size:
// {$expr: {$lte: [{$size: {$ifNull: ["$" + arrayField, []]}}, size]}}.
func ArraySizeLte(arrayField string, size int) Filter {
	return Filter{
		kind:  kindExprArraySizeConst,
		field: arrayField,
		op:    "$lte",
		num:   size,
	}
}

// ArraySizeGt creates a safe $expr filter checking that the length of arrayField
// (treating missing or null as []) is strictly greater than size:
// {$expr: {$gt: [{$size: {$ifNull: ["$" + arrayField, []]}}, size]}}.
func ArraySizeGt(arrayField string, size int) Filter {
	return Filter{
		kind:  kindExprArraySizeConst,
		field: arrayField,
		op:    "$gt",
		num:   size,
	}
}

// ArraySizeGte creates a safe $expr filter checking that the length of arrayField
// (treating missing or null as []) is greater than or equal to size:
// {$expr: {$gte: [{$size: {$ifNull: ["$" + arrayField, []]}}, size]}}.
func ArraySizeGte(arrayField string, size int) Filter {
	return Filter{
		kind:  kindExprArraySizeConst,
		field: arrayField,
		op:    "$gte",
		num:   size,
	}
}

// ExprEqFields creates a safe $expr filter comparing two document fields for equality:
// {$expr: {$eq: ["$" + leftField, "$" + rightField]}}.
func ExprEqFields(leftField, rightField string) Filter {
	return Filter{
		kind:  kindExprFieldCompare,
		field: leftField,
		str:   rightField,
		op:    "$eq",
	}
}

// ExprNeFields creates a safe $expr filter comparing two document fields for inequality:
// {$expr: {$ne: ["$" + leftField, "$" + rightField]}}.
func ExprNeFields(leftField, rightField string) Filter {
	return Filter{
		kind:  kindExprFieldCompare,
		field: leftField,
		str:   rightField,
		op:    "$ne",
	}
}

// ExprLtFields creates a safe $expr filter checking whether leftField < rightField:
// {$expr: {$lt: ["$" + leftField, "$" + rightField]}}.
func ExprLtFields(leftField, rightField string) Filter {
	return Filter{
		kind:  kindExprFieldCompare,
		field: leftField,
		str:   rightField,
		op:    "$lt",
	}
}

// ExprLteFields creates a safe $expr filter checking whether leftField <= rightField:
// {$expr: {$lte: ["$" + leftField, "$" + rightField]}}.
func ExprLteFields(leftField, rightField string) Filter {
	return Filter{
		kind:  kindExprFieldCompare,
		field: leftField,
		str:   rightField,
		op:    "$lte",
	}
}

// ExprGtFields creates a safe $expr filter checking whether leftField > rightField:
// {$expr: {$gt: ["$" + leftField, "$" + rightField]}}.
func ExprGtFields(leftField, rightField string) Filter {
	return Filter{
		kind:  kindExprFieldCompare,
		field: leftField,
		str:   rightField,
		op:    "$gt",
	}
}

// ExprGteFields creates a safe $expr filter checking whether leftField >= rightField:
// {$expr: {$gte: ["$" + leftField, "$" + rightField]}}.
func ExprGteFields(leftField, rightField string) Filter {
	return Filter{
		kind:  kindExprFieldCompare,
		field: leftField,
		str:   rightField,
		op:    "$gte",
	}
}

// Prefix creates a case-sensitive, index-friendly prefix regex filter:
// {field: {$regex: "^" + regexp.QuoteMeta(prefix)}}.
func Prefix(field, prefix string) Filter {
	return Filter{
		kind:       kindRegex,
		field:      field,
		str:        prefix,
		regexFlags: regexFlagAnchorStart,
	}
}

// PrefixCaseInsensitive creates a case-insensitive prefix regex filter:
// {field: {$regex: "^" + regexp.QuoteMeta(prefix), $options: "i"}}.
func PrefixCaseInsensitive(field, prefix string) Filter {
	return Filter{
		kind:       kindRegex,
		field:      field,
		str:        prefix,
		regexFlags: regexFlagAnchorStart | regexFlagCaseInsensitive,
	}
}

// Suffix creates a case-sensitive suffix regex filter:
// {field: {$regex: regexp.QuoteMeta(suffix) + "$"}}.
func Suffix(field, suffix string) Filter {
	return Filter{
		kind:       kindRegex,
		field:      field,
		str:        suffix,
		regexFlags: regexFlagAnchorEnd,
	}
}

// SuffixCaseInsensitive creates a case-insensitive suffix regex filter:
// {field: {$regex: regexp.QuoteMeta(suffix) + "$", $options: "i"}}.
func SuffixCaseInsensitive(field, suffix string) Filter {
	return Filter{
		kind:       kindRegex,
		field:      field,
		str:        suffix,
		regexFlags: regexFlagAnchorEnd | regexFlagCaseInsensitive,
	}
}

// Contains creates a case-sensitive substring regex filter:
// {field: {$regex: regexp.QuoteMeta(substr)}}.
func Contains(field, substr string) Filter {
	return Filter{
		kind:  kindRegex,
		field: field,
		str:   substr,
	}
}

// ContainsCaseInsensitive creates a case-insensitive substring regex filter:
// {field: {$regex: regexp.QuoteMeta(substr), $options: "i"}}.
func ContainsCaseInsensitive(field, substr string) Filter {
	return Filter{
		kind:       kindRegex,
		field:      field,
		str:        substr,
		regexFlags: regexFlagCaseInsensitive,
	}
}

// ExactCaseInsensitive creates a case-insensitive exact string match filter:
// {field: {$regex: "^" + regexp.QuoteMeta(val) + "$", $options: "i"}}.
func ExactCaseInsensitive(field, val string) Filter {
	return Filter{
		kind:       kindRegex,
		field:      field,
		str:        val,
		regexFlags: regexFlagAnchorStart | regexFlagAnchorEnd | regexFlagCaseInsensitive,
	}
}

// And combines one or more Filters into a logical $and expression: {$and: [...]}.
// Returns ErrEmptyLogicalGroup on Build() if no filters are provided.
func And(filters ...Filter) Filter {
	var copied []Filter
	if len(filters) > 0 {
		copied = make([]Filter, len(filters))
		copy(copied, filters)
	}
	return Filter{
		kind:     kindLogical,
		op:       "$and",
		children: copied,
	}
}

// Or combines one or more Filters into a logical $or expression: {$or: [...]}.
// Returns ErrEmptyLogicalGroup on Build() if no filters are provided.
func Or(filters ...Filter) Filter {
	var copied []Filter
	if len(filters) > 0 {
		copied = make([]Filter, len(filters))
		copy(copied, filters)
	}
	return Filter{
		kind:     kindLogical,
		op:       "$or",
		children: copied,
	}
}

// Nor combines one or more Filters into a logical $nor expression: {$nor: [...]}.
// Returns ErrEmptyLogicalGroup on Build() if no filters are provided.
func Nor(filters ...Filter) Filter {
	var copied []Filter
	if len(filters) > 0 {
		copied = make([]Filter, len(filters))
		copy(copied, filters)
	}
	return Filter{
		kind:     kindLogical,
		op:       "$nor",
		children: copied,
	}
}

// Not creates a field-level logical negation of a scalar operator: {field: {$not: {$op: val}}}.
func Not[T Scalar](field string, op ScalarOp[T]) Filter {
	return Filter{
		kind:  kindNot,
		field: field,
		op:    op.op,
		val:   op.val,
	}
}
