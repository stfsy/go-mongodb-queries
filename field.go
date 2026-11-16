package mq

import (
	"fmt"
	"reflect"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// Scalar defines the compile-time type constraint for permitted MongoDB scalar operands.
// Maps, slices, and arbitrary structs are excluded at compile time.
// Note: bson.DateTime is defined as `type DateTime int64` and is automatically covered by ~int64.
type Scalar interface {
	~string | ~bool |
		~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 |
		time.Time | bson.ObjectID | bson.Decimal128 | bson.Timestamp
}

// Numeric defines the compile-time type constraint for numeric operands used in $inc.
type Numeric interface {
	~int | ~int8 | ~int16 | ~int32 | ~int64 |
		~uint | ~uint8 | ~uint16 | ~uint32 | ~uint64 |
		~float32 | ~float64 |
		bson.Decimal128
}

// ValidateField verifies that a MongoDB field name or dot-separated path is safe from operator injection.
//
// Security invariants enforced:
//  1. Must not be empty or exceed MaxFieldNameLength (255 bytes).
//  2. Must not start with '$' (prevents top-level operator injection).
//  3. Must not start or end with '.', and must not contain consecutive dots ".." (empty path segments).
//  4. No dot-separated segment may start with '$' (prevents nested operator injection such as "a.$gt").
//  5. Must not contain null bytes (0x00) or ASCII control characters (< 0x20 or 0x7f).
func ValidateField(field string) error {
	return validateFieldPath(field, false)
}

// ValidateUpdateField verifies that a MongoDB update target path is safe from operator injection.
// In addition to standard dot-separated field paths, it permits positional array update tokens
// ("$" and "$[]") after the root array segment (e.g., "seats.$.hwid" or "seats.$[].locked_at"),
// while rejecting '$' at the root segment or any $-prefixed operator name (e.g., "seats.$gt").
func ValidateUpdateField(field string) error {
	return validateFieldPath(field, true)
}

func validateFieldPath(field string, allowPositional bool) error {
	n := len(field)
	if n == 0 {
		return fmt.Errorf("%w: field name cannot be empty", ErrInvalidFieldName)
	}
	if n > MaxFieldNameLength {
		return fmt.Errorf("%w: field length %d exceeds maximum %d", ErrInvalidFieldName, n, MaxFieldNameLength)
	}
	if field[n-1] == '.' {
		return fmt.Errorf("%w: trailing dot in field %q", ErrInvalidFieldName, field)
	}

	segStart := 0
	segIndex := 0
	positionalCount := 0

	for i := 0; i <= n; i++ {
		if i < n {
			c := field[i]
			if c < 0x20 || c == 0x7f {
				return fmt.Errorf("%w: field contains control character at index %d", ErrInvalidFieldName, i)
			}
			if c != '.' {
				continue
			}
		}

		if i == segStart {
			return fmt.Errorf("%w: empty path segment in field %q", ErrInvalidFieldName, field)
		}

		if field[segStart] == '$' {
			if !allowPositional || segIndex == 0 {
				return fmt.Errorf("%w: field segment cannot start with '$' in %q", ErrInvalidFieldName, field)
			}
			seg := field[segStart:i]
			if seg == "$" {
				positionalCount++
				if positionalCount > 1 {
					return fmt.Errorf("%w: multiple positional '$' tokens in field %q", ErrInvalidFieldName, field)
				}
			} else if seg != "$[]" {
				return fmt.Errorf("%w: field segment cannot start with '$' in %q", ErrInvalidFieldName, field)
			}
		}

		segStart = i + 1
		segIndex++
	}

	return nil
}

// ValidateMapKey verifies that a map key (e.g. in map[string]string) is a single-segment field name
// without dots, leading '$', or control characters.
func ValidateMapKey(key string) error {
	if strings.IndexByte(key, '.') != -1 {
		return fmt.Errorf("%w: map key %q must not contain dots", ErrInvalidFieldName, key)
	}
	return ValidateField(key)
}

var (
	timeType       = reflect.TypeFor[time.Time]()
	objectIDType   = reflect.TypeFor[bson.ObjectID]()
	decimal128Type = reflect.TypeFor[bson.Decimal128]()
	timestampType  = reflect.TypeFor[bson.Timestamp]()
	bsonEType      = reflect.TypeFor[bson.E]()
	bsonRawValType = reflect.TypeFor[bson.RawValue]()
	bsonDType      = reflect.TypeFor[bson.D]()
	bsonAType      = reflect.TypeFor[bson.A]()
	bsonRawType    = reflect.TypeFor[bson.Raw]()
	bsonJSType     = reflect.TypeFor[bson.JavaScript]()
	bsonSymbolType = reflect.TypeFor[bson.Symbol]()
)

// validateDynamicScalar performs runtime inspection for untyped/dynamic scalar entry points (such as EqAny).
// It rejects nil, maps, slices, arrays (except bson.ObjectID), pointers, functions, channels, and non-scalar structs.
func validateDynamicScalar(val any) error {
	if val == nil {
		return fmt.Errorf("%w: nil is not permitted; use EqNil or NeNil explicitly", ErrInvalidScalarValue)
	}

	switch val.(type) {
	case bson.JavaScript, bson.Symbol:
		return fmt.Errorf("%w: BSON %T is not permitted as a scalar operand", ErrInvalidScalarValue, val)
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64,
		time.Time, bson.ObjectID, bson.Decimal128, bson.DateTime, bson.Timestamp:
		return nil
	}

	rv := reflect.ValueOf(val)
	rt := rv.Type()

	switch rt.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return nil
	case reflect.Array:
		return fmt.Errorf("%w: unsupported array type %s", ErrInvalidScalarValue, rt.String())
	case reflect.Struct:
		return fmt.Errorf("%w: struct type %s is not an approved scalar", ErrInvalidScalarValue, rt.String())
	default:
		return fmt.Errorf("%w: kind %s (%s) is not permitted as a scalar operand", ErrInvalidScalarValue, rt.Kind(), rt.String())
	}
}

