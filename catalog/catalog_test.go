package catalog_test

import (
	"context"
	"io/fs"
	"regexp"
	"testing"

	shippedcatalog "github.com/HarnageaGabriel/trasloco/catalog"
	catalogmodel "github.com/HarnageaGabriel/trasloco/internal/catalog"
	"go.yaml.in/yaml/v3"
)

func TestCommittedCatalogLoadsAndIsSane(t *testing.T) {
	loaded, err := catalogmodel.Load(context.Background(), shippedcatalog.FS, nil)
	if err != nil {
		t.Fatalf("load committed catalog with schema validation: %v", err)
	}
	if len(loaded.Tasks) == 0 {
		t.Fatal("committed task catalog is empty")
	}
	for _, task := range loaded.Tasks {
		if task.Name == "" || task.ID == "" || len(task.Versions) == 0 {
			t.Errorf("invalid generated task entry: %#v", task)
		}
	}
	for _, retirement := range loaded.Retirements {
		if _, ok := loaded.Retirement(retirement.Name, retirement.Major); !ok && retirement.ReplacementKind != "external" {
			t.Errorf("retirement %s does not resolve by task major and is not explicitly external", retirement.Task)
		}
	}
	for _, name := range []string{"AzureCLI", "DotNetCoreCLI", "NuGetCommand", "Npm", "PowerShell"} {
		major, ok := loaded.LatestMajor(name)
		if !ok {
			t.Errorf("LatestMajor(%q) not found", name)
			continue
		}
		task, _ := loaded.ResolveTask(name)
		version, ok := task.Version(major)
		if !ok || version.Deprecated || version.Preview {
			t.Errorf("LatestMajor(%q) selected unusable major %d: %#v", name, major, version)
		}
		if _, retired := loaded.Retirement(name, major); retired {
			t.Errorf("LatestMajor(%q) selected retired major %d", name, major)
		}
	}

	data, err := fs.ReadFile(shippedcatalog.FS, "github-actions/actions.lock.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var lock catalogmodel.ActionsLock
	if err := yaml.Unmarshal(data, &lock); err != nil {
		t.Fatal(err)
	}
	if len(lock.Actions) < 40 {
		t.Fatalf("usable actions = %d, want at least 40", len(lock.Actions))
	}
	sha := regexp.MustCompile(`^[0-9a-f]{40}$`)
	usable := make(map[string]struct{}, len(lock.Actions))
	for _, action := range lock.Actions {
		if !sha.MatchString(action.SHA) {
			t.Errorf("action %s has invalid SHA %q", action.Name, action.SHA)
		}
		if !catalogmodel.RuntimeAllowed(action.RunsUsing) {
			t.Errorf("action %s has rejected runtime %q", action.Name, action.RunsUsing)
		}
		usable[action.Name] = struct{}{}
	}
	for _, rejected := range lock.Rejected {
		if _, ok := usable[rejected.Name]; ok {
			t.Errorf("rejected action %s is also usable", rejected.Name)
		}
	}
}
