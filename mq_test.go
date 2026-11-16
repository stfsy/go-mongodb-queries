package mq

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

type customStatus string
type customCount int

type testSeat struct {
	ID              string            `bson:"id,omitempty"`
	HWID            string            `bson:"hwid"`
	HWIDComponents  map[string]string `bson:"hwid_components,omitempty"`
	AppVersion      string            `bson:"app_version"`
	LastHeartbeatAt time.Time         `bson:"last_heartbeat_at"`
	LockedAt        *time.Time        `bson:"locked_at,omitempty"`
}

type testCreator struct {
	ID        string     `bson:"_id"`
	Email     string     `bson:"email"`
	Seats     []testSeat `bson:"seats,omitempty"`
	UpdatedAt time.Time  `bson:"updated_at"`
}

type testIDOnly struct {
	ID string `bson:"_id"`
}

type testInjectedStruct struct {
	Name string         `bson:"name"`
	Meta map[string]any `bson:"meta"`
}

type testNestedInjectedStruct struct {
	Items []map[string]any `bson:"items"`
}

type testUnmarshalableStruct struct {
	Fn func() `bson:"fn"`
}

func TestValidateFieldAndUpdateField(t *testing.T) {
	t.Parallel()

	validFields := []string{
		"_id",
		"workspace_id",
		"user.email",
		"seats.0.hwid",
		"a.b.c.d",
		"field$mid",
		strings.Repeat("a", MaxFieldNameLength),
	}

	for _, f := range validFields {
		require.NoError(t, ValidateField(f), "expected field %q to be valid", f)
		require.NoError(t, ValidateUpdateField(f), "expected update field %q to be valid", f)
	}

	// Positional paths are valid ONLY in ValidateUpdateField, NOT in ValidateField
	positionalPaths := []string{
		"seats.$.hwid",
		"seats.$.public_key",
		"seats.$",
		"seats.$[].locked_at",
		"seats.$[]",
		"seats.$[].items.$[].locked_at",
	}
	for _, p := range positionalPaths {
		require.ErrorIs(t, ValidateField(p), ErrInvalidFieldName, "filter ValidateField must reject positional path %q", p)
		require.NoError(t, ValidateUpdateField(p), "ValidateUpdateField must allow positional path %q", p)
	}

	invalidFields := []struct {
		name  string
		field string
	}{
		{name: "empty", field: ""},
		{name: "exceeds max length", field: strings.Repeat("a", MaxFieldNameLength+1)},
		{name: "starts with dollar", field: "$where"},
		{name: "root positional dollar", field: "$"},
		{name: "root positional array dollar", field: "$[]"},
		{name: "leading positional segment", field: "$.hwid"},
		{name: "multiple positional dollar tokens", field: "seats.$.items.$.hwid"},
		{name: "operator eq", field: "$eq"},
		{name: "nested segment starts with dollar", field: "user.$ne"},
		{name: "deep segment starts with dollar", field: "a.b.$gt.c"},
		{name: "leading dot", field: ".field"},
		{name: "trailing dot", field: "field."},
		{name: "consecutive dots", field: "user..email"},
		{name: "null byte", field: "user\x00name"},
		{name: "newline control char", field: "user\nname"},
		{name: "tab control char", field: "user\tname"},
		{name: "del control char", field: "user\x7fname"},
	}

	for _, tc := range invalidFields {
		t.Run(tc.name, func(t *testing.T) {
			require.ErrorIs(t, ValidateField(tc.field), ErrInvalidFieldName)
			require.ErrorIs(t, ValidateUpdateField(tc.field), ErrInvalidFieldName)
		})
	}

	// ValidateMapKey rejects dots as well
	require.NoError(t, ValidateMapKey("cpu_model"))
	require.ErrorIs(t, ValidateMapKey("cpu.model"), ErrInvalidFieldName)
	require.ErrorIs(t, ValidateMapKey("$gt"), ErrInvalidFieldName)
}

func TestEmptyAndMatchAllFilter(t *testing.T) {
	t.Parallel()

	var empty Filter
	doc, err := empty.Build()
	require.ErrorIs(t, err, ErrEmptyFilter)
	assert.Nil(t, doc)

	unknown := Filter{kind: 255}
	doc, err = unknown.Build()
	require.ErrorIs(t, err, ErrEmptyFilter)
	assert.Nil(t, doc)

	matchAll := MatchAll()
	doc, err = matchAll.Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{}, doc)
}

func TestOptionsNormalization(t *testing.T) {
	t.Parallel()

	def := DefaultOptions()
	assert.Equal(t, DefaultMaxDepth, def.MaxDepth)
	assert.Equal(t, DefaultMaxInElements, def.MaxInElements)
	assert.Equal(t, DefaultMaxPatternLength, def.MaxPatternLength)

	zeroNorm := (Options{}).normalized()
	assert.Equal(t, def, zeroNorm)

	negNorm := (Options{
		MaxDepth:         -5,
		MaxInElements:    -100,
		MaxPatternLength: -1,
	}).normalized()
	assert.Equal(t, def, negNorm)
}

func TestMatchStage(t *testing.T) {
	t.Parallel()

	stage, err := MatchStage(Eq("workspace_id", "ws_123"))
	require.NoError(t, err)
	assert.Equal(t, bson.D{{
		Key:   "$match",
		Value: bson.D{{Key: "workspace_id", Value: bson.D{{Key: "$eq", Value: "ws_123"}}}},
	}}, stage)

	_, err = MatchStage(Filter{})
	require.ErrorIs(t, err, ErrEmptyFilter)

	_, err = MatchStageWithOptions(In("status", []string{"a", "b", "c"}), Options{MaxInElements: 2})
	require.ErrorIs(t, err, ErrMaxElementsExceeded)
}

