package mq

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

type sortEntry struct {
	field     string
	direction int32
}

// Sort is a security-hardened builder for MongoDB sort documents.
// It validates every field path against operator injection and strictly emits int32(1) or int32(-1),
// preventing $meta or arbitrary object injection in sort specifications.
type Sort struct {
	entries   []sortEntry
	bootstrap [2]sortEntry
}

// NewSort creates a new empty Sort builder.
func NewSort() *Sort {
	s := &Sort{}
	s.entries = s.bootstrap[:0]
	return s
}

// Asc appends an ascending sort specification (1) for the given field.
func (s *Sort) Asc(field string) *Sort {
	if s.entries == nil {
		s.entries = s.bootstrap[:0]
	}
	s.entries = append(s.entries, sortEntry{
		field:     field,
		direction: 1,
	})
	return s
}

// Desc appends a descending sort specification (-1) for the given field.
func (s *Sort) Desc(field string) *Sort {
	if s.entries == nil {
		s.entries = s.bootstrap[:0]
	}
	s.entries = append(s.entries, sortEntry{
		field:     field,
		direction: -1,
	})
	return s
}

// Build validates all sort field paths and compiles the Sort specification into an ordered bson.D document.
// Returns ErrEmptySort if no sort fields were added, ErrInvalidFieldName if any field path is unsafe,
// or ErrDuplicateSortField if the same field appears more than once.
func (s *Sort) Build() (bson.D, error) {
	if s == nil || len(s.entries) == 0 {
		return nil, ErrEmptySort
	}

	entries := s.entries
	n := len(entries)
	doc := make(bson.D, 0, n)

	for i := 0; i < n; i++ {
		field := entries[i].field
		err := ValidateField(field)
		if err != nil {
			return nil, err
		}
		for j := 0; j < i; j++ {
			if entries[j].field == field {
				return nil, fmt.Errorf("%w: field %q", ErrDuplicateSortField, field)
			}
		}
		var dirVal any = int32(-1)
		if entries[i].direction == 1 {
			dirVal = int32(1)
		}
		doc = append(doc, bson.E{
			Key:   field,
			Value: dirVal,
		})
	}

	return doc, nil
}

// SortAsc creates a new Sort builder initialized with an ascending sort on field.
func SortAsc(field string) *Sort {
	return NewSort().Asc(field)
}

// SortDesc creates a new Sort builder initialized with a descending sort on field.
func SortDesc(field string) *Sort {
	return NewSort().Desc(field)
}
