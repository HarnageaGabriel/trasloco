package hosted

import "testing"

func TestIsHostedPoolName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"Azure Pipelines", true},
		{"azure pipelines", true},
		{"  Azure Pipelines  ", true},
		{"AZURE PIPELINES", true},
		{"Azure Agent Pool", false},
		{"MyPool", false},
		{"", false},
		{"$(poolName)", false},
		{"${{ parameters.pool }}", false},
	}
	for _, tc := range cases {
		if got := IsHostedPoolName(tc.name); got != tc.want {
			t.Errorf("IsHostedPoolName(%q) = %t; want %t", tc.name, got, tc.want)
		}
	}
}
