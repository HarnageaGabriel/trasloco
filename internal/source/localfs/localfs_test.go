package localfs

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("Windows symlink creation requires Developer Mode or symlink privilege: %v", err)
		}
		t.Skipf("symlinks unavailable on this platform or account: %v", err)
	}
}

func TestOpen(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.yml"), "steps: []")
	source, err := New(map[string]string{"": root, "templates": root}, WithMaxSize(100))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ alias, file string }{{"", "a.yml"}, {"self", "a.yml"}, {"templates", "templates@a.yml"}} {
		data, origin, err := source.Open(context.Background(), test.alias, "a.yml")
		if err != nil || string(data) != "steps: []" || origin.File != test.file || origin.Platform != "azure-pipelines" {
			t.Fatalf("Open(%q) = %q, %#v, %v", test.alias, data, origin, err)
		}
	}
	// Backslashes are separators, as in Azure.
	write(t, filepath.Join(root, "dir", "b.yml"), "x")
	if _, origin, err := source.Open(context.Background(), "", `dir\b.yml`); err != nil || origin.File != "dir/b.yml" {
		t.Fatalf("backslash open = %#v, %v", origin, err)
	}
}

func TestDuplicateRootAliasIsRejected(t *testing.T) {
	root := t.TempDir()
	if _, err := New(map[string]string{"": root, "self": root}); err == nil {
		t.Fatal("mapping both \"\" and \"self\" succeeded")
	}
	if _, err := New(map[string]string{"x": filepath.Join(root, "missing")}); err == nil || strings.Contains(err.Error(), root) {
		t.Fatalf("missing root error = %v", err)
	}
}

func TestRejectsUnsafeNames(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ok.yml"), "ok")
	write(t, filepath.Join(root, "dir", "ok.yml"), "ok")
	source, err := New(map[string]string{"": root})
	if err != nil {
		t.Fatal(err)
	}
	names := []string{
		"", "../x", "dir/../../x", "/etc/passwd", `\etc\passwd`, filepath.Join(root, "ok.yml"),
		"C:foo", `C:\foo`, `C:ok.yml`, `\server\share\x.yml`, `\?\C:\x.yml`, `//server/share/x.yml`,
		"ok.yml:stream", "ok.yml::$DATA", "ok.yml.", "ok.yml ", "dir./ok.yml", "bad\x00.yml", "bad\n.yml",
	}
	for _, name := range names {
		if data, _, err := source.Open(context.Background(), "", name); err == nil {
			t.Errorf("Open(%q) succeeded with %q", name, data)
		} else if strings.Contains(err.Error(), root) {
			t.Errorf("Open(%q) error leaks the absolute path: %v", name, err)
		} else if strings.HasPrefix(name, "ok.yml:") && !strings.Contains(err.Error(), "invalid character") {
			t.Errorf("Open(%q) error = %v, want explicit ':' character rejection", name, err)
		}
	}
}

func TestWindowsReservedNames(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("reserved device names are ordinary file names outside Windows")
	}
	source, err := New(map[string]string{"": t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"NUL", "con.yml", "COM1", "dir/aux.txt", "LPT1.yml"} {
		if _, _, err := source.Open(context.Background(), "", name); err == nil || errors.Is(err, fs.ErrNotExist) {
			t.Errorf("Open(%q) error = %v, want a name rejection", name, err)
		}
	}
}

func TestSymlinkEscapeAndParentThroughLink(t *testing.T) {
	outside, root := t.TempDir(), t.TempDir()
	write(t, filepath.Join(outside, "secret.yml"), "secret")
	write(t, filepath.Join(root, "inside.yml"), "inside")
	symlink(t, filepath.Join(outside, "secret.yml"), filepath.Join(root, "file-link.yml"))
	symlink(t, outside, filepath.Join(root, "dir-link"))
	source, err := New(map[string]string{"": root})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"file-link.yml", "dir-link/secret.yml"} {
		if data, _, err := source.Open(context.Background(), "", name); err == nil {
			t.Errorf("symlink escape %q succeeded with %q", name, data)
		} else if strings.Contains(err.Error(), root) || strings.Contains(err.Error(), outside) {
			t.Errorf("error leaks an absolute path: %v", err)
		}
	}
	// "dir-link/../inside.yml" is cleaned lexically to "inside.yml": it can only
	// ever name a file inside the root, never the parent of the link target.
	data, _, err := source.Open(context.Background(), "", "dir-link/../inside.yml")
	if err != nil || string(data) != "inside" {
		t.Fatalf("lexical parent through a link = %q, %v", data, err)
	}
	if data, _, err := source.Open(context.Background(), "", "dir-link/../secret.yml"); err == nil {
		t.Fatalf("parent through a symlinked dir reached %q", data)
	}
}

func TestSymlinkInsideRootIsAllowed(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "real.yml"), "real")
	symlink(t, filepath.Join(root, "real.yml"), filepath.Join(root, "alias.yml"))
	source, err := New(map[string]string{"": root})
	if err != nil {
		t.Fatal(err)
	}
	if data, _, err := source.Open(context.Background(), "", "alias.yml"); err != nil || string(data) != "real" {
		t.Fatalf("in-root symlink = %q, %v", data, err)
	}
}

func TestRejectsDirectoriesAndOversize(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "big.yml"), "12345")
	if err := os.Mkdir(filepath.Join(root, "dir.yml"), 0o700); err != nil {
		t.Fatal(err)
	}
	source, err := New(map[string]string{"": root}, WithMaxSize(4))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ alias, name string }{{"missing", "x"}, {"", "big.yml"}, {"", "dir.yml"}, {"", "absent.yml"}} {
		if _, _, err := source.Open(context.Background(), test.alias, test.name); err == nil {
			t.Fatalf("Open(%q,%q) succeeded", test.alias, test.name)
		} else if strings.Contains(err.Error(), root) {
			t.Fatalf("error leaks the absolute path: %v", err)
		}
	}
	if _, _, err := source.Open(context.Background(), "", "absent.yml"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file error = %v, want fs.ErrNotExist", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := source.Open(ctx, "", "big.yml"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled open error = %v", err)
	}
}

func TestDefaultLimitAndCustomSize(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.yml"), "abc")
	source, err := New(map[string]string{"": root}, WithMaxSize(0))
	if err != nil {
		t.Fatal(err)
	}
	if data, _, err := source.Open(context.Background(), "", "a.yml"); err != nil || string(data) != "abc" {
		t.Fatalf("default limit open = %q, %v", data, err)
	}
}

func TestReservedNameRule(t *testing.T) {
	for _, name := range []string{"NUL", "con", "CON.yml", "aux.txt", "COM1", "lpt9.yml", "com0", "CONIN$"} {
		if !reservedName(name) {
			t.Errorf("reservedName(%q) = false", name)
		}
	}
	for _, name := range []string{"console.yml", "comx", "COM10", "a.yml", "nul2"} {
		if reservedName(name) {
			t.Errorf("reservedName(%q) = true", name)
		}
	}
}