func TestScalarOperators_ExplicitOperatorWrapping(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	oid := bson.NewObjectID()
	dec, err := bson.ParseDecimal128("99.95")
	require.NoError(t, err)

	tests := []struct {
		name     string
		filter   Filter
		expected bson.D
	}{
		{
			name:     "Eq string",
			filter:   Eq("tenant_id", "ws_123"),
			expected: bson.D{{Key: "tenant_id", Value: bson.D{{Key: "$eq", Value: "ws_123"}}}},
		},
		{
			name:     "Eq custom string type",
			filter:   Eq("status", customStatus("active")),
			expected: bson.D{{Key: "status", Value: bson.D{{Key: "$eq", Value: customStatus("active")}}}},
		},
		{
			name:     "Ne bool",
			filter:   Ne("revoked", true),
			expected: bson.D{{Key: "revoked", Value: bson.D{{Key: "$ne", Value: true}}}},
		},
		{
			name:     "Gt int",
			filter:   Gt("seats_used", 5),
			expected: bson.D{{Key: "seats_used", Value: bson.D{{Key: "$gt", Value: 5}}}},
		},
		{
			name:     "Gte int64",
			filter:   Gte("version", int64(2)),
			expected: bson.D{{Key: "version", Value: bson.D{{Key: "$gte", Value: int64(2)}}}},
		},
		{
			name:     "Lt float64",
			filter:   Lt("score", 99.5),
			expected: bson.D{{Key: "score", Value: bson.D{{Key: "$lt", Value: 99.5}}}},
		},
		{
			name:     "Lte time.Time",
			filter:   Lte("expires_at", now),
			expected: bson.D{{Key: "expires_at", Value: bson.D{{Key: "$lte", Value: now}}}},
		},
		{
			name:     "Eq ObjectID",
			filter:   Eq("_id", oid),
			expected: bson.D{{Key: "_id", Value: bson.D{{Key: "$eq", Value: oid}}}},
		},
		{
			name:     "Eq Decimal128",
			filter:   Eq("price", dec),
			expected: bson.D{{Key: "price", Value: bson.D{{Key: "$eq", Value: dec}}}},
		},
		{
			name:     "Eq DateTime",
			filter:   Eq("dt", bson.DateTime(1700000000000)),
			expected: bson.D{{Key: "dt", Value: bson.D{{Key: "$eq", Value: bson.DateTime(1700000000000)}}}},
		},
		{
			name:     "EqNil",
			filter:   EqNil("deleted_at"),
			expected: bson.D{{Key: "deleted_at", Value: bson.D{{Key: "$eq", Value: nil}}}},
		},
		{
			name:     "NeNil",
			filter:   NeNil("activated_at"),
			expected: bson.D{{Key: "activated_at", Value: bson.D{{Key: "$ne", Value: nil}}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, buildErr := tc.filter.Build()
			require.NoError(t, buildErr)
			assert.Equal(t, tc.expected, doc)
		})
	}

	t.Run("rejects invalid field in scalar operator", func(t *testing.T) {
		_, buildErr := Eq("$where", "1 == 1").Build()
		require.ErrorIs(t, buildErr, ErrInvalidFieldName)
	})
}

func TestDynamicScalarValidation_NoSQLInjectionPrevention(t *testing.T) {
	t.Parallel()

	oid := bson.NewObjectID()
	dec, err := bson.ParseDecimal128("10.5")
	require.NoError(t, err)

	validDynamic := []any{
		"hello",
		true,
		int(1), int8(1), int16(1), int32(1), int64(1),
		uint(1), uint8(1), uint16(1), uint32(1), uint64(1),
		float32(1.5), float64(2.5),
		time.Now(),
		oid,
		dec,
		bson.DateTime(1000),
		bson.Timestamp{T: 1, I: 1},
		customStatus("active"),
		customCount(42),
	}

	for _, v := range validDynamic {
		doc, buildErr := EqAny("field", v).Build()
		require.NoError(t, buildErr)
		assert.Equal(t, bson.D{{Key: "field", Value: bson.D{{Key: "$eq", Value: v}}}}, doc)

		docNe, neErr := NeAny("field", v).Build()
		require.NoError(t, neErr)
		assert.Equal(t, bson.D{{Key: "field", Value: bson.D{{Key: "$ne", Value: v}}}}, docNe)
	}

	injectedPayloads := []struct {
		name string
		val  any
	}{
		{name: "nil value", val: nil},
		{name: "bson.M operator injection", val: bson.M{"$ne": nil}},
		{name: "map[string]any operator injection", val: map[string]any{"$gt": ""}},
		{name: "bson.D operator injection", val: bson.D{{Key: "$ne", Value: 1}}},
		{name: "slice of strings", val: []string{"a", "b"}},
		{name: "byte array (non-ObjectID)", val: [4]byte{1, 2, 3, 4}},
		{name: "arbitrary struct", val: struct{ Name string }{Name: "admin"}},
		{name: "pointer to string", val: new(string)},
		{name: "function", val: func() {}},
	}

	for _, tc := range injectedPayloads {
		t.Run(tc.name, func(t *testing.T) {
			_, eqErr := EqAny("password", tc.val).Build()
			require.ErrorIs(t, eqErr, ErrInvalidScalarValue)

			_, neErr := NeAny("password", tc.val).Build()
			require.ErrorIs(t, neErr, ErrInvalidScalarValue)
		})
	}
}

func TestSliceOperators_InNinAll(t *testing.T) {
	t.Parallel()

	t.Run("valid In, Nin, All", func(t *testing.T) {
		input := []string{"active", "trial"}
		fIn := In("status", input)
		input[0] = "mutated"

		docIn, err := fIn.Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "status", Value: bson.D{{Key: "$in", Value: []string{"active", "trial"}}}}}, docIn)

		docNin, err := Nin("role", []string{"banned"}).Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "role", Value: bson.D{{Key: "$nin", Value: []string{"banned"}}}}}, docNin)

		docAll, err := All("tags", []string{"go", "security"}).Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "tags", Value: bson.D{{Key: "$all", Value: []string{"go", "security"}}}}}, docAll)
	})

	t.Run("rejects empty slice", func(t *testing.T) {
		_, err := In("status", []string{}).Build()
		require.ErrorIs(t, err, ErrEmptySlice)

		_, err = Nin("status", []int(nil)).Build()
		require.ErrorIs(t, err, ErrEmptySlice)

		_, err = All("tags", []string{}).Build()
		require.ErrorIs(t, err, ErrEmptySlice)
	})

	t.Run("rejects slice exceeding MaxInElements", func(t *testing.T) {
		vals := []int{1, 2, 3, 4}
		_, err := In("ids", vals).BuildWithOptions(Options{MaxInElements: 3})
		require.ErrorIs(t, err, ErrMaxElementsExceeded)
	})

	t.Run("rejects invalid field name", func(t *testing.T) {
		_, err := In("$in", []string{"a"}).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})
}

