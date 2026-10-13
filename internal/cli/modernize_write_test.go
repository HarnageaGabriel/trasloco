package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestModernizeWrite(t *testing.T) {
	const classB = "steps:\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n    versionSuffix: .$(Build.BuildNumber)"
	const classBOut = "steps:\n- task: ServiceFabricUpdateManifests@2\n  inputs:\n    applicationPackagePath: pkg\n    versionSuffix: .$(Build.BuildNumber)\n    updateType: 'Manifest versions'"
	const guard = "steps:\n- task: ServiceFabricUpdateAppVersions@1\n  inputs:\n    applicationPackagePath: pkg\n    updateOnlyChanged: true"
	for _, tc := range []struct {
		name, input, want, diagnostic string
		check                         bool
		exit                          int
	}{
		{"class A", retiredCopy, strings.ReplaceAll(retiredCopy, "@1", "@2"), "TRL-MOD-0001", false, ExitOK},
		{"class B", classB, classBOut, "TRL-MOD-0002", true, ExitOK},
		{"CRLF no final newline", "steps:\r\n- task: CopyFiles@1", "steps:\r\n- task: CopyFiles@2", "TRL-MOD-0001", true, ExitOK},
		{"guard", guard, guard, "TRL-MOD-0006", false, ExitOK},
		{"guard check", guard, guard, "TRL-MOD-0006", true, ExitPolicy},
		{"manual", "steps:\n- task: CmdLine@1\n", "steps:\n- task: CmdLine@1\n", "TRL-MOD-0003", false, ExitOK},
		{"manual check", "steps:\n- task: CmdLine@1\n", "steps:\n- task: CmdLine@1\n", "TRL-MOD-0003", true, ExitPolicy},
		{"current", "steps:\n- task: CopyFiles@2\n", "steps:\n- task: CopyFiles@2\n", "", true, ExitOK},
		{"self check", "steps:\n- tAsk: GrAdle@1\n  \xeb:", "steps:\n- tAsk: GrAdle@1\n  \xeb:", "TRL-INT-0002", true, ExitError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": tc.input})
			t.Chdir(root)
			path := filepath.Join(root, "pipeline.yml")
			old := time.Unix(1000000000, 0)
			if err := os.Chtimes(path, old, old); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"modernize", "--write", "--format", "json"}
			if tc.check {
				args = append(args, "--check")
			}
			code, output, stderr := executeModernize(t, args...)
			if code != tc.exit {
				t.Fatalf("exit=%d want=%d\n%s\n%s", code, tc.exit, output, stderr)
			}
			var report modernizeJSONReport
			if err := json.Unmarshal([]byte(output), &report); err != nil {
				t.Fatal(err)
			}
			if tc.diagnostic == "TRL-MOD-0003" || tc.diagnostic == "TRL-MOD-0006" {
				if len(report.Diagnostics) != 0 || len(report.Findings) != 1 || (tc.diagnostic == "TRL-MOD-0006") != (len(report.Findings[0].RewriteNotes) == 1) {
					t.Fatalf("want one finding and no diagnostic for %s: %s", tc.diagnostic, output)
				}
			} else if tc.diagnostic != "" && (len(report.Diagnostics) == 0 || report.Diagnostics[0].Code != tc.diagnostic) {
				t.Fatalf("missing %s: %s", tc.diagnostic, output)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != tc.want {
				t.Fatalf("bytes=%q want=%q err=%v", got, tc.want, err)
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.Mode().Perm() != before.Mode().Perm() {
				t.Fatal("permissions changed")
			}
			changed := tc.input != tc.want
			if !changed && !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("untouched file mtime changed")
			}
			if report.Summary == nil || (report.Summary.StepsRewritten == 1) != changed || (report.Summary.FilesRewritten == 1) != changed {
				t.Fatalf("bad summary: %s", output)
			}
			if !changed && tc.diagnostic != "" && tc.exit != ExitError && report.Summary.ManualSteps != 1 {
				t.Fatalf("manual count: %s", output)
			}
			_, second, _ := executeModernize(t, args...)
			if err := json.Unmarshal([]byte(second), &report); err != nil {
				t.Fatal(err)
			}
			final, err := os.Stat(path)
			if err != nil || !final.ModTime().Equal(after.ModTime()) || report.Summary.StepsRewritten != 0 || report.Summary.FilesRewritten != 0 {
				t.Fatalf("second run changed file: %s err=%v", second, err)
			}
			entries, err := os.ReadDir(root)
			if err != nil || len(entries) != 1 {
				t.Fatalf("temporary file left behind: %v err=%v", entries, err)
			}
		})
	}
}

