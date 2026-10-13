package modernize

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestRewriteStepDirectives(t *testing.T) {
	cat := testCatalog(t)
	for _, task := range []string{"FtpUpload@1", "CopyFiles@1"} {
		for _, directive := range []string{
			"${{ if true }}:\n    inputs:\n      serverEndpoint: ftp\n      rootFolder: out\n      overwrite: false",
			"${{ insert }}: ${{ parameters.props }}",
		} {
			src := "steps:\n- task: " + task + "\n  " + directive + "\n"
			out, ds, changed, err := Rewrite(context.Background(), []byte(src), "test.yml", cat)
			if err != nil || changed || string(out) != src || len(ds) != 1 {
				t.Fatalf("task=%s changed=%v err=%v diagnostics=%v", task, changed, err, ds)
			}
			d := ds[0]
			if d.Code != "TRL-MOD-0009" || d.Params["reason"] != "dynamic-input-key" || d.Source.Line != 2 || d.Source.Col != 9 {
				t.Fatalf("unexpected directive diagnostic: %+v", d)
			}
		}
	}
}

func TestRewriteManyInputs(t *testing.T) {
	if testing.Short() {
		t.Skip("50,000-input regression")
	}
	var src strings.Builder
	src.WriteString("steps:\n- task: CopyFiles@1\n  inputs:\n")
	for i := 0; i < 50000; i++ {
		fmt.Fprintf(&src, "    x%05d: v\n", i)
	}
	out, ds, changed, err := Rewrite(context.Background(), []byte(src.String()), "large.yml", testCatalog(t))
	if err != nil || !changed || len(ds) != 50001 || string(out) != strings.Replace(src.String(), "CopyFiles@1", "CopyFiles@2", 1) {
		t.Fatalf("changed=%v err=%v diagnostics=%d", changed, err, len(ds))
	}
}

func TestRewriteManyRetiredSteps(t *testing.T) {
	const step = "- task: CopyFiles@1.1.4\n"
	const replacement = "- task: CopyFiles@2\n"
	src := "steps:\n" + strings.Repeat(step, 3000) + "- script: echo unchanged\n"
	want := "steps:\n" + strings.Repeat(replacement, 3000) + "- script: echo unchanged\n"
	out, ds, changed, err := Rewrite(context.Background(), []byte(src), "large.yml", testCatalog(t))
	if err != nil || !changed || len(ds) != 6000 || string(out) != want {
		t.Fatalf("changed=%v err=%v diagnostics=%d output matches=%v", changed, err, len(ds), string(out) == want)
	}
}

func TestRewriteDuplicateFoldedInputs(t *testing.T) {
	src := "steps:\n- task: CopyFiles@1\n  inputs:\n    unknowns: a\n    unknownſ: b\n"
	out, ds, changed, err := Rewrite(context.Background(), []byte(src), "test.yml", testCatalog(t))
	if err != nil || changed || string(out) != src || len(ds) != 1 || ds[0].Params["reason"] != "duplicate-input" {
		t.Fatalf("changed=%v err=%v diagnostics=%v", changed, err, ds)
	}
}
