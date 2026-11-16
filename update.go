package mq

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type updateEntry struct {
	op    string
	field string
	val   any
	isSub bool
}

// Update is a security-hardened builder for MongoDB update documents.
// It guarantees that all mutations are scoped under explicit $-prefixed update operators
// ($set, $setOnInsert, $unset, $inc, $min, $max, $currentDate, $addToSet, $push, $pull),
// preventing accidental document replacement and field-name operator injection.
//
// Use NewUpdate() as the canonical entrypoint for non-scalar and advanced updates
// (structs, slices, string maps, nilable scalar pointers, $setOnInsert, $addToSet,
// $min, $max, $currentDate, PushStruct, PullFilter, and SetNil). Package-level
// constructors (Set, Unset, Inc, Push, Pull) provide compile-time type-safe
// shorthands for simple scalar mutations.
type Update struct {
	entries   []updateEntry
	err       error
	bootstrap [4]updateEntry
}

// NewUpdate creates a new empty Update builder.
//
// NewUpdate is the canonical entrypoint for non-scalar and advanced update chains
// (structs, slices, string maps, nilable scalar pointers, $setOnInsert, $addToSet,
// $min, $max, $currentDate, PushStruct, PullFilter, and SetNil).
func NewUpdate() *Update {
	u := &Update{}
	u.entries = u.bootstrap[:0]
	return u
}

func (u *Update) appendEntry(entry updateEntry) *Update {
	if u.err != nil {
		return u
	}
	if u.entries == nil {
		u.entries = u.bootstrap[:0]
	}
	u.entries = append(u.entries, entry)
	return u
}

func (u *Update) appendOp(op, field string, val any) *Update {
	return u.appendEntry(updateEntry{
		op:    op,
		field: field,
		val:   val,
	})
}

// Set adds a scalar field assignment (or a nilable pointer to a scalar) to the $set operator:
// {$set: {field: val}}.
func (u *Update) Set(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	norm, err := normalizeUpdateScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$set", field, norm)
}

// SetNil explicitly sets a field to null under $set: {$set: {field: nil}}.
func (u *Update) SetNil(field string) *Update {
	return u.appendOp("$set", field, nil)
}

// SetOnInsert adds a scalar field assignment (or a nilable pointer to a scalar) to $setOnInsert
// for upsert operations: {$setOnInsert: {field: val}}.
func (u *Update) SetOnInsert(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	norm, err := normalizeUpdateScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$setOnInsert", field, norm)
}

// SetStruct marshals a Go domain struct (or non-nil pointer to struct) into a validated bson.D
// (verifying no $-prefixed keys exist at any nesting level) and assigns it to field under $set:
// {$set: {field: <structDoc>}}.
func (u *Update) SetStruct(field string, v any) *Update {
	if u.err != nil {
		return u
	}
	doc, err := marshalStructToSafeDoc(v, false)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$set", field, doc)
}

// SetDocumentStruct marshals a Go domain struct (or non-nil pointer to struct) into a validated bson.D,
// automatically strips "_id" so immutable document IDs are never modified on update, and merges all fields into $set.
func (u *Update) SetDocumentStruct(v any) *Update {
	if u.err != nil {
		return u
	}
	doc, err := marshalStructToSafeDoc(v, true)
	if err != nil {
		u.err = err
		return u
	}
	for _, elem := range doc {
		u.appendOp("$set", elem.Key, elem.Value)
	}
	return u
}

// SetOnInsertDocumentStruct marshals a Go domain struct (or non-nil pointer to struct) into a validated bson.D
// (preserving "_id") and merges all fields into $setOnInsert.
func (u *Update) SetOnInsertDocumentStruct(v any) *Update {
	if u.err != nil {
		return u
	}
	doc, err := marshalStructToSafeDoc(v, false)
	if err != nil {
		u.err = err
		return u
	}
	for _, elem := range doc {
		u.appendOp("$setOnInsert", elem.Key, elem.Value)
	}
	return u
}

// SetEmptySlice sets an array field to an empty BSON array (bson.A{}) under $set:
// {$set: {field: []}}.
func (u *Update) SetEmptySlice(field string) *Update {
	return u.appendOp("$set", field, bson.A{})
}

