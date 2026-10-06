package version

import "testing"

func TestGetReflectsBuildVariables(t *testing.T) {
	info := Get()
	if info.Version != Version || info.Commit != Commit || info.Date != Date {
		t.Fatalf("Get() = %+v, want build variables %q %q %q", info, Version, Commit, Date)
	}
}