func TestExistsOperators(t *testing.T) {
	t.Parallel()

	docExists, err := Exists("revoked_at").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{{Key: "revoked_at", Value: bson.D{{Key: "$exists", Value: true}}}}, docExists)

	docNotExists, err := NotExists("revoked_at").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{{Key: "revoked_at", Value: bson.D{{Key: "$exists", Value: false}}}}, docNotExists)

	docExistsNotNull, err := ExistsNotNull("activated_at").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{{
		Key: "$and",
		Value: bson.A{
			bson.D{{Key: "activated_at", Value: bson.D{{Key: "$exists", Value: true}}}},
			bson.D{{Key: "activated_at", Value: bson.D{{Key: "$ne", Value: nil}}}},
		},
	}}, docExistsNotNull)

	_, err = Exists("$bad").Build()
	require.ErrorIs(t, err, ErrInvalidFieldName)

	_, err = ExistsNotNull("bad..field").Build()
	require.ErrorIs(t, err, ErrInvalidFieldName)
}

func TestElemMatchSizeAndExprOperators(t *testing.T) {
	t.Parallel()

	t.Run("ElemMatch with embedded document filter", func(t *testing.T) {
		f := ElemMatch("seats", And(
			Eq("hwid", "sha256_abc"),
			Eq("active", true),
		))
		doc, err := f.Build()
		require.NoError(t, err)
		expected := bson.D{{
			Key: "seats",
			Value: bson.D{{
				Key: "$elemMatch",
				Value: bson.D{{
					Key: "$and",
					Value: bson.A{
						bson.D{{Key: "hwid", Value: bson.D{{Key: "$eq", Value: "sha256_abc"}}}},
						bson.D{{Key: "active", Value: bson.D{{Key: "$eq", Value: true}}}},
					},
				}},
			}},
		}}
		assert.Equal(t, expected, doc)
	})

	t.Run("ElemMatch rejects empty or MatchAll sub-filter", func(t *testing.T) {
		_, err := ElemMatch("seats", Filter{}).Build()
		require.ErrorIs(t, err, ErrEmptyElemMatch)

		_, err = ElemMatch("seats", MatchAll()).Build()
		require.ErrorIs(t, err, ErrEmptyElemMatch)

		_, err = (Filter{kind: kindElemMatchFilter, field: "seats"}).Build()
		require.ErrorIs(t, err, ErrEmptyElemMatch)
	})

	t.Run("ElemMatch propagates sub-filter error, validates field, and enforces MaxDepth", func(t *testing.T) {
		_, err := ElemMatch("seats", Eq("$bad", 1)).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = ElemMatch("$seats", Eq("hwid", "1")).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = ElemMatch("seats", EqAny("hwid", nil)).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = ElemMatch("seats", ElemMatch("devices", Eq("hwid", "1"))).BuildWithOptions(Options{MaxDepth: 1})
		require.ErrorIs(t, err, ErrMaxDepthExceeded)
	})

	t.Run("ScalarElemMatch with all operators", func(t *testing.T) {
		f := ScalarElemMatch("scores",
			OpGte(80),
			OpLt(90),
			OpEq(85),
			OpNe(82),
			OpGt(81),
			OpLte(89),
		)
		doc, err := f.Build()
		require.NoError(t, err)
		expected := bson.D{{
			Key: "scores",
			Value: bson.D{{
				Key: "$elemMatch",
				Value: bson.D{
					{Key: "$gte", Value: 80},
					{Key: "$lt", Value: 90},
					{Key: "$eq", Value: 85},
					{Key: "$ne", Value: 82},
					{Key: "$gt", Value: 81},
					{Key: "$lte", Value: 89},
				},
			}},
		}}
		assert.Equal(t, expected, doc)
	})

	t.Run("ScalarElemMatch rejects empty ops, bad field, and depth limit", func(t *testing.T) {
		_, err := ScalarElemMatch[int]("scores").Build()
		require.ErrorIs(t, err, ErrEmptyElemMatch)

		_, err = ScalarElemMatch("$scores", OpGt(1)).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = And(ScalarElemMatch("scores", OpGt(1))).BuildWithOptions(Options{MaxDepth: 1})
		require.ErrorIs(t, err, ErrMaxDepthExceeded)
	})

	t.Run("Size operator", func(t *testing.T) {
		doc, err := Size("seats", 3).Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "seats", Value: bson.D{{Key: "$size", Value: 3}}}}, doc)

		docZero, err := Size("seats", 0).Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "seats", Value: bson.D{{Key: "$size", Value: 0}}}}, docZero)

		_, err = Size("seats", -1).Build()
		require.ErrorIs(t, err, ErrNegativeArraySize)

		_, err = Size("$seats", 1).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})

	t.Run("ArraySizeLtField and constant ArraySize expressions", func(t *testing.T) {
		doc, err := ArraySizeLtField("seats", "license_seat_limit").Build()
		require.NoError(t, err)
		expected := bson.D{{
			Key: "$expr",
			Value: bson.D{{
				Key: "$lt",
				Value: bson.A{
					bson.D{{Key: "$size", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$seats", bson.A{}}}}}},
					"$license_seat_limit",
				},
			}},
		}}
		assert.Equal(t, expected, doc)

		_, err = ArraySizeLtField("$seats", "license_seat_limit").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = ArraySizeLtField("seats", "$license_seat_limit").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		for _, tc := range []struct {
			op     string
			filter Filter
		}{
			{op: "$lt", filter: ArraySizeLt("seats", 5)},
			{op: "$lte", filter: ArraySizeLte("seats", 5)},
			{op: "$gt", filter: ArraySizeGt("seats", 5)},
			{op: "$gte", filter: ArraySizeGte("seats", 5)},
		} {
			d, buildErr := tc.filter.Build()
			require.NoError(t, buildErr)
			assert.Equal(t, bson.D{{
				Key: "$expr",
				Value: bson.D{{
					Key: tc.op,
					Value: bson.A{
						bson.D{{Key: "$size", Value: bson.D{{Key: "$ifNull", Value: bson.A{"$seats", bson.A{}}}}}},
						5,
					},
				}},
			}}, d)
		}

		_, err = ArraySizeLt("seats", -1).Build()
		require.ErrorIs(t, err, ErrNegativeArraySize)

		_, err = ArraySizeLt("$seats", 1).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})

	t.Run("ExprFieldCompare operators", func(t *testing.T) {
		cases := []struct {
			op     string
			filter Filter
		}{
			{op: "$eq", filter: ExprEqFields("a", "b")},
			{op: "$ne", filter: ExprNeFields("a", "b")},
			{op: "$lt", filter: ExprLtFields("a", "b")},
			{op: "$lte", filter: ExprLteFields("a", "b")},
			{op: "$gt", filter: ExprGtFields("a", "b")},
			{op: "$gte", filter: ExprGteFields("a", "b")},
		}
		for _, tc := range cases {
			doc, err := tc.filter.Build()
			require.NoError(t, err)
			assert.Equal(t, bson.D{{
				Key:   "$expr",
				Value: bson.D{{Key: tc.op, Value: bson.A{"$a", "$b"}}},
			}}, doc)
		}

		_, err := ExprLtFields("$a", "b").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = ExprLtFields("a", "$b").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})
}

func TestSafeRegexBuilders(t *testing.T) {
	t.Parallel()

	untrusted := "admin.*(a+)+$[0-9]?"
	escaped := `admin\.\*\(a\+\)\+\$\[0-9\]\?`

	tests := []struct {
		name     string
		filter   Filter
		expected bson.D
	}{
		{
			name:   "Prefix",
			filter: Prefix("username", untrusted),
			expected: bson.D{{
				Key:   "username",
				Value: bson.D{{Key: "$regex", Value: "^" + escaped}},
			}},
		},
		{
			name:   "PrefixCaseInsensitive",
			filter: PrefixCaseInsensitive("username", untrusted),
			expected: bson.D{{
				Key:   "username",
				Value: bson.D{{Key: "$regex", Value: "^" + escaped}, {Key: "$options", Value: "i"}},
			}},
		},
		{
			name:   "Suffix",
			filter: Suffix("domain", ".example.com"),
			expected: bson.D{{
				Key:   "domain",
				Value: bson.D{{Key: "$regex", Value: `\.example\.com$`}},
			}},
		},
		{
			name:   "SuffixCaseInsensitive",
			filter: SuffixCaseInsensitive("domain", ".example.com"),
			expected: bson.D{{
				Key:   "domain",
				Value: bson.D{{Key: "$regex", Value: `\.example\.com$`}, {Key: "$options", Value: "i"}},
			}},
		},
		{
			name:   "Contains",
			filter: Contains("bio", "c++"),
			expected: bson.D{{
				Key:   "bio",
				Value: bson.D{{Key: "$regex", Value: `c\+\+`}},
			}},
		},
		{
			name:   "Contains plain without metacharacters",
			filter: Contains("bio", "golang"),
			expected: bson.D{{
				Key:   "bio",
				Value: bson.D{{Key: "$regex", Value: "golang"}},
			}},
		},
		{
			name:   "ContainsCaseInsensitive",
			filter: ContainsCaseInsensitive("bio", "c++"),
			expected: bson.D{{
				Key:   "bio",
				Value: bson.D{{Key: "$regex", Value: `c\+\+`}, {Key: "$options", Value: "i"}},
			}},
		},
		{
			name:   "ExactCaseInsensitive",
			filter: ExactCaseInsensitive("email", "User+1@Example.com"),
			expected: bson.D{{
				Key:   "email",
				Value: bson.D{{Key: "$regex", Value: `^User\+1@Example\.com$`}, {Key: "$options", Value: "i"}},
			}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := tc.filter.Build()
			require.NoError(t, err)
			assert.Equal(t, tc.expected, doc)
		})
	}

	t.Run("rejects empty pattern, null byte, pattern too long, and bad field", func(t *testing.T) {
		_, err := Prefix("username", "").Build()
		require.ErrorIs(t, err, ErrInvalidPattern)

		_, err = Contains("username", "abc\x00def").Build()
		require.ErrorIs(t, err, ErrInvalidPattern)

		_, err = Prefix("username", "123456").BuildWithOptions(Options{MaxPatternLength: 5})
		require.ErrorIs(t, err, ErrPatternTooLong)

		_, err = Prefix("$username", "abc").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})
}

func TestLogicalOperatorsAndDepthGuard(t *testing.T) {
	t.Parallel()

	t.Run("And, Or, Nor, Not", func(t *testing.T) {
		f := And(
			Eq("workspace_id", "ws_1"),
			Or(
				Eq("status", "active"),
				Eq("status", "trial"),
			),
			Nor(
				Eq("suspended", true),
			),
			Not("failed_attempts", OpGte(5)),
		)

		doc, err := f.Build()
		require.NoError(t, err)
		expected := bson.D{{
			Key: "$and",
			Value: bson.A{
				bson.D{{Key: "workspace_id", Value: bson.D{{Key: "$eq", Value: "ws_1"}}}},
				bson.D{{
					Key: "$or",
					Value: bson.A{
						bson.D{{Key: "status", Value: bson.D{{Key: "$eq", Value: "active"}}}},
						bson.D{{Key: "status", Value: bson.D{{Key: "$eq", Value: "trial"}}}},
					},
				}},
				bson.D{{
					Key: "$nor",
					Value: bson.A{
						bson.D{{Key: "suspended", Value: bson.D{{Key: "$eq", Value: true}}}},
					},
				}},
				bson.D{{
					Key: "failed_attempts",
					Value: bson.D{{
						Key:   "$not",
						Value: bson.D{{Key: "$gte", Value: 5}},
					}},
				}},
			},
		}}
		assert.Equal(t, expected, doc)
	})

	t.Run("rejects empty logical groups and empty children", func(t *testing.T) {
		_, err := And().Build()
		require.ErrorIs(t, err, ErrEmptyLogicalGroup)

		_, err = Or().Build()
		require.ErrorIs(t, err, ErrEmptyLogicalGroup)

		_, err = Nor().Build()
		require.ErrorIs(t, err, ErrEmptyLogicalGroup)

		_, err = And(Eq("a", 1), Filter{}).Build()
		require.ErrorIs(t, err, ErrEmptyFilter)

		_, err = And(EqAny("a", nil)).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = And(Eq("a", 1), Eq("$bad", 2)).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})

	t.Run("enforces MaxDepth on nested logical trees and Not", func(t *testing.T) {
		nested := Eq("a", 1)
		for i := 0; i < 12; i++ {
			nested = And(nested)
		}
		_, err := nested.Build()
		require.ErrorIs(t, err, ErrMaxDepthExceeded)

		_, err = And(Not("a", OpEq(1))).BuildWithOptions(Options{MaxDepth: 1})
		require.ErrorIs(t, err, ErrMaxDepthExceeded)

		_, err = Not("$bad", OpEq(1)).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)
	})
}

