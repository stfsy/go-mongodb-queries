# Project Guidelines

Security-hardened, generic Go query, update, sort, and aggregation stage builder for the official MongoDB Go Driver v2 (`go.mongodb.org/mongo-driver/v2/bson`).

## Architecture

- **Core Module (`github.com/stfsy/go-mongodb-queries`, `package mq`)**:
  - Safe BSON filter AST & builder (`Filter`, `Eq`, `Ne`, `Gt`, `Gte`, `Lt`, `Lte`, `EqNil`, `NeNil`, `EqAny`, `NeAny`, `In`, `Nin`, `All`, `Exists`, `NotExists`, `ExistsNotNull`, `ElemMatch`, `ScalarElemMatch`, `Size`, `ArraySizeLtField`, `ArraySizeLt`, `ArraySizeLte`, `ArraySizeGt`, `ArraySizeGte`, `ExprEqFields`, `ExprNeFields`, `ExprLtFields`, `ExprLteFields`, `ExprGtFields`, `ExprGteFields`, `Prefix`, `PrefixCaseInsensitive`, `Suffix`, `SuffixCaseInsensitive`, `Contains`, `ContainsCaseInsensitive`, `ExactCaseInsensitive`, `And`, `Or`, `Nor`, `Not`, `MatchAll`, `MatchStage`).
  - Safe Update document builder (`Update`, `Set`, `SetNil`, `SetOnInsert`, `SetStruct`, `SetDocumentStruct`, `SetOnInsertDocumentStruct`, `SetEmptySlice`, `SetSlice`, `SetStringMap`, `Unset`, `Inc`, `Min`, `Max`, `CurrentDate`, `AddToSet`, `Push`, `PushStruct`, `Pull`, `PullFilter`).
  - Safe Sort builder (`Sort`, `SortAsc`, `SortDesc`).
  - Field path validators (`ValidateField`, `ValidateUpdateField`, `ValidateMapKey`) & scalar/struct type guardrails.

For detailed security architecture and rules, see [README.md](README.md).

## Build, Test, and Lint

- **Run Tests**:
  - Script: `./test.sh`
  - Command: `go test -v -race -cover -timeout 60s ./...`
- **Run Benchmarks**:
  - `go test -benchmem -run=NONE -bench="Benchmark" .`
- **Run Linter**:
  - Script (Docker): `./lint.sh`
  - Local CLI: `golangci-lint run -v`

## Conventions & Security Invariants

1. **Explicit Operator Wrapping (`$eq`)**: Never emit bare `{"field": value}` filters. All scalar comparisons must emit explicit operator documents (`{"field": {"$eq": value}}`) to neutralize NoSQL operator injection.
2. **Generic Scalar Constraint + Runtime Validation**: Scalar operators only accept approved primitive/scalar types (`string`, `bool`, numeric types, `time.Time`, `bson.ObjectID`, `bson.Decimal128`, `bson.DateTime`, `bson.Timestamp`). Self-constructed BSON maps/containers (`bson.M`, `bson.D`, `bson.A`, `bson.E`, `bson.Raw`, `map[string]any`), slices, or arbitrary structs are rejected in scalar filters.
3. **Strict Field Name Validation**:
   - Filter and Sort fields (`ValidateField`) must never be empty, exceed 255 bytes, contain null/control characters (`< 0x20` or `0x7f`), start with `$`, or contain dot-path segments that are empty (`..`) or start with `$` (`a.$b`).
   - Update fields (`ValidateUpdateField`) additionally allow positional array update tokens (`"$"`, `"$[]"`) after the root array segment (e.g., `"seats.$.hwid"`), while blocking `$` at segment 0, multiple `$` positional tokens in a single path, and any `$operator` segment.
4. **Ban on Server-Side Code & Raw Operators**: `$where`, `$function`, `$accumulator`, and raw unvalidated `bson.M`/`bson.D` inputs are strictly prohibited.
5. **Safe Regex Construction**: Never expose raw `$regex` patterns. Use `Prefix`, `Suffix`, `Contains`, and `ExactCaseInsensitive` (and case-insensitive variants), which automatically apply `regexp.QuoteMeta` and enforce pattern length limits to prevent ReDoS.
6. **Non-Empty Bounded Set Operations (`$in`, `$nin`, `$all`)**: Empty slices return `ErrEmptySlice` on `.Build()`. Slices exceeding `MaxInElements` (default 1000) return `ErrMaxElementsExceeded`.
7. **Bounded Logical Combinators (`$and`, `$or`, `$nor`)**: Logical groups require at least one child expression (`ErrEmptyLogicalGroup`) and enforce a maximum nesting depth (`MaxDepth`, default 10, `ErrMaxDepthExceeded`).
8. **Fail Closed on Empty Filters (`MatchAll`)**: An uninitialized or empty `Filter` returns `ErrEmptyFilter` on `.Build()`. Querying an entire collection requires an explicit `MatchAll()` call.
9. **Strict `$exists` & Null-Safety**: `Exists` and `NotExists` emit strict boolean values (`true`/`false`), and `ExistsNotNull` combines `$exists: true` with `$ne: nil`.
10. **Validated `$elemMatch` & `$expr` Builders**: `ElemMatch` requires a non-empty validated `Filter` (or `ScalarElemMatch` with `ScalarOp[T]`) and rejects `MatchAll()` sub-filters (`ErrEmptyElemMatch`). `$expr` array-capacity (`ArraySizeLtField`, `ArraySizeLt/Lte/Gt/Gte`) and field-comparison (`Expr*Fields`) builders validate both operand field paths.
11. **Go Style & Error Handling**: Follow [.github/instructions/go.instructions.md](.github/instructions/go.instructions.md):
    - Separate error declaration and error checking (`result, err := fn() \n if err != nil`).
    - Context propagation: avoid `context.Background()` except in tests.
12. **Conventional Commits**: Commit messages must follow Conventional Commits (`feat:`, `fix:`, `chore:`, etc.) to support automated changelog and semver releases via Release Please ([.github/workflows/release.yml](.github/workflows/release.yml)).
