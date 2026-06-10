package homedweb

import (
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

const sampleJSON = `{
  "dashboards": [
    {
      "name": "РћС„РёСЃ",
      "blocks": [
        {
          "name": "РєРѕС‚РµР»",
          "items": [
            {"endpoint": "custom/61226326-10251872", "expose": "isDHWenabled", "name": "Р“Р’РЎ"},
            {"endpoint": "custom/61226326-10251872", "expose": "OTget25",      "name": "РџРѕРґР°С‡Р°"},
            {"endpoint": "custom/61226326-10251872", "expose": "OTget26"}
          ]
        }
      ]
    },
    {
      "name": "РљР»РёРјР°С‚",
      "blocks": [
        {
          "name": "РљР»РёРјР°С‚",
          "items": [
            {"endpoint": "custom/14705744-45074752", "expose": "setTemperature", "name": "рџЊЎ Р·Р°РґР°РЅРЅР°СЏ"}
          ]
        }
      ]
    }
  ],
  "names": {
    "custom/14705744-45074752/status_2": "рџљ°Р“Р’РЎ",
    "custom/27755404-43141976/status_15": "Р—РµСЂРєР°Р»Рѕ РґСѓС€"
  }
}`

// writeSample drops the sample JSON into a temporary file and
// returns its path. The caller is expected to clean it up.
func writeSample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "database.json")
	if err := os.WriteFile(p, []byte(sampleJSON), 0o600); err != nil {
		t.Fatalf("write sample: %v", err)
	}
	return p
}

func TestLoad_OK(t *testing.T) {
	p := writeSample(t)
	db, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if db == nil {
		t.Fatal("Load: nil db without error")
	}
	if len(db.Dashboards) != 2 {
		t.Fatalf("dashboards=%d want 2", len(db.Dashboards))
	}
	if db.Dashboards[0].Name != "РћС„РёСЃ" {
		t.Errorf("dashboards[0].name=%q", db.Dashboards[0].Name)
	}
	if len(db.Names) != 2 {
		t.Errorf("names=%d want 2", len(db.Names))
	}
}

func TestLoad_Missing(t *testing.T) {
	db, err := Load(filepath.Join(t.TempDir(), "no-such.json"))
	if err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if db != nil {
		t.Errorf("Load(missing): db=%+v", db)
	}
}

func TestLoad_EmptyPath(t *testing.T) {
	db, err := Load("")
	if err != nil {
		t.Fatalf("Load(\"\"): %v", err)
	}
	if db != nil {
		t.Errorf("Load(\"\"): db=%+v", db)
	}
}

func TestLoad_Malformed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(p, []byte(`{not json`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := Load(p); err == nil {
		t.Fatal("Load(bad): expected error, got nil")
	}
}

func TestProvider_Lookup(t *testing.T) {
	p := writeSample(t)
	pr, err := NewProvider(p)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	got := pr.Lookup("custom/61226326-10251872", "OTget25", "")
	want := []Match{{
		Dashboard: "РћС„РёСЃ",
		Block:     "РєРѕС‚РµР»",
		Item:      Item{Endpoint: "custom/61226326-10251872", Expose: "OTget25", Name: "РџРѕРґР°С‡Р°"},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Lookup(OTget25) = %+v, want %+v", got, want)
	}
}

func TestProvider_LookupEndpoint(t *testing.T) {
	p := writeSample(t)
	pr, _ := NewProvider(p)
	got := pr.LookupEndpoint("custom/61226326-10251872")
	if len(got) != 3 {
		t.Fatalf("LookupEndpoint: len=%d want 3 (%+v)", len(got), got)
	}
	for _, m := range got {
		if m.Dashboard != "РћС„РёСЃ" || m.Block != "РєРѕС‚РµР»" {
			t.Errorf("unexpected match: %+v", m)
		}
	}
}

func TestProvider_Lookup_NoMatch(t *testing.T) {
	p := writeSample(t)
	pr, _ := NewProvider(p)
	if got := pr.Lookup("unknown/device", "x", ""); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestProvider_LookupStatusName(t *testing.T) {
	p := writeSample(t)
	pr, _ := NewProvider(p)
	if got := pr.LookupStatusName("custom/14705744-45074752", "status_2"); got != "рџљ°Р“Р’РЎ" {
		t.Errorf("status name = %q want рџљ°Р“Р’РЎ", got)
	}
	if got := pr.LookupStatusName("custom/missing", "status_x"); got != "" {
		t.Errorf("missing status name = %q want \"\"", got)
	}
}

func TestProvider_Empty(t *testing.T) {
	pr, err := NewProvider("")
	if err != nil {
		t.Fatalf("NewProvider(\"\"): %v", err)
	}
	if pr.HasDatabase() {
		t.Error("HasDatabase()=true for empty provider")
	}
	if got := pr.Lookup("custom/61226326-10251872", "OTget25", ""); got != nil {
		t.Errorf("empty provider Lookup = %+v", got)
	}
}

func TestProvider_Load_Reload(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db.json")
	if err := os.WriteFile(p, []byte(sampleJSON), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	pr, err := NewProvider(p)
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	if !pr.HasDatabase() {
		t.Fatal("HasDatabase=false after load")
	}
	// Rewrite with a different content.
	other := `{"dashboards":[{"name":"X","blocks":[]}]}`
	if err := os.WriteFile(p, []byte(other), 0o600); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	if err := pr.Load(p); err != nil {
		t.Fatalf("Load: %v", err)
	}
	db := pr.Database()
	if db == nil || len(db.Dashboards) != 1 || db.Dashboards[0].Name != "X" {
		t.Errorf("reload: %+v", db)
	}
}

func TestProvider_Load_Missing(t *testing.T) {
	pr, err := NewProvider("")
	if err != nil {
		t.Fatalf("NewProvider: %v", err)
	}
	// Loading a missing file should clear the database (no error).
	if err := pr.Load(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("Load(missing): %v", err)
	}
	if pr.HasDatabase() {
		t.Error("HasDatabase=true after missing load")
	}
}

func TestProvider_Concurrent(t *testing.T) {
	p := writeSample(t)
	pr, _ := NewProvider(p)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = pr.Lookup("custom/61226326-10251872", "OTget25", "")
				_ = pr.LookupEndpoint("custom/61226326-10251872")
				_ = pr.LookupStatusName("custom/14705744-45074752", "status_2")
			}
		}()
	}
	wg.Wait()
}