func TestUpdateBuilder(t *testing.T) {
	t.Parallel()

	t.Run("valid multi-operator update including positional paths and pointer scalars", func(t *testing.T) {
		limit := 5
		var nilTime *time.Time
		oid := bson.NewObjectID()

		u := NewUpdate().
			Set("status", "active").
			Set("seats.$.app_version", "2.1.0").
			Set("seats.$[].locked_at", nilTime).
			Set("license_seat_limit", &limit).
			Set("last_oid", &oid).
			SetNil("revoked_at").
			SetOnInsert("created_by", "system").
			SetOnInsert("initial_limit", &limit).
			Unset("temp_token").
			Inc("seats_used", 1).
			Min("min_latency", 10).
			Max("max_latency", 250).
			CurrentDate("updated_at").
			AddToSet("tags", "verified").
			Push("audit_ids", "evt_1").
			Pull("old_tokens", "tok_expired").
			PullFilter("revoked_seats", Eq("hwid", "dead_beef"))

		doc, err := u.Build()
		require.NoError(t, err)

		expected := bson.D{
			{Key: "$set", Value: bson.D{
				{Key: "status", Value: "active"},
				{Key: "seats.$.app_version", Value: "2.1.0"},
				{Key: "seats.$[].locked_at", Value: nil},
				{Key: "license_seat_limit", Value: 5},
				{Key: "last_oid", Value: oid},
				{Key: "revoked_at", Value: nil},
			}},
			{Key: "$setOnInsert", Value: bson.D{
				{Key: "created_by", Value: "system"},
				{Key: "initial_limit", Value: 5},
			}},
			{Key: "$unset", Value: bson.D{
				{Key: "temp_token", Value: ""},
			}},
			{Key: "$inc", Value: bson.D{
				{Key: "seats_used", Value: 1},
			}},
			{Key: "$min", Value: bson.D{
				{Key: "min_latency", Value: 10},
			}},
			{Key: "$max", Value: bson.D{
				{Key: "max_latency", Value: 250},
			}},
			{Key: "$currentDate", Value: bson.D{
				{Key: "updated_at", Value: true},
			}},
			{Key: "$addToSet", Value: bson.D{
				{Key: "tags", Value: "verified"},
			}},
			{Key: "$push", Value: bson.D{
				{Key: "audit_ids", Value: "evt_1"},
			}},
			{Key: "$pull", Value: bson.D{
				{Key: "old_tokens", Value: bson.D{{Key: "$eq", Value: "tok_expired"}}},
				{Key: "revoked_seats", Value: bson.D{{Key: "hwid", Value: bson.D{{Key: "$eq", Value: "dead_beef"}}}}},
			}},
		}
		assert.Equal(t, expected, doc)
	})

	t.Run("standalone shorthand constructors and custom numeric", func(t *testing.T) {
		docSet, err := Set("name", "Seatling").Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "$set", Value: bson.D{{Key: "name", Value: "Seatling"}}}}, docSet)

		docUnset, err := Unset("legacy").Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "$unset", Value: bson.D{{Key: "legacy", Value: ""}}}}, docUnset)

		docInc, err := Inc("count", customCount(5)).Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "$inc", Value: bson.D{{Key: "count", Value: customCount(5)}}}}, docInc)

		docIncDyn, err := NewUpdate().Inc("count", customCount(3)).Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "$inc", Value: bson.D{{Key: "count", Value: customCount(3)}}}}, docIncDyn)

		docPush, err := Push("items", "x").Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "$push", Value: bson.D{{Key: "items", Value: "x"}}}}, docPush)

		docPull, err := Pull("items", "x").Build()
		require.NoError(t, err)
		assert.Equal(t, bson.D{{Key: "$pull", Value: bson.D{{Key: "items", Value: bson.D{{Key: "$eq", Value: "x"}}}}}}, docPull)
	})

	t.Run("PushStruct, SetStruct, SetDocumentStruct, SetOnInsertDocumentStruct, SetEmptySlice, SetSlice, SetStringMap", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Millisecond)
		seat := testSeat{
			ID:              "seat_1",
			HWID:            "sha256_123",
			HWIDComponents:  map[string]string{"cpu": "m3", "os": "macos"},
			AppVersion:      "1.0.0",
			LastHeartbeatAt: now,
		}

		// PushStruct and SetStruct
		doc, err := NewUpdate().
			PushStruct("seats", seat).
			SetStruct("primary_seat", &seat).
			SetEmptySlice("old_seats").
			SetSlice("tags", []string{"a", "b"}).
			SetSlice("oids", []bson.ObjectID{bson.NewObjectID()}).
			SetSlice("backup_seats", []testSeat{seat}).
			SetSlice("pointer_seats", []*testSeat{&seat}).
			SetSlice("empty_typed_seats", []testSeat{}).
			SetStringMap("active_seats.$.hwid_components", map[string]string{"disk": "nvme0", "board": "apple"}).
			Build()
		require.NoError(t, err)
		assert.Len(t, doc, 2) // $push and $set

		// SetDocumentStruct strips _id
		creator := testCreator{
			ID:        "ws_123",
			Email:     "creator@example.com",
			Seats:     []testSeat{seat},
			UpdatedAt: now,
		}
		docSetStruct, err := NewUpdate().SetDocumentStruct(&creator).Build()
		require.NoError(t, err)
		setElems := docSetStruct[0].Value.(bson.D)
		for _, e := range setElems {
			assert.NotEqual(t, "_id", e.Key, "SetDocumentStruct must strip _id")
		}

		// SetOnInsertDocumentStruct preserves _id
		docInsertStruct, err := NewUpdate().SetOnInsertDocumentStruct(creator).Build()
		require.NoError(t, err)
		insertElems := docInsertStruct[0].Value.(bson.D)
		assert.Equal(t, "_id", insertElems[0].Key)
		assert.Equal(t, "ws_123", insertElems[0].Value)
	})

	t.Run("Struct, Slice, and Map error and injection cases", func(t *testing.T) {
		// Reject nil, non-struct, bson.M, bson.D, time.Time, bson.E in PushStruct / SetStruct / SetDocumentStruct
		var nilSeat *testSeat
		for _, badVal := range []any{
			nil,
			nilSeat,
			"string",
			bson.M{"$ne": 1},
			bson.D{{Key: "a", Value: 1}},
			time.Now(),
			bson.E{Key: "a", Value: 1},
			bson.RawValue{},
			testIDOnly{ID: "only_id"},              // becomes empty when _id is stripped in SetDocumentStruct
			testUnmarshalableStruct{Fn: func() {}}, // fails bson.Marshal
		} {
			_, err := NewUpdate().SetDocumentStruct(badVal).Build()
			require.ErrorIs(t, err, ErrInvalidScalarValue)
		}

		_, err := NewUpdate().PushStruct("seats", bson.M{"$gt": 1}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetStruct("seat", bson.D{{Key: "a", Value: 1}}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetOnInsertDocumentStruct("not_a_struct").Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		// Struct containing a nested map with $-prefixed key is rejected!
		injected := testInjectedStruct{
			Name: "bad",
			Meta: map[string]any{"$where": "1 == 1"},
		}
		_, err = NewUpdate().PushStruct("items", injected).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		nestedInjected := testNestedInjectedStruct{
			Items: []map[string]any{{"$gt": 1}},
		}
		_, err = NewUpdate().SetStruct("nested", nestedInjected).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		// SetSlice error cases
		_, err = NewUpdate().SetSlice("seats", nil).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("seats", bson.D{{Key: "a", Value: 1}}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("seats", bson.A{"a"}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("seats", bson.Raw{0x05, 0x00, 0x00, 0x00, 0x00}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("seats", []bson.M{{"$ne": 1}}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("seats", []*testSeat{nil}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("seats", []testInjectedStruct{injected}).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		// SetStringMap error cases
		_, err = NewUpdate().SetStringMap("components", map[string]string{"$gt": "x"}).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = NewUpdate().SetStringMap("components", map[string]string{"a.b": "x"}).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		// Pointer to non-scalar in Set rejected
		_, err = NewUpdate().Set("a", &testIDOnly{ID: "1"}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)
	})

	t.Run("Update error cases", func(t *testing.T) {
		var nilUpdate *Update
		_, err := nilUpdate.Build()
		require.ErrorIs(t, err, ErrEmptyUpdate)

		_, err = NewUpdate().Build()
		require.ErrorIs(t, err, ErrEmptyUpdate)

		// Duplicate field across same or different operators
		_, err = NewUpdate().Set("status", "a").Set("status", "b").Build()
		require.ErrorIs(t, err, ErrDuplicateUpdateField)

		_, err = NewUpdate().Set("status", "a").Unset("status").Build()
		require.ErrorIs(t, err, ErrDuplicateUpdateField)

		// Hierarchical parent/child field conflicts
		_, err = NewUpdate().SetStruct("user", testSeat{HWID: "1"}).Set("user.hwid", "2").Build()
		require.ErrorIs(t, err, ErrDuplicateUpdateField)

		_, err = NewUpdate().Set("user.hwid", "2").SetStruct("user", testSeat{HWID: "1"}).Build()
		require.ErrorIs(t, err, ErrDuplicateUpdateField)

		_, err = NewUpdate().PushStruct("seats", testSeat{HWID: "1"}).Set("seats.$.hwid", "2").Build()
		require.ErrorIs(t, err, ErrDuplicateUpdateField)

		// All scalar pointer types (both non-nil and typed nil) in Set
		sVal := "str"
		bVal := true
		i8Val := int8(8)
		i16Val := int16(16)
		i32Val := int32(32)
		i64Val := int64(64)
		uVal := uint(1)
		u8Val := uint8(8)
		u16Val := uint16(16)
		u32Val := uint32(32)
		u64Val := uint64(64)
		f32Val := float32(1.5)
		f64Val := float64(2.5)
		tVal := time.Now().UTC()
		decVal, decErr := bson.ParseDecimal128("1.23")
		require.NoError(t, decErr)
		dtVal := bson.DateTime(100)
		tsVal := bson.Timestamp{T: 1, I: 2}
		csVal := customStatus("active")

		var (
			nilStr *string
			nilB   *bool
			nilI   *int
			nilI8  *int8
			nilI16 *int16
			nilI32 *int32
			nilI64 *int64
			nilU   *uint
			nilU8  *uint8
			nilU16 *uint16
			nilU32 *uint32
			nilU64 *uint64
			nilF32 *float32
			nilF64 *float64
			nilOid *bson.ObjectID
			nilDec *bson.Decimal128
			nilDt  *bson.DateTime
			nilTs  *bson.Timestamp
			nilCs  *customStatus
		)

		ptrDoc, err := (&Update{}).
			Set("s", &sVal).Set("b", &bVal).
			Set("i8", &i8Val).Set("i16", &i16Val).Set("i32", &i32Val).Set("i64", &i64Val).
			Set("u", &uVal).Set("u8", &u8Val).Set("u16", &u16Val).Set("u32", &u32Val).Set("u64", &u64Val).
			Set("f32", &f32Val).Set("f64", &f64Val).
			Set("t", &tVal).Set("dec", &decVal).Set("dt", &dtVal).Set("ts", &tsVal).Set("cs", &csVal).
			Set("ns", nilStr).Set("nb", nilB).Set("ni", nilI).
			Set("ni8", nilI8).Set("ni16", nilI16).Set("ni32", nilI32).Set("ni64", nilI64).
			Set("nu", nilU).Set("nu8", nilU8).Set("nu16", nilU16).Set("nu32", nilU32).Set("nu64", nilU64).
			Set("nf32", nilF32).Set("nf64", nilF64).
			Set("noid", nilOid).Set("ndec", nilDec).Set("ndt", nilDt).Set("nts", nilTs).Set("ncs", nilCs).
			PullFilter("seats", Eq("hwid", "x")).
			Build()
		require.NoError(t, err)
		assert.Len(t, ptrDoc, 2)

		// Invalid field name
		_, err = NewUpdate().Set("$set", "a").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = NewUpdate().Set("seats.$gt.hwid", "a").Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		// Invalid scalar values across methods
		_, err = NewUpdate().Set("a", bson.M{"$gt": 1}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetOnInsert("a", nil).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Inc("a", "not-a-number").Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Inc("a", nil).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Min("a", []int{1}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Max("a", []int{1}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().AddToSet("a", map[string]int{}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Push("a", nil).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Pull("a", nil).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		// Sticky error propagation across chained calls (including SetNil, SetEmptySlice, Unset, CurrentDate via appendOp)
		_, err = NewUpdate().
			Set("a", nil).
			Set("b", "ok").
			SetNil("b_nil").
			SetEmptySlice("b_empty").
			SetOnInsert("c", "ok").
			Unset("c_unset").
			SetStruct("d", testSeat{HWID: "1"}).
			SetDocumentStruct(testCreator{ID: "1", Email: "a@b.c"}).
			SetOnInsertDocumentStruct(testCreator{ID: "1", Email: "a@b.c"}).
			SetSlice("e", []string{"a"}).
			SetStringMap("f", map[string]string{"k": "v"}).
			Inc("g", 1).
			Min("h", 1).
			Max("i", 1).
			CurrentDate("i_date").
			AddToSet("j", "ok").
			Push("k", "ok").
			PushStruct("l", testSeat{HWID: "1"}).
			Pull("m", "ok").
			PullFilter("n", Eq("x", 1)).
			Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		// PullFilter with MatchAll, empty Filter, or exceeding Options rejected
		_, err = NewUpdate().PullFilter("seats", MatchAll()).Build()
		require.ErrorIs(t, err, ErrEmptyElemMatch)

		_, err = NewUpdate().PullFilter("seats", Filter{}).Build()
		require.ErrorIs(t, err, ErrEmptyElemMatch)

		_, err = NewUpdate().PullFilter("seats", EqAny("hwid", nil)).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().PullFilter("seats", Eq("$bad", 1)).Build()
		require.ErrorIs(t, err, ErrInvalidFieldName)

		_, err = NewUpdate().PullFilter("seats", And(Eq("hwid", "1"))).BuildWithOptions(Options{MaxDepth: 1})
		require.ErrorIs(t, err, ErrMaxDepthExceeded)
	})
}

func TestSortBuilder(t *testing.T) {
	t.Parallel()

	doc, err := SortDesc("created_at").Asc("_id").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{
		{Key: "created_at", Value: int32(-1)},
		{Key: "_id", Value: int32(1)},
	}, doc)

	docAsc, err := SortAsc("name").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{{Key: "name", Value: int32(1)}}, docAsc)

	zeroAsc, err := (&Sort{}).Asc("a").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{{Key: "a", Value: int32(1)}}, zeroAsc)

	zeroDesc, err := (&Sort{}).Desc("a").Build()
	require.NoError(t, err)
	assert.Equal(t, bson.D{{Key: "a", Value: int32(-1)}}, zeroDesc)

	var nilSort *Sort
	_, err = nilSort.Build()
	require.ErrorIs(t, err, ErrEmptySort)

	_, err = NewSort().Build()
	require.ErrorIs(t, err, ErrEmptySort)

	_, err = SortAsc("$natural").Build()
	require.ErrorIs(t, err, ErrInvalidFieldName)

	_, err = SortAsc("created_at").Desc("created_at").Build()
	require.ErrorIs(t, err, ErrDuplicateSortField)
}

func TestConcurrencyAndImmutability(t *testing.T) {
	t.Parallel()

	sharedFilter := And(
		Eq("workspace_id", "ws_123"),
		In("status", []string{"active", "trial"}),
		ExistsNotNull("activated_at"),
		ElemMatch("seats", And(
			Eq("hwid", "sha256_abc"),
			Not("revoked", OpEq(true)),
		)),
		ArraySizeLtField("seats", "license_seat_limit"),
		PrefixCaseInsensitive("email", "user+1@"),
	)

	sharedUpdate := NewUpdate().
		Set("status", "active").
		Set("seats.$.app_version", "2.0.0").
		Inc("seats_used", 1).
		PullFilter("revoked_seats", Eq("hwid", "revoked_hwid"))

	sharedSort := SortDesc("created_at").Asc("_id")

	expectedFilterDoc, err := sharedFilter.Build()
	require.NoError(t, err)
	expectedUpdateDoc, err := sharedUpdate.Build()
	require.NoError(t, err)
	expectedSortDoc, err := sharedSort.Build()
	require.NoError(t, err)

	const goroutines = 32
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for range goroutines {
		go func() {
			defer wg.Done()
			for range 20 {
				fDoc, fErr := sharedFilter.Build()
				assert.NoError(t, fErr)
				assert.Equal(t, expectedFilterDoc, fDoc)

				uDoc, uErr := sharedUpdate.Build()
				assert.NoError(t, uErr)
				assert.Equal(t, expectedUpdateDoc, uDoc)

				sDoc, sErr := sharedSort.Build()
				assert.NoError(t, sErr)
				assert.Equal(t, expectedSortDoc, sDoc)
			}
		}()
	}

	wg.Wait()
}

func TestSecurityAuditRegressions(t *testing.T) {
	t.Parallel()

	t.Run("MatchAll rejected inside logical combinators, ElemMatch, and PullFilter", func(t *testing.T) {
		t.Parallel()

		_, err := And(MatchAll()).Build()
		require.ErrorIs(t, err, ErrEmptyFilter)

		_, err = Or(Eq("tenant_id", "t1"), MatchAll()).Build()
		require.ErrorIs(t, err, ErrEmptyFilter)

		_, err = Nor(MatchAll()).Build()
		require.ErrorIs(t, err, ErrEmptyFilter)

		_, err = ElemMatch("seats", Or(Eq("active", true), MatchAll())).Build()
		require.ErrorIs(t, err, ErrEmptyFilter)

		_, err = NewUpdate().PullFilter("seats", Or(Eq("active", true), MatchAll())).Build()
		require.ErrorIs(t, err, ErrEmptyFilter)
	})

	t.Run("zero-value ScalarOp rejected in Not and ScalarElemMatch", func(t *testing.T) {
		t.Parallel()

		var zeroOp ScalarOp[string]
		_, err := Not("status", zeroOp).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = ScalarElemMatch("tags", zeroOp).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)
	})

	t.Run("ExistsNotNull enforces MaxDepth", func(t *testing.T) {
		t.Parallel()

		_, err := And(ExistsNotNull("activated_at")).BuildWithOptions(Options{MaxDepth: 1})
		require.ErrorIs(t, err, ErrMaxDepthExceeded)

		doc, err := And(ExistsNotNull("activated_at")).BuildWithOptions(Options{MaxDepth: 2})
		require.NoError(t, err)
		assert.NotEmpty(t, doc)
	})

	t.Run("BSON JavaScript, Symbol, and CodeWithScope rejected in scalars and structs", func(t *testing.T) {
		t.Parallel()

		_, err := EqAny("fn", bson.JavaScript("return true;")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = EqAny("sym", bson.Symbol("sym")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Set("fn", bson.JavaScript("return true;")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		jsVal := bson.JavaScript("return true;")
		_, err = NewUpdate().Set("fn", &jsVal).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().Set("sym", bson.Symbol("sym")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("fns", []bson.JavaScript{"return 1;"}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		type codeDoc struct {
			Fn bson.CodeWithScope `bson:"fn"`
		}
		_, err = NewUpdate().SetStruct("payload", codeDoc{
			Fn: bson.CodeWithScope{
				Code:  "return true;",
				Scope: bson.D{{Key: "$where", Value: "1"}},
			},
		}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		type jsDoc struct {
			Fn bson.JavaScript `bson:"fn"`
		}
		_, err = NewUpdate().SetDocumentStruct(jsDoc{Fn: "return 1;"}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)
	})

	t.Run("generic Scalar entry points reject bson.JavaScript and bson.Symbol", func(t *testing.T) {
		t.Parallel()

		_, err := Eq("fn", bson.JavaScript("return 1;")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = Ne("sym", bson.Symbol("sym")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = In("fn", []bson.JavaScript{"return 1;"}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = Nin("sym", []bson.Symbol{"sym"}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = Not("fn", OpEq(bson.JavaScript("return 1;"))).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = ScalarElemMatch("fns", OpEq(bson.JavaScript("return 1;"))).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = Set("fn", bson.JavaScript("return 1;")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = Push("fns", bson.Symbol("sym")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = Pull("fns", bson.JavaScript("return 1;")).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)
	})

	t.Run("unsafe BSON types rejected in structs and slices", func(t *testing.T) {
		t.Parallel()

		type regexDoc struct {
			R bson.Regex `bson:"r"`
		}
		_, err := NewUpdate().SetStruct("payload", regexDoc{R: bson.Regex{Pattern: ".*", Options: "i"}}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		type sentinelDoc struct {
			Min bson.MinKey `bson:"min"`
		}
		_, err = NewUpdate().SetStruct("payload", sentinelDoc{}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		type maxDoc struct {
			Max bson.MaxKey `bson:"max"`
		}
		_, err = NewUpdate().SetStruct("payload", maxDoc{}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		type undefDoc struct {
			U bson.Undefined `bson:"u"`
		}
		_, err = NewUpdate().SetStruct("payload", undefDoc{}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		type dbPtrDoc struct {
			P bson.DBPointer `bson:"p"`
		}
		_, err = NewUpdate().SetStruct("payload", dbPtrDoc{P: bson.DBPointer{DB: "db", Pointer: bson.NewObjectID()}}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("raw_arr", bson.RawArray{0x05, 0x00, 0x00, 0x00, 0x00}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)

		_, err = NewUpdate().SetSlice("raw_elem", bson.RawElement{0x00}).Build()
		require.ErrorIs(t, err, ErrInvalidScalarValue)
	})
}