// SetSlice validates a slice of scalars or domain structs (rejecting maps, bson.D, bson.M, or non-slice values)
// and assigns it to field under $set: {$set: {field: [...]}}.
// Passing an empty typed slice (e.g. []models.Seat{}) emits an empty BSON array (bson.A{}).
func (u *Update) SetSlice(field string, slice any) *Update {
	if u.err != nil {
		return u
	}
	if slice == nil {
		u.err = fmt.Errorf("field %q: %w: slice cannot be nil; use SetEmptySlice or SetNil", field, ErrInvalidScalarValue)
		return u
	}

	rv := reflect.ValueOf(slice)
	rt := rv.Type()
	if rt.Kind() != reflect.Slice || rt == bsonDType || rt == bsonAType || rt == bsonRawType {
		u.err = fmt.Errorf("field %q: %w: expected typed Go slice, got %s", field, ErrInvalidScalarValue, rt.String())
		return u
	}

	elemType := rt.Elem()
	elemIsScalar := isApprovedScalarType(elemType)
	elemIsStruct := isUserStructType(elemType) || (elemType.Kind() == reflect.Pointer && isUserStructType(elemType.Elem()))
	if !elemIsScalar && !elemIsStruct {
		u.err = fmt.Errorf("field %q: %w: slice element type %s is not a permitted scalar or domain struct", field, ErrInvalidScalarValue, elemType.String())
		return u
	}

	n := rv.Len()
	arr := make(bson.A, 0, n)
	for i := 0; i < n; i++ {
		item := rv.Index(i).Interface()
		if elemIsScalar {
			arr = append(arr, item)
			continue
		}
		doc, err := marshalStructToSafeDoc(item, false)
		if err != nil {
			u.err = fmt.Errorf("field %q index %d: %w", field, i, err)
			return u
		}
		arr = append(arr, doc)
	}

	return u.appendOp("$set", field, arr)
}

// SetStringMap validates all keys in m via ValidateMapKey (rejecting empty keys, dots, leading '$', or control chars)
// and assigns the map to field under $set: {$set: {field: m}}.
func (u *Update) SetStringMap(field string, m map[string]string) *Update {
	if u.err != nil {
		return u
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		err := ValidateMapKey(k)
		if err != nil {
			u.err = fmt.Errorf("field %q map key %q: %w", field, k, err)
			return u
		}
		keys = append(keys, k)
	}
	slices.Sort(keys)
	doc := make(bson.D, 0, len(keys))
	for _, k := range keys {
		doc = append(doc, bson.E{Key: k, Value: m[k]})
	}
	return u.appendOp("$set", field, doc)
}

// Unset removes a field from the document: {$unset: {field: ""}}.
func (u *Update) Unset(field string) *Update {
	return u.appendOp("$unset", field, "")
}

// Inc increments a numeric field by delta: {$inc: {field: delta}}.
func (u *Update) Inc(field string, delta any) *Update {
	if u.err != nil {
		return u
	}
	if !isNumericScalar(delta) {
		u.err = fmt.Errorf("field %q: %w: $inc requires a numeric value", field, ErrInvalidScalarValue)
		return u
	}
	return u.appendOp("$inc", field, delta)
}

// Min updates the field only if the specified scalar value is less than the existing field value:
// {$min: {field: val}}.
func (u *Update) Min(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	err := validateDynamicScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$min", field, val)
}

// Max updates the field only if the specified scalar value is greater than the existing field value:
// {$max: {field: val}}.
func (u *Update) Max(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	err := validateDynamicScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$max", field, val)
}

// CurrentDate sets the value of a field to the current server date/time:
// {$currentDate: {field: true}}.
func (u *Update) CurrentDate(field string) *Update {
	return u.appendOp("$currentDate", field, true)
}

// AddToSet appends a scalar value to an array only if it does not already exist:
// {$addToSet: {field: val}}.
func (u *Update) AddToSet(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	err := validateDynamicScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$addToSet", field, val)
}

// Push appends a scalar value to an array field: {$push: {field: val}}.
func (u *Update) Push(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	err := validateDynamicScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$push", field, val)
}

// PushStruct marshals a Go domain struct (or non-nil pointer to struct) into a validated bson.D
// (verifying no $-prefixed keys exist at any nesting level) and appends it to an array field under $push:
// {$push: {field: <structDoc>}}.
func (u *Update) PushStruct(field string, v any) *Update {
	if u.err != nil {
		return u
	}
	doc, err := marshalStructToSafeDoc(v, false)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$push", field, doc)
}

// Pull removes all instances of a scalar value from an array field:
// {$pull: {field: {$eq: val}}}.
func (u *Update) Pull(field string, val any) *Update {
	if u.err != nil {
		return u
	}
	err := validateDynamicScalar(val)
	if err != nil {
		u.err = fmt.Errorf("field %q: %w", field, err)
		return u
	}
	return u.appendOp("$pull", field, bson.D{{Key: "$eq", Value: val}})
}