// normalizeUpdateScalar validates a scalar operand for Update.Set / SetOnInsert.
// Unlike filter comparisons, it also permits typed pointers to valid scalars (e.g. *time.Time, *int, *bool, *string),
// dereferencing non-nil pointers and converting typed nil pointers to nil.
func normalizeUpdateScalar(val any) (any, error) {
	if val == nil {
		return nil, fmt.Errorf("%w: untyped nil is not permitted; use SetNil explicitly", ErrInvalidScalarValue)
	}

	switch v := val.(type) {
	case bson.JavaScript, bson.Symbol:
		return nil, fmt.Errorf("%w: BSON %T is not permitted as a scalar operand", ErrInvalidScalarValue, val)
	case string, bool,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64,
		time.Time, bson.ObjectID, bson.Decimal128, bson.DateTime, bson.Timestamp:
		return val, nil
	case *string:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *bool:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *int:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *int8:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *int16:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *int32:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *int64:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *uint:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *uint8:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *uint16:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *uint32:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *uint64:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *float32:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *float64:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *time.Time:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *bson.ObjectID:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *bson.Decimal128:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *bson.DateTime:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	case *bson.Timestamp:
		if v == nil {
			return nil, nil
		}
		return *v, nil
	}

	rv := reflect.ValueOf(val)
	if rv.Kind() == reflect.Pointer {
		elemType := rv.Type().Elem()
		if !isApprovedScalarType(elemType) {
			return nil, fmt.Errorf("%w: pointer to non-scalar type %s is not permitted", ErrInvalidScalarValue, rv.Type().String())
		}
		if rv.IsNil() {
			return nil, nil
		}
		return rv.Elem().Interface(), nil
	}

	err := validateDynamicScalar(val)
	if err != nil {
		return nil, err
	}
	return val, nil
}

func isApprovedScalarType(rt reflect.Type) bool {
	if rt == bsonJSType || rt == bsonSymbolType {
		return false
	}
	switch rt.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	case reflect.Array:
		return rt == objectIDType
	case reflect.Struct:
		return rt == timeType || rt == decimal128Type || rt == timestampType
	default:
		return false
	}
}

func isUserStructType(rt reflect.Type) bool {
	if rt.Kind() != reflect.Struct {
		return false
	}
	if isApprovedScalarType(rt) || rt == bsonEType || rt == bsonRawValType {
		return false
	}
	return true
}

// marshalStructToSafeDoc verifies that v is a Go struct (or non-nil pointer to struct),
// marshals it via bson.Marshal into a bson.D, and recursively validates all BSON keys
// against operator injection (no $-prefixed keys).
func marshalStructToSafeDoc(v any, stripID bool) (bson.D, error) {
	if v == nil {
		return nil, fmt.Errorf("%w: struct value cannot be nil", ErrInvalidScalarValue)
	}

	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return nil, fmt.Errorf("%w: struct pointer cannot be nil", ErrInvalidScalarValue)
		}
		rv = rv.Elem()
	}

	if !isUserStructType(rv.Type()) {
		return nil, fmt.Errorf("%w: expected domain struct, got %s", ErrInvalidScalarValue, rv.Type().String())
	}

	raw, err := bson.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%w: marshal struct: %w", ErrInvalidScalarValue, err)
	}

	var doc bson.D
	err = bson.Unmarshal(raw, &doc)
	if err != nil {
		return nil, fmt.Errorf("%w: unmarshal struct doc: %w", ErrInvalidScalarValue, err)
	}

	cleaned, err := validateDecodedBSONDoc(doc, stripID)
	if err != nil {
		return nil, err
	}
	if len(cleaned) == 0 {
		return nil, fmt.Errorf("%w: struct produced an empty BSON document", ErrInvalidScalarValue)
	}
	return cleaned, nil
}

func validateDecodedBSONDoc(doc bson.D, stripID bool) (bson.D, error) {
	w := 0
	for i := range doc {
		elem := doc[i]
		if stripID && elem.Key == "_id" {
			continue
		}
		err := ValidateField(elem.Key)
		if err != nil {
			return nil, fmt.Errorf("struct field %q: %w", elem.Key, err)
		}
		err = validateDecodedBSONValue(elem.Value)
		if err != nil {
			return nil, fmt.Errorf("struct field %q: %w", elem.Key, err)
		}
		if w != i {
			doc[w] = elem
		}
		w++
	}
	return doc[:w], nil
}

func validateDecodedBSONValue(val any) error {
	switch v := val.(type) {
	case bson.JavaScript, bson.CodeWithScope, bson.Symbol:
		return fmt.Errorf("%w: BSON %T is not permitted in struct documents", ErrInvalidScalarValue, val)
	case bson.D:
		_, err := validateDecodedBSONDoc(v, false)
		return err
	case bson.A:
		for _, item := range v {
			err := validateDecodedBSONValue(item)
			if err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}
