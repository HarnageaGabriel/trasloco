package cli

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/google/go-cmp/cmp"

	"github.com/HarnageaGabriel/trasloco/internal/yamlx"
)

func encodeUTF16(text string, littleEndian, bom bool) []byte {
	var data []byte
	put := func(unit uint16) {
		if littleEndian {
			data = append(data, byte(unit), byte(unit>>8))
		} else {
			data = append(data, byte(unit>>8), byte(unit))
		}
	}
	if bom {
		put(0xfeff)
	}
	for _, unit := range utf16.Encode([]rune(text)) {
		put(unit)
	}
	return data
}

func TestDecodeModernizeDataUTF16(t *testing.T) {
	tests := []struct {
		name    string
		input   []byte
		want    string
		wantErr bool
	}{
		{name: "LE with BOM", input: encodeUTF16(retiredCopy, true, true), want: retiredCopy},
		{name: "BE with BOM", input: encodeUTF16(retiredCopy, false, true), want: retiredCopy},
		{name: "LE without BOM", input: encodeUTF16(retiredCopy, true, false), want: retiredCopy},
		{name: "BE without BOM", input: encodeUTF16(retiredCopy, false, false), want: retiredCopy},
		{name: "plain UTF-8", input: []byte(retiredCopy), want: retiredCopy},
		{name: "odd length LE with BOM", input: append(encodeUTF16(retiredCopy, true, true), 'x'), wantErr: true},
		// Without a BOM an odd length is not UTF-16: the bytes are returned unchanged and later skipped.
		{name: "odd length LE without BOM", input: append(encodeUTF16(retiredCopy, true, false), 'x'), want: string(append(encodeUTF16(retiredCopy, true, false), 'x'))},
		{name: "short file", input: []byte("a\x00"), want: "a\x00"},
		{name: "NUL only", input: make([]byte, 8), want: string(make([]byte, 8))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := decodeModernizeData(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, test.wantErr)
			}
			if diff := cmp.Diff(test.want, string(got)); diff != "" {
				t.Errorf("decoded text mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestModernizeScansBOMlessUTF16Files(t *testing.T) {
	tests := []struct {
		name         string
		littleEndian bool
		bom          bool
	}{
		{"LE with BOM", true, true},
		{"BE with BOM", false, true},
		{"LE without BOM", true, false},
		{"BE without BOM", false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": string(encodeUTF16(retiredCopy, test.littleEndian, test.bom))})
			t.Chdir(root)
			code, out, stderr := executeModernize(t, "modernize", "--check")
			if code != ExitPolicy || !strings.Contains(out, "CopyFiles@1") || stderr != "" {
				t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
			}
		})
	}
}

func TestModernizePartialRunExitPrecedence(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check", "missing.yml", "pipeline.yml")
	if code != ExitError {
		t.Fatalf("exit = %d, want %d (I/O error wins over findings)", code, ExitError)
	}
	if !strings.Contains(out, "pipeline.yml:2:9") || !strings.Contains(out, "CopyFiles@1") {
		t.Errorf("findings missing from report:\n%s", out)
	}
	if !strings.Contains(stderr, "missing.yml") {
		t.Errorf("stderr missing the unreadable path: %q", stderr)
	}
	_, help, _ := executeModernize(t, "modernize", "--help")
	if !strings.Contains(help, "Precedence: exit 1 wins over exit 5") {
		t.Errorf("help does not document precedence:\n%s", help)
	}
}

func TestModernizeCheckFailsClosedOnUndecodableDiscoveredFile(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
	if err := os.WriteFile(filepath.Join(root, "bad.yml"), []byte{0xff, 0xfe, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitError || !strings.Contains(out, "CopyFiles@1") || !strings.Contains(stderr, "bad.yml") {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
}

func TestModernizeCheckFailsClosedOnUnreadableDiscoveredFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows chmod does not reliably deny reads")
	}
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy, "unreadable.yml": retiredCopy})
	path := filepath.Join(root, "unreadable.yml")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if _, err := os.ReadFile(path); err == nil {
		t.Skip("chmod does not deny reads for this user")
	}
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitError || !strings.Contains(out, "pipeline.yml") || !strings.Contains(stderr, "unreadable.yml") {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
}

func TestModernizeCheckFailsClosedOnUnreadableSubdirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows chmod does not reliably deny traversal")
	}
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy, "blocked/nested.yml": retiredCopy})
	path := filepath.Join(root, "blocked")
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
	if entries, err := os.ReadDir(path); err == nil {
		_ = entries
		t.Skip("chmod does not deny traversal for this user")
	}
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitError || !strings.Contains(out, "pipeline.yml") || !strings.Contains(stderr, "blocked") {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, out, stderr)
	}
}

func TestModernizeCheckFailsClosedOnOversizedDiscoveredFile(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
	oversized := filepath.Join(root, "oversized.yml")
	if err := os.WriteFile(oversized, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(oversized, yamlx.DefaultSizeLimit+1); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	code, out, stderr := executeModernize(t, "modernize", "--check")
	if code != ExitError || !strings.Contains(out, "CopyFiles@1") || !strings.Contains(stderr, "oversized.yml") {
		t.Fatalf("--check exit = %d; stdout:\n%s; stderr:\n%s", code, out, stderr)
	}
	code, out, stderr = executeModernize(t, "modernize")
	if code != ExitOK || !strings.Contains(out, "CopyFiles@1") || !strings.Contains(stderr, "oversized.yml") {
		t.Fatalf("default exit = %d; stdout:\n%s; stderr:\n%s", code, out, stderr)
	}
}
