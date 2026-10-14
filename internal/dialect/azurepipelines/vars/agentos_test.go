package vars

import "testing"

func TestAgentOSForVMImage(t *testing.T) {
	cases := []struct{ image, want string }{
		{"ubuntu-latest", AgentOSLinux},
		{"Ubuntu-24.04", AgentOSLinux},
		{" windows-2022 ", AgentOSWindows},
		{"windows-latest", AgentOSWindows},
		{"macOS-latest", AgentOSMacOS},
		{"macOS-15", AgentOSMacOS},
		{"", ""},
		{"ubuntu-18.04", ""},
		{"windows-2019", ""},
		{"macOS-13", ""},
		{"windows-2025-vs2026", AgentOSWindows},
		{"Ubuntu-26.04", AgentOSLinux},
		{"macOS-26", AgentOSMacOS},
		{"my-self-hosted-image", ""},
		{"${{ parameters.image }}", ""},
		{"$(image)", ""},
		{"ubuntu", ""},
	}
	for _, tc := range cases {
		if got := AgentOSForVMImage(tc.image); got != tc.want {
			t.Errorf("AgentOSForVMImage(%q) = %q, want %q", tc.image, got, tc.want)
		}
	}
}
