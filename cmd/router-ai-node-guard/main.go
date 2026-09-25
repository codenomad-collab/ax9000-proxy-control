package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	version          = "1.1.2"
	stateSchema      = 2
	failureThreshold = 2
)

var (
	controllerURL    string
	runtimeConfig    string
	persistentConfig string
	templateConfig   string
	crashCore        string
	stateFile        string
	logFile          string
	lockFile         string
)

type runtimeOptions struct {
	ControllerURL    string
	ShellCrashRoot   string
	RuntimeConfig    string
	PersistentConfig string
	TemplateConfig   string
	CrashCore        string
	StateFile        string
	LogFile          string
	LockFile         string
}

var serviceProbes = []ProbeTarget{
	{Name: "openai", URL: "https://api.openai.com/v1/models", ExpectedStatus: "401"},
	{Name: "anthropic", URL: "https://api.anthropic.com/v1/messages", ExpectedStatus: "401"},
	{Name: "github", URL: "https://api.github.com/rate_limit", ExpectedStatus: "200"},
}

type NodeSet struct {
	Primary   string `json:"primary"`
	Secondary string `json:"secondary"`
	Backup    string `json:"backup"`
}

func (n NodeSet) Ordered() []string {
	return []string{n.Primary, n.Secondary, n.Backup}
}

type State struct {
	SchemaVersion       int                   `json:"schema_version"`
	Version             string                `json:"guard_version"`
	Initialized         bool                  `json:"initialized"`
	BaselineAt          string                `json:"baseline_at,omitempty"`
	LastCheck           string                `json:"last_check"`
	Status              string                `json:"status"`
	ConsecutiveFailures int                   `json:"consecutive_failures"`
	ExpectedNodes       NodeSet               `json:"expected_nodes"`
	LastIssue           string                `json:"last_issue,omitempty"`
	LastRepair          string                `json:"last_repair,omitempty"`
	LastRollback        string                `json:"last_rollback,omitempty"`
	RollbackStatus      string                `json:"rollback_status,omitempty"`
	LastExercise        string                `json:"last_exercise,omitempty"`
	ExerciseStatus      string                `json:"exercise_status,omitempty"`
	Checks              map[string]ProbeStats `json:"checks,omitempty"`
}

type ProbeTarget struct {
	Name           string
	URL            string
	ExpectedStatus string
}

type ProbeStats struct {
	SamplesMS []int `json:"samples_ms"`
	Successes int   `json:"successes"`
	MedianMS  int   `json:"median_ms,omitempty"`
	SpreadMS  int   `json:"spread_ms,omitempty"`
}

type DelayHistory struct {
	Delay int `json:"delay"`
}

type ProxyInfo struct {
	Name    string         `json:"name"`
	Type    string         `json:"type"`
	Now     string         `json:"now"`
	All     []string       `json:"all"`
	Alive   bool           `json:"alive"`
	History []DelayHistory `json:"history"`
}

type ProxiesResponse struct {
	Proxies map[string]ProxyInfo `json:"proxies"`
}

type ProviderInfo struct {
	Name    string      `json:"name"`
	Type    string      `json:"type"`
	Proxies []ProxyInfo `json:"proxies"`
}

type ProvidersResponse struct {
	Providers map[string]ProviderInfo `json:"providers"`
}

type RuleInfo struct {
	Type    string `json:"type"`
	Payload string `json:"payload"`
	Proxy   string `json:"proxy"`
}

type RulesResponse struct {
	Rules []RuleInfo `json:"rules"`
}

type ConnectionMetadata struct {
	Host string `json:"host"`
}

type ConnectionInfo struct {
	ID          string             `json:"id"`
	Metadata    ConnectionMetadata `json:"metadata"`
	Rule        string             `json:"rule"`
	RulePayload string             `json:"rulePayload"`
	Chains      []string           `json:"chains"`
}

type ConnectionsResponse struct {
	Connections []ConnectionInfo `json:"connections"`
}

type Snapshot struct {
	Proxies   ProxiesResponse
	Providers ProvidersResponse
}

type Evaluation struct {
	Healthy          bool
	Issues           []string
	MissingNames     []string
	ManagedGroupDown bool
	Checks           map[string]ProbeStats
}

type CandidateResult struct {
	Name     string
	Type     string
	Stats    map[string]ProbeStats
	Score    int
	MedianMS int
	SpreadMS int
}

type ManagedFile struct {
	path      string
	candidate string
	backup    string
}

type Controller struct {
	secret string
	client *http.Client
}

func main() {
	mode, opts, err := parseOptions(os.Args[1:])
	if err != nil {
		fatalf("configuration: %v", err)
	}
	if mode == "version" {
		fmt.Println(version)
		return
	}
	applyRuntimeOptions(opts)
	if mode == "status" {
		data, readErr := os.ReadFile(stateFile)
		if readErr != nil {
			fatalf("read state: %v", readErr)
		}
		fmt.Print(string(data))
		return
	}

	lock, err := acquireLock()
	if err != nil {
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return
		}
		fatalf("lock: %v", err)
	}
	defer lock.Close()

	if mode == "exercise-switch" {
		err = runSwitchExercise()
	} else {
		err = run()
	}
	if err != nil {
		logf("ERROR %v", err)
		os.Exit(1)
	}
}

