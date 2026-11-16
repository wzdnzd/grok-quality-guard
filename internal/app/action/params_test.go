package action

import (
	"reflect"
	"testing"
)

func TestParameterHelpers(t *testing.T) {
	values := map[string]any{
		"count":   3.0,
		"enabled": "yes",
		"auth_ids": []any{
			" auth-1 ",
			"",
			"auth-2",
			"auth-1",
		},
	}
	if got := Int(values, "count", 1); got != 3 {
		t.Fatalf("Int() = %d, want 3", got)
	}
	if got := Int(values, "missing", 7); got != 7 {
		t.Fatalf("Int() fallback = %d, want 7", got)
	}
	if !Bool(values, "enabled") {
		t.Fatal("Bool() did not parse yes")
	}
	want := []string{"auth-1", "auth-2"}
	if got := AuthIDs(values); !reflect.DeepEqual(got, want) {
		t.Fatalf("AuthIDs() = %#v, want %#v", got, want)
	}
	if got := AuthIDs(map[string]any{"auth_id": "auth-3"}); !reflect.DeepEqual(got, []string{"auth-3"}) {
		t.Fatalf("single AuthIDs() = %#v", got)
	}
}
