package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestRewriteFiltersOnlyTouchesManagedProviders(t *testing.T) {
	providers := []string{
		"ai_us_primary", "ai_us_secondary", "ai_us_backup",
		"github_us_primary", "github_us_secondary", "github_us_backup",
	}
	var source strings.Builder
	source.WriteString("proxy-providers:\n")
	for _, name := range providers {
		source.WriteString("  " + name + ":\n")
		source.WriteString("    type: http\n")
		source.WriteString("    url: https://subscription.invalid/private-token\n")
		source.WriteString("    path: ./providers/" + name + ".yaml\n")
		source.WriteString("    filter: '^old$'\n")
		source.WriteString("    health-check:\n")
		source.WriteString("      enable: true\n")
	}
	source.WriteString("  household:\n")
	source.WriteString("    filter: 'keep-me'\n")
	source.WriteString("rules:\n")
	source.WriteString("  - MATCH,PROXY\n")

	mapping := map[string]string{
		"ai_us_primary":       "🇺🇸美国推荐A",
		"ai_us_secondary":     "🇺🇸美国推荐B",
		"ai_us_backup":        "🇺🇸美国备用C",
		"github_us_primary":   "🇺🇸美国推荐A",
		"github_us_secondary": "🇺🇸美国推荐B",
		"github_us_backup":    "🇺🇸美国备用C",
	}

	updated, err := rewriteFilters([]byte(source.String()), mapping)
	if err != nil {
		t.Fatalf("rewriteFilters returned error: %v", err)
	}
	text := string(updated)
	if strings.Count(text, "https://subscription.invalid/private-token") != len(providers) {
		t.Fatal("subscription URLs changed")
	}
	if !strings.Contains(text, "    filter: 'keep-me'") {
		t.Fatal("unmanaged provider changed")
	}
	if !strings.Contains(text, "  - MATCH,PROXY") {
		t.Fatal("unrelated rules changed")
	}
	for provider, node := range mapping {
		header := "  " + provider + ":"
		lines := strings.Split(text, "\n")
		start := -1
		for i, line := range lines {
			if line == header {
				start = i
				break
			}
		}
		if start == -1 {
			t.Fatalf("provider %s missing", provider)
		}
		blockLines := []string{lines[start]}
		for i := start + 1; i < len(lines); i++ {
			line := lines[i]
			if line != "" && !strings.HasPrefix(line, "    ") {
				break
			}
			blockLines = append(blockLines, line)
		}
		block := strings.Join(blockLines, "\n")
		expected := "    filter: '^" + regexp.QuoteMeta(node) + "$'"
		if !strings.Contains(block, expected) {
			t.Fatalf("provider %s did not receive exact escaped filter; block=%q", provider, block)
		}
	}
}

func TestRewriteFiltersFailsClosedWhenProviderMissing(t *testing.T) {
	source := []byte("proxy-providers:\n  ai_us_primary:\n    filter: '^old$'\n")
	_, err := rewriteFilters(source, map[string]string{
		"ai_us_primary":   "node-a",
		"ai_us_secondary": "node-b",
	})
	if err == nil {
		t.Fatal("expected missing provider to fail")
	}
}

func TestShortlistPrefersRecommendedUSNodesAndProtocol(t *testing.T) {
	nodes := []ProxyInfo{
		{Name: "🇺🇸美国普通", Type: "Hysteria2", Alive: true, History: []DelayHistory{{Delay: 90}}},
		{Name: "🇺🇸美国推荐慢", Type: "Hysteria2", Alive: true, History: []DelayHistory{{Delay: 200}}},
		{Name: "🇺🇸美国推荐快", Type: "Hysteria2", Alive: true, History: []DelayHistory{{Delay: 120}}},
		{Name: "🇯🇵日本推荐", Type: "Hysteria2", Alive: true, History: []DelayHistory{{Delay: 50}}},
		{Name: "🇺🇸美国推荐离线", Type: "Hysteria2", Alive: false, History: []DelayHistory{{Delay: 10}}},
		{Name: "🇺🇸美国VLESS推荐", Type: "Vless", Alive: true, History: []DelayHistory{{Delay: 80}}},
	}
	got := shortlist(nodes, "Hysteria2", 3)
	if len(got) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(got))
	}
	want := []string{"🇺🇸美国推荐快", "🇺🇸美国推荐慢", "🇺🇸美国普通"}
	for i := range want {
		if got[i].Name != want[i] {
			t.Fatalf("candidate %d: want %q, got %q", i, want[i], got[i].Name)
		}
	}
}

func TestParseOptionsDerivesPathsWithoutModelDefaults(t *testing.T) {
	t.Setenv("NODE_GUARD_SHELLCRASH_ROOT", "")
	mode, opts, err := parseOptions([]string{"--shellcrash-root", "/media/router/ShellClash"})
	if err != nil {
		t.Fatal(err)
	}
	if mode != "run" || opts.PersistentConfig != "/media/router/ShellClash/yamls/config.yaml" || opts.StateFile != "/media/router/ShellClash/tools/ai-node-guard-state.json" {
		t.Fatalf("unexpected derived options: mode=%s opts=%+v", mode, opts)
	}
	if _, _, err := parseOptions(nil); err == nil {
		t.Fatal("missing persistent root should fail closed")
	}
}

