# Go MongoDB Queries (`mq`)

**Build MongoDB filters, updates, and sorts in Go without worrying about NoSQL injection.**

`mq` is a small, type-safe query builder for the official [MongoDB Go Driver v2](https://pkg.go.dev/go.mongodb.org/mongo-driver/v2). It turns readable Go code into valid BSON and refuses anything that looks unsafe: bad field names, raw operators, empty filters, runaway regexes, and more.

```go
filter, err := mq.And(
	mq.Eq("workspace_id", "ws_123"),
	mq.In("status", []string{"active", "trial"}),
).Build()
```

---

## Table of Contents

- [Why use it?](#why-use-it)
- [Installation](#installation)
- [Quick start](#quick-start)
- [Cookbook](#cookbook)
  - [Filters](#filters)
  - [Updates](#updates)
  - [Sorting](#sorting)
  - [Aggregation `$match` stage](#aggregation-match-stage)
- [Handling errors](#handling-errors)
- [Configuring limits](#configuring-limits)
- [FAQ](#faq)
- [Safety rules in detail](#safety-rules-in-detail)
- [Benchmarks](#benchmarks)
- [Development](#development)
- [License](#license)

---

## Why use it?

Building MongoDB queries from user input by hand is risky. Here is the classic NoSQL injection problem:

```go
// DANGEROUS: if the client sends {"$ne": ""} as the password,
// this matches every user, no password needed.
filter := bson.M{"email": email, "password": passwordFromJSON}
```

`mq` prevents this class of bug by design:

| Problem | How `mq` protects you |
| --- | --- |
| **Operator injection** (`{"$ne": ""}` sneaking in as a value) | Values must be plain scalars (string, number, bool, time, ObjectID…). Maps, slices, and structs are rejected. Every comparison is explicit: `{"field": {"$eq": value}}`. |
| **Malicious field names** (`"$where"`, `"a.$gt"`) | All field names are validated. |
| **Regex denial of service (ReDoS)** | No raw `$regex`. Use `Prefix`, `Suffix`, `Contains`, etc. Input is escaped and length-limited. |
| **Accidentally matching or wiping everything** | An empty filter is an error. You must write `mq.MatchAll()` on purpose. |
| **Huge queries** | `$in` lists and nesting depth are capped. |
| **Server-side JavaScript** | `$where`, `$function`, and `$accumulator` cannot be built. |

You also get compile-time type checking. For example, `mq.Eq("age", someMap)` will not compile.

> **Note:** All errors are reported when you call `.Build()`. Builder calls never panic, and you only need to check for errors once at the end.

---

## Installation

Requires Go 1.26+ and the MongoDB Go Driver v2.

```bash
go get github.com/stfsy/go-mongodb-queries
```

Import it with a short alias:

```go
import mq "github.com/stfsy/go-mongodb-queries"
```

---

## Quick start

A complete example: find, update, and sort documents.

```go
package main

import (
	"context"
	"log"

	mq "github.com/stfsy/go-mongodb-queries"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

func main() {
	ctx := context.Background()

	client, err := mongo.Connect(options.Client().ApplyURI("mongodb://localhost:27017"))
	if err != nil {
		log.Fatal(err)
	}
	defer client.Disconnect(ctx)

	users := client.Database("app").Collection("users")

	// 1. Build a filter: active users in a workspace.
	filter, err := mq.And(
		mq.Eq("workspace_id", "ws_123"),
		mq.Eq("status", "active"),
	).Build()
	if err != nil {
		log.Fatal(err)
	}

	// 2. Build a sort: newest first.
	sort, err := mq.SortDesc("created_at").Build()
	if err != nil {
		log.Fatal(err)
	}

	// 3. Use them with the normal MongoDB driver.
	cursor, err := users.Find(ctx, filter, options.Find().SetSort(sort))
	if err != nil {
		log.Fatal(err)
	}
	defer cursor.Close(ctx)

	// 4. Build an update and apply it.
	update, err := mq.NewUpdate().
		Set("status", "inactive").
		CurrentDate("updated_at").
		Build()
	if err != nil {
		log.Fatal(err)
	}

	_, err = users.UpdateOne(ctx, filter, update)
	if err != nil {
		log.Fatal(err)
	}
}
```

`Build()` returns a plain `bson.D`, so the result works anywhere the driver accepts a filter or update document.

---

## Cookbook

### Filters

**Compare values.** Each function takes a field name and a value.

```go
mq.Eq("status", "active")       // status == "active"
mq.Ne("status", "deleted")      // status != "deleted"
mq.Gt("age", 18)                // age > 18
mq.Gte("age", 18)               // age >= 18
mq.Lt("price", 9.99)            // price < 9.99
mq.Lte("created_at", time.Now())
mq.EqNil("deleted_at")          // deleted_at is null
mq.NeNil("deleted_at")          // deleted_at is not null
```

**Combine conditions.**

```go
mq.And(a, b, c)   // all must match
mq.Or(a, b)       // any can match
mq.Nor(a, b)      // none may match
mq.Not("age", mq.OpGt(65)) // NOT (age > 65)
```

**Match against lists.** Empty lists are rejected on purpose.

```go
mq.In("role", []string{"admin", "editor"})
mq.Nin("role", []string{"banned"})
mq.All("tags", []string{"go", "mongodb"}) // array contains all of these
```

**Check for fields.**

```go
mq.Exists("phone")
mq.NotExists("phone")
mq.ExistsNotNull("phone") // field exists and is not null
```

**Search text safely.** The input is escaped, so `"a.b*"` is treated literally and cannot act as a regex.

```go
mq.Prefix("name", "Ali")                        // starts with
mq.Suffix("email", "@example.com")              // ends with
mq.Contains("bio", "gopher")                    // contains
mq.ExactCaseInsensitive("email", "A@B.com")     // equals, ignoring case
mq.PrefixCaseInsensitive("email", "alice")      // plus Suffix/Contains variants
```

**Query arrays.**

```go
// Documents where at least one seat matches ALL of these conditions
mq.ElemMatch("seats", mq.And(
	mq.Eq("hwid", "sha256_fingerprint"),
	mq.Eq("active", true),
))

// Scalar arrays, e.g. scores: [3, 8, 12]
mq.ScalarElemMatch("scores", mq.OpGte(5), mq.OpLt(10))

mq.Size("tags", 3)                            // exactly 3 items
mq.ArraySizeGt("tags", 0)                     // Lt / Lte / Gt / Gte variants
mq.ArraySizeLtField("seats", "seat_limit")    // array shorter than another field
```

**Compare two fields of the same document.**

```go
mq.ExprGtFields("spent", "budget") // spent > budget (Eq/Ne/Lt/Lte/Gt/Gte variants)
```

**Match everything (on purpose).**

```go
filter, err := mq.MatchAll().Build() // {}
```

**A realistic, nested example.**

```go
filter, err := mq.And(
	mq.Eq("workspace_id", "ws_123"),
	mq.In("status", []string{"active", "trial"}),
	mq.ExistsNotNull("activated_at"),
	mq.ArraySizeLtField("seats", "license_seat_limit"),
	mq.ElemMatch("seats", mq.And(
		mq.Eq("hwid", "sha256_fingerprint"),
		mq.Eq("active", true),
	)),
	mq.PrefixCaseInsensitive("customer.email", "alice+tag@"),
).Build()
```

<details>
<summary><strong>What does the generated BSON look like?</strong></summary>

`mq.Eq("workspace_id", "ws_123").Build()` produces:

```json
{ "workspace_id": { "$eq": "ws_123" } }
```

The explicit `$eq` is intentional. It guarantees a value can never be interpreted as an operator.

</details>

**Using `any`-typed values.** If you only have a value as `any` (for example, from a generic helper), use `EqAny` or `NeAny`. They validate the type at runtime.

```go
mq.EqAny("field", someValue)
```

### Updates

Use `mq.NewUpdate()` and chain as many operations as you need.

```go
update, err := mq.NewUpdate().
	Set("seats.$.app_version", "2.4.1").
	SetStringMap("seats.$.hwid_components", map[string]string{"cpu": "m3", "os": "macos"}).
	Inc("seat_changes_count", 1).
	CurrentDate("updated_at").
	PullFilter("revoked_seats", mq.Eq("hwid", "revoked_hwid")).
	Build()
```

| Goal | Method |
| --- | --- |
| Set / clear a value | `Set`, `SetNil`, `Unset`, `SetOnInsert` (upserts) |
| Numbers | `Inc`, `Min`, `Max` |
| Dates | `CurrentDate` |
| Arrays | `Push`, `PushStruct`, `AddToSet`, `Pull`, `PullFilter`, `SetSlice`, `SetEmptySlice` |
| Maps and structs | `SetStringMap`, `SetStruct`, `SetDocumentStruct`, `SetOnInsertDocumentStruct` |

Good to know:

- **Positional paths** like `"seats.$.hwid"` and `"seats.$[]"` are allowed in updates, but not in filters.
- **Conflicting changes are caught.** Setting `"a"` and `"a.b"` in the same update returns `ErrDuplicateUpdateField`.
- **Structs are scanned.** Struct fields are checked for `$`-prefixed keys before they are sent to the database.
- **Shorthands exist** for single-value updates: `mq.Set(...)`, `mq.Unset(...)`, `mq.Inc(...)`, `mq.Push(...)`, `mq.Pull(...)`.

### Sorting

```go
sort, err := mq.SortDesc("created_at").Asc("_id").Build()
// → {created_at: -1, _id: 1}
```

You can also start with `mq.SortAsc(...)` or `mq.NewSort()`. Field order matters: the first field is the primary sort key.

### Aggregation `$match` stage

`MatchStage` wraps a filter in `{"$match": ...}` so you can drop it into a pipeline.

```go
match, err := mq.MatchStage(mq.Eq("workspace_id", "ws_123"))
if err != nil {
	log.Fatal(err)
}

pipeline := mongo.Pipeline{
	match,
	bson.D{{Key: "$group", Value: bson.D{
		{Key: "_id", Value: "$status"},
		{Key: "count", Value: bson.D{{Key: "$sum", Value: 1}}},
	}}},
}
cursor, err := coll.Aggregate(ctx, pipeline)
```

---

## Handling errors

Every builder reports problems from `Build()`. Compare against the exported errors with `errors.Is`:

```go
filter, err := mq.In("status", []string{}).Build()
if errors.Is(err, mq.ErrEmptySlice) {
	// reject the request, or skip this condition
}
```

| Error | Meaning |
| --- | --- |
| `ErrEmptyFilter` | Empty filter. Use `MatchAll()` if you really want everything. |
| `ErrInvalidFieldName` | Field name is empty, too long, or contains `$`/control characters. |
| `ErrInvalidScalarValue` | Value is a map, slice, struct, or other non-scalar. |
| `ErrEmptySlice` / `ErrMaxElementsExceeded` | `In`/`Nin`/`All` list is empty or too large. |
| `ErrEmptyLogicalGroup` / `ErrMaxDepthExceeded` | `And`/`Or`/`Nor` has no children, or is nested too deeply. |
| `ErrEmptyElemMatch` | `ElemMatch` has no (or a match-all) condition. |
| `ErrNegativeArraySize` | `Size` was given a negative number. |
| `ErrInvalidPattern` / `ErrPatternTooLong` | Text search pattern is empty, contains null bytes, or is too long. |
| `ErrEmptyUpdate` / `ErrDuplicateUpdateField` | Update has no operations, or changes the same field twice. |
| `ErrEmptySort` / `ErrDuplicateSortField` | Sort has no fields, or repeats a field. |

---

## Configuring limits

Defaults are conservative. To change them, use the `...WithOptions` variants:

```go
opts := mq.Options{
	MaxDepth:         5,   // default 10
	MaxInElements:    200, // default 1000
	MaxPatternLength: 64,  // default 256
}

filter, err := mq.Eq("a", 1).BuildWithOptions(opts)
match, err := mq.MatchStageWithOptions(mq.Eq("a", 1), opts)
update, err := mq.NewUpdate().Set("a", 1).BuildWithOptions(opts)
```

Need to validate a field name yourself (for example, one chosen by an API client)? Use `mq.ValidateField(name)` for filters/sorts and `mq.ValidateUpdateField(name)` for updates.

---

## FAQ

**Does `mq` replace the MongoDB driver?**
No. It only builds the `bson.D` documents you pass to the driver.

**Why can't I pass a `bson.M` or a map as a value?**
That is exactly how operator injection happens. If a user-controlled map reaches a query, `{"$ne": ""}` can slip in. `mq` accepts only plain scalars.

**What if I need an operator `mq` doesn't have?**
Open an issue. Raw operators are intentionally excluded, so new operators are added only with safe validation.

**Can I let users pick which field to filter or sort on?**
Yes. Field names are validated on `Build()`. Still, allow-list the fields your API exposes, because validation only guarantees a name is *safe*, not that it is *allowed*.

**Do I need to check errors after every call?**
No. Chain everything and check the error once at `Build()`.

---

## Safety rules in detail

<details>
<summary><strong>Click to expand the full list of invariants</strong></summary>

1. **Explicit operator wrapping (`$eq` everywhere).** Never emits implicit equality `{"field": value}`. Every scalar comparison is wrapped in an operator document such as `{"field": {"$eq": value}}`, which neutralizes NoSQL operator injection at the BSON level.
2. **Compile-time and runtime scalar constraints.** Scalar functions use a generic `Scalar` constraint, and `EqAny`/`NeAny` use runtime reflection guards. Self-constructed BSON containers (`bson.M`, `bson.D`, `bson.A`, `bson.E`, `bson.Raw`, `map[string]any`), slices, and arbitrary structs are rejected as scalar operands.
3. **Field path sanitization (`ValidateField`, `ValidateUpdateField`).** Rejects empty fields, fields starting with `$`, dot segments starting with `$` (`"a.$gt"`), empty segments (`"a..b"`), control characters, null bytes, and names over 255 bytes. In updates, positional array tokens (`"$"`, `"$[]"`) are allowed after the root array segment (e.g. `"seats.$.hwid"`), but multiple positional tokens in one path are rejected.
4. **No server-side JavaScript or raw filters.** `$where`, `$function`, and `$accumulator` cannot be constructed.
5. **ReDoS-safe string matching.** Raw `$regex` is replaced with `Prefix`, `PrefixCaseInsensitive`, `Suffix`, `SuffixCaseInsensitive`, `Contains`, `ContainsCaseInsensitive`, and `ExactCaseInsensitive`. They escape metacharacters with `regexp.QuoteMeta` and enforce a pattern length limit (`DefaultMaxPatternLength = 256`).
6. **Non-empty and bounded set operations (`$in`, `$nin`, `$all`).** Empty slices return `ErrEmptySlice`. Slices larger than `MaxInElements` (default 1000) return `ErrMaxElementsExceeded`.
7. **Bounded logical combinators (`$and`, `$or`, `$nor`, `$not`).** Empty groups fail with `ErrEmptyLogicalGroup`. Nesting deeper than `MaxDepth` (default 10) fails with `ErrMaxDepthExceeded`.
8. **Explicit `MatchAll()`.** An empty or zero-value `Filter` fails with `ErrEmptyFilter`. Matching every document requires `mq.MatchAll()`.
9. **Strict boolean `$exists` and null-safe helpers.** `Exists`, `NotExists`, and `ExistsNotNull` emit strict boolean operands and explicit null exclusion.
10. **Validated `$elemMatch` and `$expr`.** `ElemMatch` and `ScalarElemMatch` require a non-empty validated sub-filter (rejecting `MatchAll()`). `ArraySizeLtField`, `ArraySizeLt/Lte/Gt/Gte`, and `ExprEqFields/NeFields/LtFields/LteFields/GtFields/GteFields` validate every field name they use.
11. **Validated array `$size`.** Negative lengths return `ErrNegativeArraySize`.
12. **Operator-scoped updates, validated struct marshaling, and validated sorts.** Update builders only emit `$`-scoped update operators, recursively validate marshaled struct keys against `$` injection, detect duplicate or parent/child conflicting field changes (`ErrDuplicateUpdateField`), and reject empty updates (`ErrEmptyUpdate`). Sort builders only emit `int32(1)` or `int32(-1)` on validated field paths.

</details>

---

## Benchmarks

Builds are fast: simple filters take well under a microsecond, so you will not notice `mq` in your request latency.

```bash
go test -benchmem -run=NONE -bench="Benchmark" .
```

<details>
<summary><strong>Sample results (Windows, amd64, 16 threads)</strong></summary>

```text
BenchmarkValidateField_Simple-16               	69361321	        16.69 ns/op	       0 B/op	       0 allocs/op
BenchmarkValidateField_NestedPath-16           	33058944	        42.39 ns/op	       0 B/op	       0 allocs/op
BenchmarkValidateUpdateField_Positional-16     	43109176	        33.75 ns/op	       0 B/op	       0 allocs/op
BenchmarkEqBuild-16                            	 7089055	       152.2 ns/op	     104 B/op	       4 allocs/op
BenchmarkCompoundFilterBuild-16                	  468391	      2445 ns/op	    2472 B/op	      59 allocs/op
BenchmarkPrefixRegexBuild-16                   	 4214445	       269.2 ns/op	     168 B/op	       5 allocs/op
BenchmarkUpdateBuild-16                        	 1930518	       610.0 ns/op	     608 B/op	       9 allocs/op
BenchmarkUpdateStructAndMapBuild-16            	  321548	      3631 ns/op	    1301 B/op	      34 allocs/op
BenchmarkSortBuild-16                          	 9540717	       109.8 ns/op	     144 B/op	       2 allocs/op
```

</details>

---

## Development

```bash
./test.sh        # go test -v -race -cover -timeout 60s ./...
./lint.sh        # golangci-lint via Docker (or: golangci-lint run -v)
```

Commits follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `chore:`…) so releases and changelogs are automated.

---

## License

All source code is licensed under the [MIT License](LICENSE).