func TestModernizeWriteUTF16(t *testing.T) {
	for _, little := range []bool{true, false} {
		for _, bom := range []bool{true, false} {
			input := encodeUTF16(retiredCopy, little, bom)
			root := writeTree(t, map[string]string{"pipeline.yml": string(input)})
			code, _, stderr := executeModernize(t, "modernize", "--write", "--check", root)
			got, err := os.ReadFile(filepath.Join(root, "pipeline.yml"))
			if err != nil || code != ExitPolicy || !bytes.Equal(input, got) || !strings.Contains(stderr, "UTF-16 file not rewritten") {
				t.Fatalf("little=%v bom=%v exit=%d err=%v stderr=%s", little, bom, code, err, stderr)
			}
		}
	}
}

func TestModernizeWriteSymlinks(t *testing.T) {
	for _, kind := range []string{"file", "directory", "ancestor"} {
		t.Run(kind, func(t *testing.T) {
			outside := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
			root := t.TempDir()
			link := filepath.Join(root, "linked.yml")
			target := filepath.Join(outside, "pipeline.yml")
			if kind != "file" {
				target = outside
			}
			if err := os.Symlink(target, link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			path := root
			if kind == "ancestor" {
				path = filepath.Join(link, "pipeline.yml")
			}
			code, _, stderr := executeModernize(t, "modernize", "--write", path)
			got, err := os.ReadFile(filepath.Join(outside, "pipeline.yml"))
			if err != nil || code != ExitOK || string(got) != retiredCopy || !strings.Contains(stderr, "symbolic links are not followed") {
				t.Fatalf("exit=%d err=%v stderr=%s", code, err, stderr)
			}
		})
	}
}

func TestModernizeWriteReports(t *testing.T) {
	for _, format := range []string{"text", "json", "sarif"} {
		t.Run(format, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": "steps:\n- task: CopyFiles@1.1.4\n"})
			code, output, stderr := executeModernize(t, "modernize", "--write", "--format", format, root)
			if code != ExitOK || stderr != "" || !strings.Contains(output, map[bool]string{true: "CopyFiles@1 -> CopyFiles@2  [rewritten]", false: "TRL-MOD-0001"}[format == "text"]) || !strings.Contains(output, "TRL-MOD-0011") {
				t.Fatalf("exit=%d\n%s\n%s", code, output, stderr)
			}
			if format == "text" && !strings.Contains(output, "Rewrote 1 step(s) in 1 file(s); 0 step(s) need manual migration") {
				t.Fatalf("missing summary: %s", output)
			}
			if format == "sarif" {
				var report sarifLog
				if err := json.Unmarshal([]byte(output), &report); err != nil {
					t.Fatal(err)
				}
				if len(report.Runs[0].Results) != 2 || !report.Runs[0].Invocations[0].ExecutionSuccessful || report.Runs[0].Properties["summary"] == nil {
					t.Fatalf("invalid SARIF: %s", output)
				}
			}
		})
	}
}

func TestModernizeWriteFailures(t *testing.T) {
	root := writeTree(t, map[string]string{"pipeline.yml": retiredCopy})
	for _, tc := range []struct {
		args []string
		exit int
	}{
		{[]string{"modernize", "--write", "--check", filepath.Join(root, "missing.yml")}, ExitError},
		{[]string{"modernize", "--write", "--format", "invalid", root}, ExitUsage},
	} {
		code, _, stderr := executeModernize(t, tc.args...)
		if code != tc.exit {
			t.Fatalf("exit=%d want=%d: %s", code, tc.exit, stderr)
		}
	}
	path := filepath.Join(root, "pipeline.yml")
	initial, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeModernizeFile(path, []byte("stale contents"), []byte("replacement"), initial); err == nil {
		t.Fatal("overwrote a concurrently changed file")
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != retiredCopy {
		t.Fatalf("failure changed original: %q err=%v", got, err)
	}
}
