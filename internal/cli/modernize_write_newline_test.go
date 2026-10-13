package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModernizeWriteAppendsKeepLineEndings(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{
			"newline-terminated Maven@2",
			"steps:\n- task: Maven@2\n  inputs:\n    goals: package\n    publishJUnitResults: false\n",
			"steps:\n- task: Maven@4\n  inputs:\n    goals: package\n    publishJUnitResults: false\n    mavenFeedAuthenticate: true\n",
		},
		{
			"CRLF Maven@2 pin",
			"steps:\r\n- task: Maven@2\r\n  inputs:\r\n    goals: package\r\n    publishJUnitResults: false\r\n",
			"steps:\r\n- task: Maven@4\r\n  inputs:\r\n    goals: package\r\n    publishJUnitResults: false\r\n    mavenFeedAuthenticate: true\r\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := writeTree(t, map[string]string{"pipeline.yml": tc.input})
			t.Chdir(root)
			code, output, stderr := executeModernize(t, "modernize", "--write", "--format", "json")
			if code != ExitOK {
				t.Fatalf("exit=%d\n%s\n%s", code, output, stderr)
			}
			got, err := os.ReadFile(filepath.Join(root, "pipeline.yml"))
			if err != nil || string(got) != tc.want {
				t.Fatalf("bytes=%q want=%q err=%v", got, tc.want, err)
			}
		})
	}
}
