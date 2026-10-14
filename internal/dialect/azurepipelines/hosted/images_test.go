package hosted

import "testing"

func TestImageOS(t *testing.T) {
	cases := []struct {
		image string
		os    string
		ok    bool
	}{
		{"ubuntu-latest", "linux", true},
		{"Ubuntu-24.04", "linux", true},
		{" windows-2022 ", "windows", true},
		{"windows-latest", "windows", true},
		{"macOS-latest", "macos", true},
		{"macOS-15", "macos", true},
		{"windows-2025-vs2026", "windows", true},
		{"Ubuntu-26.04", "linux", true},
		{"macOS-26", "macos", true},
		{"", "", false},
		{"ubuntu-18.04", "", false},
		{"windows-2019", "", false},
		{"macOS-13", "", false},
		{"my-self-hosted-image", "", false},
		{"${{ parameters.image }}", "", false},
		{"$(image)", "", false},
		{"ubuntu", "", false},
	}
	for _, tc := range cases {
		if os, ok := ImageOS(tc.image); os != tc.os || ok != tc.ok {
			t.Errorf("ImageOS(%q) = %q, %t; want %q, %t", tc.image, os, ok, tc.os, tc.ok)
		}
	}
}

// Every mapped value must be an OS the normalized runner model understands.
func TestHostedImageOSValuesAreKnown(t *testing.T) {
	for image, os := range imageOS { //trasloco:unordered-ok independent checks
		switch os {
		case "windows", "linux", "macos":
		default:
			t.Errorf("%s maps to unknown OS %q", image, os)
		}
	}
}