func parseOptions(args []string) (string, runtimeOptions, error) {
	mode := "run"
	filtered := make([]string, 0, len(args))
	for _, arg := range args {
		switch arg {
		case "--version":
			mode = "version"
		case "--status":
			mode = "status"
		case "--exercise-switch":
			mode = "exercise-switch"
		default:
			filtered = append(filtered, arg)
		}
	}
	if mode == "version" {
		return mode, runtimeOptions{}, nil
	}

	env := func(name string) string { return strings.TrimSpace(os.Getenv(name)) }
	rootDefault := env("NODE_GUARD_SHELLCRASH_ROOT")
	if rootDefault == "" {
		rootDefault = discoverShellCrashRootFromExecutable()
	}
	fs := flag.NewFlagSet("router-ai-node-guard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := runtimeOptions{}
	fs.StringVar(&opts.ControllerURL, "controller-url", firstNonEmpty(env("NODE_GUARD_CONTROLLER_URL"), "http://127.0.0.1:9097"), "Mihomo controller URL")
	fs.StringVar(&opts.ShellCrashRoot, "shellcrash-root", rootDefault, "ShellCrash persistent root")
	fs.StringVar(&opts.RuntimeConfig, "runtime-config", env("NODE_GUARD_RUNTIME_CONFIG"), "runtime Mihomo config")
	fs.StringVar(&opts.PersistentConfig, "persistent-config", env("NODE_GUARD_PERSISTENT_CONFIG"), "persistent Mihomo config")
	fs.StringVar(&opts.TemplateConfig, "template-config", env("NODE_GUARD_TEMPLATE_CONFIG"), "template Mihomo config")
	fs.StringVar(&opts.CrashCore, "crash-core", env("NODE_GUARD_CRASH_CORE"), "Mihomo binary used for config validation")
	fs.StringVar(&opts.StateFile, "state-file", env("NODE_GUARD_STATE_FILE"), "guard state path")
	fs.StringVar(&opts.LogFile, "log-file", env("NODE_GUARD_LOG_FILE"), "guard log path")
	fs.StringVar(&opts.LockFile, "lock-file", firstNonEmpty(env("NODE_GUARD_LOCK_FILE"), "/tmp/router-ai-node-guard.lock"), "process lock path")
	if err := fs.Parse(filtered); err != nil {
		return "", runtimeOptions{}, err
	}
	if len(fs.Args()) != 0 {
		return "", runtimeOptions{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.ShellCrashRoot == "" && (opts.PersistentConfig == "" || opts.TemplateConfig == "" || opts.StateFile == "" || opts.LogFile == "") {
		return "", runtimeOptions{}, errors.New("set --shellcrash-root or explicitly provide persistent, template, state and log paths")
	}
	if opts.RuntimeConfig == "" {
		opts.RuntimeConfig = "/tmp/ShellCrash/config.yaml"
	}
	if opts.CrashCore == "" {
		opts.CrashCore = "/tmp/ShellCrash/CrashCore"
	}
	if opts.ShellCrashRoot != "" {
		opts.PersistentConfig = firstNonEmpty(opts.PersistentConfig, filepath.Join(opts.ShellCrashRoot, "yamls", "config.yaml"))
		opts.TemplateConfig = firstNonEmpty(opts.TemplateConfig, filepath.Join(opts.ShellCrashRoot, "yamls", "config.yaml.template"))
		opts.StateFile = firstNonEmpty(opts.StateFile, filepath.Join(opts.ShellCrashRoot, "tools", "ai-node-guard-state.json"))
		opts.LogFile = firstNonEmpty(opts.LogFile, filepath.Join(opts.ShellCrashRoot, "logs", "ai-node-guard.log"))
	}
	return mode, opts, nil
}

func discoverShellCrashRootFromExecutable() string {
	executable, err := os.Executable()
	if err != nil {
		return ""
	}
	toolsDirectory := filepath.Dir(executable)
	if filepath.Base(toolsDirectory) != "tools" {
		return ""
	}
	root := filepath.Clean(filepath.Join(toolsDirectory, ".."))
	if info, err := os.Stat(filepath.Join(root, "configs", "ShellCrash.cfg")); err == nil && info.Mode().IsRegular() {
		return root
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func applyRuntimeOptions(opts runtimeOptions) {
	controllerURL = strings.TrimRight(opts.ControllerURL, "/")
	runtimeConfig = opts.RuntimeConfig
	persistentConfig = opts.PersistentConfig
	templateConfig = opts.TemplateConfig
	crashCore = opts.CrashCore
	stateFile = opts.StateFile
	logFile = opts.LogFile
	lockFile = opts.LockFile
}

func run() error {
	state := loadState()
	secret, err := readControllerSecret(runtimeConfig)
	if err != nil {
		secret, err = readControllerSecret(persistentConfig)
		if err != nil {
			return fmt.Errorf("controller credential unavailable: %w", err)
		}
	}
	controller := &Controller{
		secret: secret,
		client: &http.Client{Timeout: 8 * time.Second},
	}

	snapshot, err := controller.snapshot()
	if err != nil {
		return recordFailure(&state, fmt.Sprintf("controller snapshot failed: %v", err), nil)
	}
	if !state.Initialized {
		return controller.establishBaseline(&state, snapshot)
	}
	evaluation := controller.evaluate(snapshot, state.ExpectedNodes, 1)
	if evaluation.Healthy {
		recordHealthy(&state, evaluation.Checks)
		fmt.Println("healthy")
		return nil
	}

	logf("WARN initial check: %s", strings.Join(evaluation.Issues, "; "))
	controller.refreshProviders()
	time.Sleep(4 * time.Second)
	snapshot, refreshErr := controller.snapshot()
	if refreshErr == nil {
		evaluation = controller.evaluate(snapshot, state.ExpectedNodes, 1)
		if evaluation.Healthy {
			recordHealthy(&state, evaluation.Checks)
			logf("RECOVERED provider refresh restored health")
			return nil
		}
	}

	state.ConsecutiveFailures++
	state.LastCheck = nowString()
	state.Status = "degraded"
	state.LastIssue = strings.Join(evaluation.Issues, "; ")
	state.Checks = evaluation.Checks
	if err := saveState(state); err != nil {
		return err
	}

	if !shouldRepair(evaluation, state.ConsecutiveFailures) {
		logf("WARN waiting for confirmation, consecutive_failures=%d", state.ConsecutiveFailures)
		return nil
	}

	if refreshErr != nil {
		return fmt.Errorf("cannot repair without fresh provider data: %w", refreshErr)
	}
	selection, results, err := controller.selectReplacement(snapshot.Providers)
	for _, result := range results {
		logf("CANDIDATE name=%q type=%s score=%d median=%d spread=%d", result.Name, result.Type, result.Score, result.MedianMS, result.SpreadMS)
	}
	if err != nil {
		state.Status = "attention"
		state.LastIssue = err.Error()
		saveState(state)
		return fmt.Errorf("automatic replacement withheld: %w", err)
	}

	if selection == state.ExpectedNodes {
		return fmt.Errorf("selected nodes are unchanged but health remains degraded")
	}
	logf("REPAIR selected primary=%q secondary=%q backup=%q", selection.Primary, selection.Secondary, selection.Backup)
	if err := controller.applySelection(selection); err != nil {
		state.Status = "attention"
		state.LastIssue = err.Error()
		if strings.Contains(err.Error(), "rolled back") {
			state.LastRollback = nowString()
			state.RollbackStatus = rollbackStatusFromError(err)
		}
		if saveErr := saveState(state); saveErr != nil {
			return fmt.Errorf("%w; save state: %v", err, saveErr)
		}
		return err
	}

	state.ExpectedNodes = selection
	state.ConsecutiveFailures = 0
	state.LastCheck = nowString()
	state.Status = "healthy"
	state.LastIssue = ""
	state.LastRepair = nowString()
	state.RollbackStatus = ""
	state.Checks = nil
	if err := saveState(state); err != nil {
		return err
	}
	logf("REPAIRED configuration, template and live traffic verified")
	return nil
}

func shouldRepair(evaluation Evaluation, consecutiveFailures int) bool {
	return len(evaluation.MissingNames) > 0 || evaluation.ManagedGroupDown || consecutiveFailures >= failureThreshold
}

func runSwitchExercise() error {
	state := loadState()
	if !state.Initialized {
		return errors.New("switch exercise requires an initialized baseline")
	}
	secret, err := readControllerSecret(runtimeConfig)
	if err != nil {
		secret, err = readControllerSecret(persistentConfig)
		if err != nil {
			return fmt.Errorf("controller credential unavailable: %w", err)
		}
	}
	controller := &Controller{secret: secret, client: &http.Client{Timeout: 8 * time.Second}}
	snapshot, err := controller.snapshot()
	if err != nil {
		return fmt.Errorf("exercise snapshot: %w", err)
	}
	current, ok := nodeSetFromSnapshot(snapshot)
	if !ok {
		return errors.New("exercise cannot determine the active three-node order")
	}
	selection, results, err := controller.selectReplacementWithPolicy(snapshot.Providers, 6, 2)
	if err != nil {
		return fmt.Errorf("exercise candidate selection: %w", err)
	}
	alternative, err := exerciseSelection(current, selection, results)
	if err != nil {
		return err
	}

	logf("EXERCISE applying a verified alternate order")
	if err := controller.applySelection(alternative); err != nil {
		state.LastExercise = nowString()
		state.ExerciseStatus = "alternate_failed"
		state.LastIssue = err.Error()
		if strings.Contains(err.Error(), "rolled back") {
			state.LastRollback = nowString()
			state.RollbackStatus = rollbackStatusFromError(err)
		}
		_ = saveState(state)
		return fmt.Errorf("exercise alternate switch: %w", err)
	}
	logf("EXERCISE restoring the original verified order")
	if err := controller.applySelection(current); err != nil {
		state.LastExercise = nowString()
		state.ExerciseStatus = "restore_failed"
		state.ExpectedNodes = alternative
		state.LastIssue = err.Error()
		if strings.Contains(err.Error(), "rolled back") {
			state.LastRollback = nowString()
			state.RollbackStatus = rollbackStatusFromError(err)
		}
		_ = saveState(state)
		return fmt.Errorf("exercise restore: %w", err)
	}

	state.ExpectedNodes = current
	state.LastExercise = nowString()
	state.ExerciseStatus = "passed"
	state.LastCheck = state.LastExercise
	state.Status = "healthy"
	state.ConsecutiveFailures = 0
	state.LastIssue = ""
	state.Checks = nil
	if err := saveState(state); err != nil {
		return err
	}
	logf("EXERCISE alternate switch and original-order restore passed")
	fmt.Println("switch-exercise-passed")
	return nil
}

func exerciseSelection(current, selected NodeSet, results []CandidateResult) (NodeSet, error) {
	if selected != current {
		return selected, nil
	}
	// Swapping two independently verified Hysteria2 lines changes the active
	// fallback order without introducing an untested node.
	if current.Primary != "" && current.Secondary != "" && current.Primary != current.Secondary {
		return NodeSet{Primary: current.Secondary, Secondary: current.Primary, Backup: current.Backup}, nil
	}
	verified := make([]string, 0, len(results))
	for _, result := range results {
		if result.Type == "Hysteria2" && result.Name != current.Primary {
			verified = append(verified, result.Name)
		}
	}
	if len(verified) > 0 {
		return NodeSet{Primary: verified[0], Secondary: current.Primary, Backup: current.Backup}, nil
	}
	return NodeSet{}, errors.New("exercise has no distinct verified alternate order")
}

func (c *Controller) establishBaseline(state *State, snapshot Snapshot) error {
	selection, results, err := c.selectReplacementWithPolicy(snapshot.Providers, 0, 1)
	for _, result := range results {
		logf("BASELINE candidate name=%q type=%s score=%d median=%d spread=%d", result.Name, result.Type, result.Score, result.MedianMS, result.SpreadMS)
	}
	if err != nil {
		state.Status = "attention"
		state.LastCheck = nowString()
		state.LastIssue = "baseline failed: " + err.Error()
		if saveErr := saveState(*state); saveErr != nil {
			return fmt.Errorf("%s; save state: %v", state.LastIssue, saveErr)
		}
		return errors.New(state.LastIssue)
	}

	current, currentOK := nodeSetFromSnapshot(snapshot)
	if !currentOK || current != selection {
		if err := c.applySelection(selection); err != nil {
			state.Status = "attention"
			state.LastCheck = nowString()
			state.LastIssue = "baseline apply failed: " + err.Error()
			if strings.Contains(err.Error(), "rolled back") {
				state.LastRollback = nowString()
				state.RollbackStatus = rollbackStatusFromError(err)
			}
			if saveErr := saveState(*state); saveErr != nil {
				return fmt.Errorf("%s; save state: %v", state.LastIssue, saveErr)
			}
			return errors.New(state.LastIssue)
		}
	}

	state.SchemaVersion = stateSchema
	state.Version = version
	state.Initialized = true
	state.BaselineAt = nowString()
	state.LastCheck = state.BaselineAt
	state.Status = "healthy"
	state.ConsecutiveFailures = 0
	state.ExpectedNodes = selection
	state.LastIssue = ""
	state.Checks = statsForCandidate(results, selection.Primary)
	if err := saveState(*state); err != nil {
		return err
	}
	logf("BASELINE established after probing all eligible US candidates")
	fmt.Println("baseline-established")
	return nil
}

func nodeSetFromSnapshot(snapshot Snapshot) (NodeSet, bool) {
	group, ok := snapshot.Proxies.Proxies["AI-US-STABLE"]
	if !ok || len(group.All) != 3 {
		return NodeSet{}, false
	}
	return NodeSet{Primary: group.All[0], Secondary: group.All[1], Backup: group.All[2]}, true
}

func statsForCandidate(results []CandidateResult, name string) map[string]ProbeStats {
	for _, result := range results {
		if result.Name == name {
			return result.Stats
		}
	}
	return nil
}

func acquireLock() (*os.File, error) {
	f, err := os.OpenFile(lockFile, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func loadState() State {
	state := State{SchemaVersion: stateSchema, Version: version}
	data, err := os.ReadFile(stateFile)
	if err == nil {
		var persisted State
		if json.Unmarshal(data, &persisted) == nil && persisted.SchemaVersion == stateSchema && persisted.Initialized && persisted.ExpectedNodes.Primary != "" && persisted.ExpectedNodes.Secondary != "" && persisted.ExpectedNodes.Backup != "" {
			state = persisted
		}
	}
	state.SchemaVersion = stateSchema
	state.Version = version
	return state
}

func saveState(state State) error {
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeAtomic(stateFile, data, 0600)
}

func recordHealthy(state *State, checks map[string]ProbeStats) {
	wasUnhealthy := state.Status != "" && state.Status != "healthy"
	state.LastCheck = nowString()
	state.Status = "healthy"
	state.ConsecutiveFailures = 0
	state.LastIssue = ""
	state.Checks = checks
	if err := saveState(*state); err != nil {
		logf("ERROR save healthy state: %v", err)
	}
	if wasUnhealthy {
		logf("RECOVERED routine health check passed")
	}
}

func recordFailure(state *State, issue string, checks map[string]ProbeStats) error {
	state.LastCheck = nowString()
	state.Status = "degraded"
	state.ConsecutiveFailures++
	state.LastIssue = issue
	state.Checks = checks
	if err := saveState(*state); err != nil {
		return fmt.Errorf("%s; save state: %v", issue, err)
	}
	return errors.New(issue)
}

func readControllerSecret(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "secret:") {
			continue
		}
		value := strings.TrimSpace(strings.TrimPrefix(line, "secret:"))
		if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
			value = strings.ReplaceAll(value[1:len(value)-1], "''", "'")
		} else if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
			if unquoted, unquoteErr := strconv.Unquote(value); unquoteErr == nil {
				value = unquoted
			}
		}
		if value == "" {
			return "", errors.New("empty secret")
		}
		return value, nil
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", errors.New("secret field not found")
}

func (c *Controller) request(method, path string, body []byte, output any) error {
	req, err := http.NewRequest(method, controllerURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("%s %s returned HTTP %d", method, path, resp.StatusCode)
	}
	if output == nil || resp.StatusCode == http.StatusNoContent {
		io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(output)
}

func (c *Controller) snapshot() (Snapshot, error) {
	var snapshot Snapshot
	if err := c.request(http.MethodGet, "/proxies", nil, &snapshot.Proxies); err != nil {
		return snapshot, err
	}
	if err := c.request(http.MethodGet, "/providers/proxies", nil, &snapshot.Providers); err != nil {
		return snapshot, err
	}
	return snapshot, nil
}

func (c *Controller) evaluate(snapshot Snapshot, expected NodeSet, rounds int) Evaluation {
	result := Evaluation{Healthy: true, Checks: map[string]ProbeStats{}}
	order := expected.Ordered()
	ai, ok := snapshot.Proxies.Proxies["AI-US-STABLE"]
	if !ok {
		result.Issues = append(result.Issues, "AI-US-STABLE missing")
	} else {
		if ai.Type != "Fallback" || !sameStrings(ai.All, order) {
			result.Issues = append(result.Issues, "AI-US-STABLE structure/order mismatch")
		}
		if !ai.Alive {
			result.Issues = append(result.Issues, "AI-US-STABLE unavailable")
		}
	}
	result.ManagedGroupDown = !ai.Alive

	expectedProviders := map[string]string{
		"ai_us_primary": expected.Primary, "ai_us_secondary": expected.Secondary, "ai_us_backup": expected.Backup,
	}
	for providerName, nodeName := range expectedProviders {
		provider, ok := snapshot.Providers.Providers[providerName]
		if !ok || len(provider.Proxies) != 1 || provider.Proxies[0].Name != nodeName || !provider.Proxies[0].Alive {
			result.Issues = append(result.Issues, providerName+" unhealthy or renamed")
		}
	}

	household, ok := snapshot.Providers.Providers["household"]
	if !ok {
		result.Issues = append(result.Issues, "household provider missing")
	} else {
		names := make(map[string]bool, len(household.Proxies))
		for _, node := range household.Proxies {
			names[node.Name] = true
		}
		for _, name := range order {
			if !names[name] {
				result.MissingNames = append(result.MissingNames, name)
			}
		}
		if len(result.MissingNames) > 0 {
			result.Issues = append(result.Issues, "subscription names missing: "+strings.Join(result.MissingNames, " | "))
		}
	}

	if expected.Primary != "" {
		stats, probeOK := c.probeNode(expected.Primary, rounds)
		result.Checks = stats
		if !probeOK {
			result.Issues = append(result.Issues, "primary multi-service probe failed")
		}
	}
	result.Healthy = len(result.Issues) == 0
	return result
}

func (c *Controller) probeNode(name string, rounds int) (map[string]ProbeStats, bool) {
	stats := make(map[string]ProbeStats, len(serviceProbes))
	allOK := true
	for _, target := range serviceProbes {
		values := make([]int, 0, rounds)
		for i := 0; i < rounds; i++ {
			delay, err := c.delay(name, target)
			if err == nil && delay > 0 {
				values = append(values, delay)
			}
			if rounds > 1 {
				time.Sleep(120 * time.Millisecond)
			}
		}
		stat := ProbeStats{SamplesMS: values, Successes: len(values)}
		if len(values) > 0 {
			ordered := append([]int(nil), values...)
			sort.Ints(ordered)
			stat.MedianMS = median(ordered)
			stat.SpreadMS = ordered[len(ordered)-1] - ordered[0]
		}
		if len(values) != rounds {
			allOK = false
		}
		stats[target.Name] = stat
	}
	return stats, allOK
}

func (c *Controller) delay(name string, target ProbeTarget) (int, error) {
	query := url.Values{}
	query.Set("url", target.URL)
	query.Set("timeout", "5000")
	query.Set("expected", target.ExpectedStatus)
	path := "/proxies/" + url.PathEscape(name) + "/delay?" + query.Encode()
	var response struct {
		Delay int `json:"delay"`
	}
	if err := c.request(http.MethodGet, path, nil, &response); err != nil {
		return 0, err
	}
	return response.Delay, nil
}

func (c *Controller) refreshProviders() {
	providers := []string{
		"household", "ai_us_primary", "ai_us_secondary", "ai_us_backup",
	}
	for _, name := range providers {
		path := "/providers/proxies/" + url.PathEscape(name)
		if err := c.request(http.MethodPut, path, nil, nil); err != nil {
			logf("WARN provider refresh %s: %v", name, err)
		}
	}
}

func (c *Controller) selectReplacement(providers ProvidersResponse) (NodeSet, []CandidateResult, error) {
	return c.selectReplacementWithPolicy(providers, 6, 3)
}

func (c *Controller) selectReplacementWithPolicy(providers ProvidersResponse, limit, rounds int) (NodeSet, []CandidateResult, error) {
	household, ok := providers.Providers["household"]
	if !ok {
		return NodeSet{}, nil, errors.New("household provider unavailable")
	}
	hysteria := shortlist(household.Proxies, "Hysteria2", limit)
	vless := shortlist(household.Proxies, "Vless", limit)
	if len(hysteria) < 2 || len(vless) < 1 {
		return NodeSet{}, nil, fmt.Errorf("insufficient US protocol diversity: hysteria=%d vless=%d", len(hysteria), len(vless))
	}

	results := make([]CandidateResult, 0, len(hysteria)+len(vless))
	for _, node := range append(hysteria, vless...) {
		stats, ok := c.probeNode(node.Name, rounds)
		if !ok {
			continue
		}
		values := make([]int, 0, len(serviceProbes)*rounds)
		for _, target := range serviceProbes {
			values = append(values, stats[target.Name].SamplesMS...)
		}
		sort.Ints(values)
		med := median(values)
		spread := values[len(values)-1] - values[0]
		if med > 1500 || spread > 1200 {
			continue
		}
		results = append(results, CandidateResult{
			Name: node.Name, Type: node.Type, Stats: stats,
			MedianMS: med, SpreadMS: spread, Score: med + spread/3,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score == results[j].Score {
			return results[i].Name < results[j].Name
		}
		return results[i].Score < results[j].Score
	})

	selectedHysteria := make([]CandidateResult, 0, 2)
	selectedVless := make([]CandidateResult, 0, 1)
	for _, candidate := range results {
		if candidate.Type == "Hysteria2" && len(selectedHysteria) < 2 {
			selectedHysteria = append(selectedHysteria, candidate)
		}
		if strings.EqualFold(candidate.Type, "Vless") && len(selectedVless) < 1 {
			selectedVless = append(selectedVless, candidate)
		}
	}
	if len(selectedHysteria) < 2 || len(selectedVless) < 1 {
		return NodeSet{}, results, fmt.Errorf("no safe replacement set after probing: hysteria=%d vless=%d", len(selectedHysteria), len(selectedVless))
	}
	selection := NodeSet{
		Primary: selectedHysteria[0].Name, Secondary: selectedHysteria[1].Name, Backup: selectedVless[0].Name,
	}
	return selection, results, nil
}

func shortlist(nodes []ProxyInfo, wantedType string, limit int) []ProxyInfo {
	preferred := make([]ProxyInfo, 0)
	others := make([]ProxyInfo, 0)
	for _, node := range nodes {
		if !node.Alive || !strings.EqualFold(node.Type, wantedType) || (!strings.Contains(node.Name, "美国") && !strings.Contains(node.Name, "🇺🇸")) {
			continue
		}
		if strings.Contains(node.Name, "推荐") {
			preferred = append(preferred, node)
		} else {
			others = append(others, node)
		}
	}
	sortByHistory(preferred)
	sortByHistory(others)
	combined := append(preferred, others...)
	if limit > 0 && len(combined) > limit {
		combined = combined[:limit]
	}
	return combined
}

func sortByHistory(nodes []ProxyInfo) {
	sort.SliceStable(nodes, func(i, j int) bool {
		return lastDelay(nodes[i]) < lastDelay(nodes[j])
	})
}

func lastDelay(node ProxyInfo) int {
	for i := len(node.History) - 1; i >= 0; i-- {
		if node.History[i].Delay > 0 {
			return node.History[i].Delay
		}
	}
	return 1 << 30
}

func (c *Controller) applySelection(selection NodeSet) error {
	mapping := map[string]string{
		"ai_us_primary": selection.Primary, "ai_us_secondary": selection.Secondary, "ai_us_backup": selection.Backup,
	}
	timestamp := time.Now().Format("20060102-150405")
	files := []ManagedFile{
		{path: persistentConfig, candidate: persistentConfig + ".ai-guard-candidate", backup: persistentConfig + ".bak.ai-guard-" + timestamp},
		{path: templateConfig, candidate: templateConfig + ".ai-guard-candidate", backup: templateConfig + ".bak.ai-guard-" + timestamp},
	}
	cleanup := func() {
		for _, file := range files {
			os.Remove(file.candidate)
		}
	}
	defer cleanup()

	for _, file := range files {
		original, err := os.ReadFile(file.path)
		if err != nil {
			return err
		}
		updated, err := rewriteFilters(original, mapping)
		if err != nil {
			return fmt.Errorf("rewrite %s: %w", file.path, err)
		}
		if err := writeAtomic(file.candidate, updated, 0600); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		output, err := exec.CommandContext(ctx, crashCore, validationArguments(file.candidate)...).CombinedOutput()
		cancel()
		if err != nil {
			return fmt.Errorf("validate %s failed: %v (%s)", file.path, err, conciseOutput(output))
		}
	}

	for _, file := range files {
		if err := copyFile(file.path, file.backup, 0600); err != nil {
			return fmt.Errorf("backup %s: %w", file.path, err)
		}
	}
	for _, file := range files {
		if err := os.Rename(file.candidate, file.path); err != nil {
			rollbackErr := c.rollback(files)
			return withRollbackResult(fmt.Errorf("replace %s: %w", file.path, err), rollbackErr)
		}
	}
	if err := c.reload(); err != nil {
		return withRollbackResult(fmt.Errorf("reload failed: %w", err), c.rollback(files))
	}
	time.Sleep(5 * time.Second)
	if err := c.verifyApplied(selection); err != nil {
		return withRollbackResult(fmt.Errorf("verification failed: %w", err), c.rollback(files))
	}
	pruneBackups(filepath.Dir(persistentConfig), filepath.Base(persistentConfig)+".bak.ai-guard-", 5)
	pruneBackups(filepath.Dir(templateConfig), filepath.Base(templateConfig)+".bak.ai-guard-", 5)
	return nil
}

func validationArguments(candidate string) []string {
	dataDirectory := filepath.Dir(filepath.Dir(persistentConfig))
	return []string{"-t", "-d", dataDirectory, "-f", candidate}
}

func rewriteFilters(input []byte, mapping map[string]string) ([]byte, error) {
	lines := strings.SplitAfter(string(input), "\n")
	replaced := make(map[string]int, len(mapping))
	current := ""
	headerRE := regexp.MustCompile(`^  ([A-Za-z0-9_]+):\s*$`)
	for i, line := range lines {
		trimmedNewline := strings.TrimSuffix(line, "\n")
		if matches := headerRE.FindStringSubmatch(trimmedNewline); matches != nil {
			if _, ok := mapping[matches[1]]; ok {
				current = matches[1]
			} else {
				current = ""
			}
			continue
		}
		if current != "" && strings.HasPrefix(line, "    filter:") {
			pattern := "^" + regexp.QuoteMeta(mapping[current]) + "$"
			pattern = strings.ReplaceAll(pattern, "'", "''")
			newline := ""
			if strings.HasSuffix(line, "\n") {
				newline = "\n"
			}
			lines[i] = "    filter: '" + pattern + "'" + newline
			replaced[current]++
		}
	}
	for provider := range mapping {
		if replaced[provider] != 1 {
			return nil, fmt.Errorf("provider %s filter replacements=%d", provider, replaced[provider])
		}
	}
	return []byte(strings.Join(lines, "")), nil
}

func (c *Controller) reload() error {
	body, _ := json.Marshal(map[string]string{"path": persistentConfig})
	return c.request(http.MethodPut, "/configs?force=true", body, nil)
}

func (c *Controller) rollback(files []ManagedFile) error {
	var failures []string
	for _, file := range files {
		if err := copyFile(file.backup, file.path, 0600); err != nil {
			logf("ERROR rollback %s: %v", file.path, err)
			failures = append(failures, filepath.Base(file.path)+": "+err.Error())
		}
	}
	if err := c.reload(); err != nil {
		logf("ERROR rollback reload: %v", err)
		failures = append(failures, "reload: "+err.Error())
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	logf("ROLLBACK completed successfully")
	return nil
}

func withRollbackResult(cause, rollbackErr error) error {
	if rollbackErr != nil {
		return fmt.Errorf("%w; rollback failed: %v", cause, rollbackErr)
	}
	return fmt.Errorf("%w; rolled back successfully", cause)
}

func rollbackStatusFromError(err error) string {
	if strings.Contains(err.Error(), "rollback failed") {
		return "failed"
	}
	return "successful"
}

func (c *Controller) verifyApplied(expected NodeSet) error {
	snapshot, err := c.snapshot()
	if err != nil {
		return err
	}
	evaluation := c.evaluate(snapshot, expected, 1)
	if !evaluation.Healthy {
		return errors.New(strings.Join(evaluation.Issues, "; "))
	}
	var rules RulesResponse
	if err := c.request(http.MethodGet, "/rules", nil, &rules); err != nil {
		return err
	}
	if err := validateManagedRules(rules.Rules); err != nil {
		return err
	}
	flows := []struct{ label, host, targetURL, group string }{
		{"ChatGPT", "chatgpt.com", "https://chatgpt.com/?ai_guard_verify=1", "AI-US-STABLE"},
		{"Claude", "claude.ai", "https://claude.ai/?ai_guard_verify=1", "AI-US-STABLE"},
		{"GitHub", "github.com", "https://github.com/MetaCubeX/metacubexd/releases/download/v1.273.1/compressed-dist.tgz", "AI-US-STABLE"},
	}
	for _, flow := range flows {
		if err := c.verifyFlow(flow.host, flow.targetURL, flow.group); err != nil {
			return fmt.Errorf("%s flow: %w", flow.label, err)
		}
	}
	return nil
}

var githubDomains = []string{
	"github.com", "githubusercontent.com", "githubassets.com", "githubcopilot.com",
	"github.io", "github.dev", "ghcr.io", "git.io",
}

func validateManagedRules(rules []RuleInfo) error {
	managedCount := 0
	githubSeen := make(map[string]bool, len(githubDomains))
	githubExpected := make(map[string]bool, len(githubDomains))
	for _, domain := range githubDomains {
		githubExpected[domain] = true
	}
	for _, rule := range rules {
		if rule.Proxy == "GITHUB-US" {
			return fmt.Errorf("legacy GITHUB-US rule remains: %s", rule.Payload)
		}
		if rule.Proxy == "AI-US-STABLE" {
			managedCount++
		}
		if rule.Type == "DomainSuffix" && githubExpected[rule.Payload] {
			if rule.Proxy != "AI-US-STABLE" {
				return fmt.Errorf("GitHub domain %s routes to %s", rule.Payload, rule.Proxy)
			}
			githubSeen[rule.Payload] = true
		}
	}
	// Keep the original 12 AI rules and all eight migrated GitHub rules.
	if managedCount < 20 {
		return fmt.Errorf("AI-US-STABLE rules below merged baseline: %d", managedCount)
	}
	for _, domain := range githubDomains {
		if !githubSeen[domain] {
			return fmt.Errorf("GitHub domain rule missing: %s", domain)
		}
	}
	return nil
}

func (c *Controller) verifyFlow(host, targetURL, expectedGroup string) error {
	before, err := c.connectionIDs()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "curl", "--http1.1", "-L", "-sS", "-o", "/dev/null", "--proxy", "http://127.0.0.1:7890", "--connect-timeout", "6", "--max-time", "15", "--limit-rate", "5", "-A", "Mozilla/5.0", targetURL)
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		cmd.Wait()
	}()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		var response ConnectionsResponse
		if err := c.request(http.MethodGet, "/connections", nil, &response); err == nil {
			for _, connection := range response.Connections {
				if before[connection.ID] {
					continue
				}
				if connection.Metadata.Host == host || strings.HasSuffix(connection.Metadata.Host, "."+host) {
					for _, chain := range connection.Chains {
						if chain == expectedGroup {
							return nil
						}
					}
					return fmt.Errorf("host %s used chains %v", connection.Metadata.Host, connection.Chains)
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("new matching connection not observed")
}

func (c *Controller) connectionIDs() (map[string]bool, error) {
	var response ConnectionsResponse
	if err := c.request(http.MethodGet, "/connections", nil, &response); err != nil {
		return nil, err
	}
	ids := make(map[string]bool, len(response.Connections))
	for _, connection := range response.Connections {
		ids[connection.ID] = true
	}
	return ids, nil
}

func copyFile(source, destination string, mode os.FileMode) error {
	data, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	return writeAtomic(destination, data, mode)
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0755); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(mode); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return err
	}
	dir, err := os.Open(directory)
	if err != nil {
		return nil
	}
	defer dir.Close()
	return dir.Sync()
}

func pruneBackups(directory, prefix string, keep int) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	paths := make([]string, 0)
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), prefix) {
			paths = append(paths, filepath.Join(directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	for len(paths) > keep {
		os.Remove(paths[0])
		paths = paths[1:]
	}
}

func conciseOutput(output []byte) string {
	text := strings.TrimSpace(string(output))
	if len(text) > 240 {
		text = text[len(text)-240:]
	}
	return strings.ReplaceAll(text, "\n", " ")
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return (values[middle-1] + values[middle]) / 2
}

func nowString() string {
	return time.Now().Format(time.RFC3339)
}

func logf(format string, args ...any) {
	message := fmt.Sprintf(format, args...)
	line := fmt.Sprintf("%s %s\n", nowString(), message)
	if logFile != "" {
		if err := appendLogAtomic([]byte(line)); err != nil {
			fmt.Fprintf(os.Stderr, "log write: %v\n", err)
		}
	}
	if strings.HasPrefix(message, "ERROR") || strings.HasPrefix(message, "WARN") || strings.HasPrefix(message, "REPAIR") || strings.HasPrefix(message, "RECOVERED") {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = exec.CommandContext(ctx, "logger", "-t", "router-ai-node-guard", message).Run()
		cancel()
	}
}

func appendLogAtomic(line []byte) error {
	data, err := os.ReadFile(logFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 512*1024 {
		data = data[len(data)-256*1024:]
		if index := bytes.IndexByte(data, '\n'); index >= 0 {
			data = data[index+1:]
		}
	}
	data = append(data, line...)
	return writeAtomic(logFile, data, 0600)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
