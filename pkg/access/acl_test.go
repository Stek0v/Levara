package access

import (
	"reflect"
	"testing"
)

func TestValidPermissionType(t *testing.T) {
	for _, ok := range []string{PermissionRead, PermissionWrite, PermissionDelete, PermissionShare} {
		if !ValidPermissionType(ok) {
			t.Fatalf("ValidPermissionType(%q) = false, want true", ok)
		}
	}
	for _, bad := range []string{"", "admin", "READ", "owner", "execute"} {
		if ValidPermissionType(bad) {
			t.Fatalf("ValidPermissionType(%q) = true, want false", bad)
		}
	}
}

func TestPermissionTypes(t *testing.T) {
	want := []string{"read", "write", "delete", "share"}
	if got := PermissionTypes(); !reflect.DeepEqual(got, want) {
		t.Fatalf("PermissionTypes() = %v, want %v", got, want)
	}
	// Every enumerated type must also validate — the two helpers share one source.
	for _, pt := range PermissionTypes() {
		if !ValidPermissionType(pt) {
			t.Fatalf("enumerated %q fails ValidPermissionType", pt)
		}
	}
}