// PullFilter removes array elements matching a validated Filter (for arrays of embedded documents):
// {$pull: {arrayField: <subFilter>}}.
// Rejects empty sub-filters and MatchAll() so $pull never accidentally wipes all elements.
func (u *Update) PullFilter(arrayField string, subFilter Filter) *Update {
	return u.appendEntry(updateEntry{
		op:    "$pull",
		field: arrayField,
		val:   subFilter,
		isSub: true,
	})
}

// Build validates all update operations and compiles the Update into a bson.D document.
// Returns ErrEmptyUpdate if no operations were added, ErrInvalidFieldName if any field path is unsafe,
// or ErrDuplicateUpdateField if the same or hierarchical parent/child field path is targeted more than once.
func (u *Update) Build() (bson.D, error) {
	return u.BuildWithOptions(DefaultOptions())
}

type updateOpGroup struct {
	op    string
	count int
	doc   bson.D
}

func fieldsConflict(a, b string) bool {
	if a == b {
		return true
	}
	if len(a) < len(b) {
		return b[len(a)] == '.' && strings.HasPrefix(b, a)
	}
	if len(b) < len(a) {
		return a[len(b)] == '.' && strings.HasPrefix(a, b)
	}
	return false
}

// BuildWithOptions validates all update operations using the provided Options and compiles into a bson.D document.
func (u *Update) BuildWithOptions(opts Options) (bson.D, error) {
	if u == nil || (u.err == nil && len(u.entries) == 0) {
		return nil, ErrEmptyUpdate
	}
	if u.err != nil {
		return nil, u.err
	}

	cfg := opts.normalized()
	entries := u.entries
	n := len(entries)

	var groups [10]updateOpGroup
	numGroups := 0

	for i := 0; i < n; i++ {
		field := entries[i].field
		err := ValidateUpdateField(field)
		if err != nil {
			return nil, err
		}
		for j := 0; j < i; j++ {
			if fieldsConflict(entries[j].field, field) {
				return nil, fmt.Errorf("%w: field %q conflicts with %q", ErrDuplicateUpdateField, field, entries[j].field)
			}
		}

		op := entries[i].op
		found := false
		for g := 0; g < numGroups; g++ {
			if groups[g].op == op {
				groups[g].count++
				found = true
				break
			}
		}
		if !found {
			groups[numGroups] = updateOpGroup{op: op, count: 1}
			numGroups++
		}
	}

	for g := 0; g < numGroups; g++ {
		groups[g].doc = make(bson.D, 0, groups[g].count)
	}

	for i := 0; i < n; i++ {
		entry := &entries[i]
		val := entry.val
		if entry.isSub {
			sub := val.(Filter)
			if sub.err != nil {
				return nil, sub.err
			}
			if sub.kind == kindEmpty || sub.kind == kindMatchAll {
				return nil, fmt.Errorf("%w: PullFilter on field %q", ErrEmptyElemMatch, entry.field)
			}
			subDoc, subErr := sub.buildAtDepth(1, cfg)
			if subErr != nil {
				return nil, subErr
			}
			val = subDoc
		}

		for g := 0; g < numGroups; g++ {
			if groups[g].op == entry.op {
				groups[g].doc = append(groups[g].doc, bson.E{
					Key:   entry.field,
					Value: val,
				})
				break
			}
		}
	}

	result := make(bson.D, 0, numGroups)
	for g := 0; g < numGroups; g++ {
		result = append(result, bson.E{
			Key:   groups[g].op,
			Value: groups[g].doc,
		})
	}
	return result, nil
}

// Set creates a new Update builder initialized with {$set: {field: val}}.
func Set[T Scalar](field string, val T) *Update {
	return NewUpdate().appendOp("$set", field, val)
}

// Unset creates a new Update builder initialized with {$unset: {field: ""}}.
func Unset(field string) *Update {
	return NewUpdate().Unset(field)
}

// Inc creates a new Update builder initialized with {$inc: {field: delta}}.
func Inc[T Numeric](field string, delta T) *Update {
	return NewUpdate().appendOp("$inc", field, delta)
}

// Push creates a new Update builder initialized with {$push: {field: val}}.
func Push[T Scalar](field string, val T) *Update {
	return NewUpdate().appendOp("$push", field, val)
}

// Pull creates a new Update builder initialized with {$pull: {field: {$eq: val}}}.
func Pull[T Scalar](field string, val T) *Update {
	return NewUpdate().appendOp("$pull", field, bson.D{{Key: "$eq", Value: val}})
}

func isNumericScalar(val any) bool {
	if val == nil {
		return false
	}
	switch val.(type) {
	case int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64,
		bson.Decimal128:
		return true
	}
	k := reflect.TypeOf(val).Kind()
	switch k {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