func TestLoadStateRejectsLegacyAndLoadsInitializedSchema(t *testing.T) {
	temporary := t.TempDir()
	previous := stateFile
	stateFile = filepath.Join(temporary, "state.json")
	t.Cleanup(func() { stateFile = previous })

	legacy := State{SchemaVersion: 1, Initialized: true, ExpectedNodes: NodeSet{Primary: "a", Secondary: "b", Backup: "c"}}
	data, _ := json.Marshal(legacy)
	if err := os.WriteFile(stateFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadState(); got.Initialized {
		t.Fatalf("legacy state must trigger a fresh baseline: %+v", got)
	}

	want := State{SchemaVersion: stateSchema, Version: version, Initialized: true, ExpectedNodes: NodeSet{Primary: "a", Secondary: "b", Backup: "c"}}
	data, _ = json.Marshal(want)
	if err := os.WriteFile(stateFile, data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := loadState(); !got.Initialized || got.ExpectedNodes != want.ExpectedNodes {
		t.Fatalf("initialized state was not loaded: %+v", got)
	}
}

func TestProbeNodeReportsPartialTargetFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "anthropic") {
			http.Error(w, "unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"delay":123}`))
	}))
	defer server.Close()
	previous := controllerURL
	controllerURL = server.URL
	t.Cleanup(func() { controllerURL = previous })
	c := &Controller{client: &http.Client{Timeout: time.Second}}
	stats, ok := c.probeNode("node-a", 1)
	if ok {
		t.Fatal("partial service failure must fail the multi-target probe")
	}
	if stats["anthropic"].Successes != 0 || stats["openai"].Successes != 1 || stats["github"].Successes != 1 {
		t.Fatalf("unexpected probe stats: %+v", stats)
	}
}

func TestShouldRepairAfterConsecutiveFailureOrImmediateFault(t *testing.T) {
	if shouldRepair(Evaluation{}, failureThreshold-1) {
		t.Fatal("a single ordinary failure must not switch")
	}
	if !shouldRepair(Evaluation{}, failureThreshold) {
		t.Fatal("consecutive failures must trigger repair")
	}
	if !shouldRepair(Evaluation{BothGroupsDown: true}, 1) {
		t.Fatal("both groups down must trigger immediate repair")
	}
	if !shouldRepair(Evaluation{MissingNames: []string{"renamed"}}, 1) {
		t.Fatal("renamed or missing nodes must trigger immediate repair")
	}
}

func TestRollbackRestoresBothFilesAndReportsSuccess(t *testing.T) {
	temporary := t.TempDir()
	files := []ManagedFile{
		{path: filepath.Join(temporary, "config.yaml"), backup: filepath.Join(temporary, "config.backup")},
		{path: filepath.Join(temporary, "template.yaml"), backup: filepath.Join(temporary, "template.backup")},
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, []byte("broken"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.backup, []byte("original"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/configs" {
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	previousURL, previousConfig := controllerURL, persistentConfig
	controllerURL, persistentConfig = server.URL, files[0].path
	t.Cleanup(func() { controllerURL, persistentConfig = previousURL, previousConfig })
	c := &Controller{client: &http.Client{Timeout: time.Second}}
	if err := c.rollback(files); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		data, err := os.ReadFile(file.path)
		if err != nil || string(data) != "original" {
			t.Fatalf("rollback did not restore %s: data=%q err=%v", file.path, data, err)
		}
	}
	if got := withRollbackResult(os.ErrInvalid, nil).Error(); !strings.Contains(got, "rolled back successfully") {
		t.Fatalf("rollback success was not observable: %s", got)
	}
}

func TestValidationArgumentsUseShellCrashDataDirectory(t *testing.T) {
	previous := persistentConfig
	persistentConfig = "/media/router/ShellClash/yamls/config.yaml"
	t.Cleanup(func() { persistentConfig = previous })
	got := validationArguments("/media/router/ShellClash/yamls/config.candidate")
	want := []string{"-t", "-d", "/media/router/ShellClash", "-f", "/media/router/ShellClash/yamls/config.candidate"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("unexpected validation arguments: %v", got)
	}
}

func TestExerciseSelectionUsesVerifiedAlternateOrder(t *testing.T) {
	current := NodeSet{Primary: "primary", Secondary: "secondary", Backup: "backup"}
	got, err := exerciseSelection(current, current, []CandidateResult{{Name: "primary", Type: "Hysteria2"}, {Name: "secondary", Type: "Hysteria2"}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Primary != "secondary" || got.Secondary != "primary" || got.Backup != "backup" {
		t.Fatalf("unexpected exercise order: %+v", got)
	}
}

func TestValidateManagedRuleCountsAllowsExtraUserRules(t *testing.T) {
	build := func(ai, github int) []RuleInfo {
		rules := make([]RuleInfo, 0, ai+github)
		for i := 0; i < ai; i++ {
			rules = append(rules, RuleInfo{Proxy: "AI-US-STABLE"})
		}
		for i := 0; i < github; i++ {
			rules = append(rules, RuleInfo{Proxy: "GITHUB-US"})
		}
		return rules
	}
	for _, tc := range []struct {
		ai, github int
		wantErr    bool
	}{
		{12, 8, false},
		{13, 8, false},
		{12, 9, false},
		{11, 8, true},
		{12, 7, true},
	} {
		err := validateManagedRuleCounts(build(tc.ai, tc.github))
		if (err != nil) != tc.wantErr {
			t.Errorf("AI=%d GitHub=%d: unexpected error %v", tc.ai, tc.github, err)
		}
	}
}
