package mq

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func BenchmarkValidateField_Simple(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ValidateField("workspace_id")
	}
}

func BenchmarkValidateField_NestedPath(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ValidateField("seats.0.hardware.fingerprint_sha256")
	}
}

func BenchmarkValidateUpdateField_Positional(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_ = ValidateUpdateField("seats.$.last_heartbeat_at")
	}
}

func BenchmarkEqBuild(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, err := Eq("workspace_id", "ws_01J8Z9X2K3M4N5P6Q7R8S9T0V1").Build()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkCompoundFilterBuild(b *testing.B) {
	oid := bson.NewObjectID()
	statuses := []string{"active", "trial"}
	b.ReportAllocs()
	for b.Loop() {
		_, err := And(
			Eq("_id", oid),
			Eq("workspace_id", "ws_123"),
			In("status", statuses),
			ExistsNotNull("activated_at"),
			ElemMatch("seats", Eq("hwid", "sha256_abc123")),
			ArraySizeLtField("seats", "license_seat_limit"),
		).Build()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkPrefixRegexBuild(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, err := PrefixCaseInsensitive("email", "stefan+test@example.com").Build()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdateBuild(b *testing.B) {
	now := time.Now().UTC()
	b.ReportAllocs()
	for b.Loop() {
		_, err := NewUpdate().
			Set("seats.$.last_heartbeat_at", now).
			Set("seats.$.app_version", "2.1.0").
			Inc("seats_used", 1).
			CurrentDate("updated_at").
			Build()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUpdateStructAndMapBuild(b *testing.B) {
	now := time.Now().UTC()
	seat := testSeat{
		ID:              "seat_1",
		HWID:            "sha256_abc123",
		AppVersion:      "2.1.0",
		LastHeartbeatAt: now,
	}
	components := map[string]string{"cpu": "m3", "os": "macos"}
	b.ReportAllocs()
	for b.Loop() {
		_, err := NewUpdate().
			PushStruct("seats", seat).
			SetStringMap("hwid_components", components).
			Build()
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkSortBuild(b *testing.B) {
	b.ReportAllocs()
	for b.Loop() {
		_, err := SortDesc("created_at").Asc("_id").Build()
		if err != nil {
			b.Fatal(err)
		}
	}
}
