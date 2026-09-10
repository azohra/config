package config

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func testMCPServers() MCPServers {
	return MCPServers{
		Agents: []string{"claude-code", "codex"},
		Servers: map[string]MCPServer{
			"blender": {Command: "uvx", Args: []string{"blender-mcp"}, Env: map[string]string{"BLENDER_HOST": "localhost"}},
			"docs":    {URL: "https://mcp.example.com/mcp", Headers: map[string]string{"X-Region": "us-east-1"}},
			"scratch": {Command: "npx", Args: []string{"-y", "@example/scratch-mcp"}, Agents: []string{"codex"}},
		},
	}
}

func mcpFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "mcp-servers", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// placeMCPHarness writes one harness file where Config expects it, or where
// a link there points, and returns the path Config will read.
func placeMCPHarness(t *testing.T, paths Paths, harness string, data []byte) string {
	t.Helper()
	target, _ := mcpHarnessByID(harness)
	path := target.path(paths)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func testMCPManager(paths Paths, servers MCPServers) mcpServerManager {
	return mcpServerManager{Paths: paths, Servers: servers, Log: Logger{Out: &bytes.Buffer{}}}
}

func TestMCPServersContractRejectsAmbiguousDeclarations(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*MCPServers)
		want   string
	}{
		{"no agents", func(s *MCPServers) { s.Agents = nil }, "agents must not be empty"},
		{"unknown agent", func(s *MCPServers) { s.Agents = []string{"claude-code", "cursor"} }, "not a known harness"},
		{"duplicate agent", func(s *MCPServers) { s.Agents = []string{"codex", "codex"} }, "agents repeats"},
		{"no servers", func(s *MCPServers) { s.Servers = nil }, "servers must not be empty"},
		{"invalid name", func(s *MCPServers) { s.Servers["Bad Name"] = MCPServer{Command: "x"} }, `server "Bad Name" is invalid`},
		{"both transports", func(s *MCPServers) {
			s.Servers["both"] = MCPServer{Command: "x", URL: "https://example.com/mcp"}
		}, "both command and url"},
		{"neither transport", func(s *MCPServers) { s.Servers["none"] = MCPServer{Args: []string{"x"}} }, "neither command nor url"},
		{"headers on stdio", func(s *MCPServers) {
			s.Servers["mixed"] = MCPServer{Command: "x", Headers: map[string]string{"A": "b"}}
		}, "headers belong to an http server"},
		{"env on http", func(s *MCPServers) {
			s.Servers["mixed"] = MCPServer{URL: "https://example.com/mcp", Env: map[string]string{"A": "b"}}
		}, "args and env belong to a stdio server"},
		{"relative url", func(s *MCPServers) { s.Servers["rel"] = MCPServer{URL: "/mcp"} }, "absolute http or https"},
		{"invalid env name", func(s *MCPServers) {
			s.Servers["env"] = MCPServer{Command: "x", Env: map[string]string{"BAD-NAME": "1"}}
		}, `env name "BAD-NAME" is invalid`},
		{"invalid header", func(s *MCPServers) {
			s.Servers["header"] = MCPServer{URL: "https://example.com/mcp", Headers: map[string]string{"X Y": "1"}}
		}, `header "X Y" is invalid`},
		{"header line break", func(s *MCPServers) {
			s.Servers["header"] = MCPServer{URL: "https://example.com/mcp", Headers: map[string]string{"X": "a\nb"}}
		}, "line break"},
		{"undeclared server agent", func(s *MCPServers) {
			s.Servers["narrow"] = MCPServer{Command: "x", Agents: []string{"cursor"}}
		}, `agent "cursor" is not declared`},
		{"empty server agents", func(s *MCPServers) {
			s.Servers["narrow"] = MCPServer{Command: "x", Agents: []string{}}
		}, "agents must not be empty"},
		{"duplicate server agent", func(s *MCPServers) {
			s.Servers["narrow"] = MCPServer{Command: "x", Agents: []string{"codex", "codex"}}
		}, "agents repeats"},
	} {
		t.Run(test.name, func(t *testing.T) {
			contract := testMCPServers()
			test.change(&contract)
			if err := contract.Validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() = %v, want %q", err, test.want)
			}
		})
	}
	if err := testMCPServers().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMachineDecodesMCPServers(t *testing.T) {
	paths := writeMachineTOML(t, validMachineTOML()+`
[mcp_servers]
agents = ["claude-code", "codex"]

[mcp_servers.servers.blender]
command = "uvx"
args = ["blender-mcp"]
env = { BLENDER_HOST = "localhost" }

[mcp_servers.servers.docs]
url = "https://mcp.example.com/mcp"
headers = { X-Region = "us-east-1" }
agents = ["codex"]
`)
	machine, err := LoadMachine(paths)
	if err != nil {
		t.Fatal(err)
	}
	if machine.MCPServers == nil || len(machine.MCPServers.Servers) != 2 {
		t.Fatalf("mcp_servers was not decoded: %+v", machine.MCPServers)
	}
	desired := machine.MCPServers.desired()
	if _, reaches := desired["claude-code"]["docs"]; reaches {
		t.Fatal("a narrowed server reached the resource default")
	}
	if desired["codex"]["docs"].Headers["X-Region"] != "us-east-1" || desired["claude-code"]["blender"].Env["BLENDER_HOST"] != "localhost" {
		t.Fatalf("declared values were lost: %+v", desired)
	}
	if _, err := LoadMachine(writeMachineTOML(t, validMachineTOML()+"\n[mcp_servers]\nagents = [\"codex\"]\n")); err == nil || !strings.Contains(err.Error(), "mcp_servers: servers must not be empty") {
		t.Fatalf("an empty declaration loaded: %v", err)
	}
}

func TestMCPServersReconcileConvergesBothHarnesses(t *testing.T) {
	paths := testPaths(t)
	claude := placeMCPHarness(t, paths, "claude-code", mcpFixture(t, "claude.json"))
	codex := placeMCPHarness(t, paths, "codex", mcpFixture(t, "codex.toml"))
	contract := testMCPServers()

	resource := inspectMCPServers(paths, contract)
	if resource.State != Drift || !resource.Allows(Apply) {
		t.Fatalf("stale harnesses inspected as %+v", resource)
	}
	for _, want := range []string{"blender differs for Claude Code", "docs is missing for Claude Code", "scratch is missing for Codex"} {
		if !slices.Contains(resource.Details, want) {
			t.Fatalf("details %v lack %q", resource.Details, want)
		}
	}
	if slices.Contains(resource.Details, "scratch is missing for Claude Code") {
		t.Fatal("a narrowed server was expected on the wrong harness")
	}

	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct{ path, fixture string }{{claude, "claude.expected.json"}, {codex, "codex.expected.toml"}} {
		got, err := os.ReadFile(file.path)
		if err != nil {
			t.Fatal(err)
		}
		if want := mcpFixture(t, file.fixture); !bytes.Equal(got, want) {
			t.Fatalf("%s:\n%s\nwant:\n%s", file.fixture, got, want)
		}
	}
	if info, err := os.Stat(claude); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("harness file mode changed: %v %v", info, err)
	}
	ledger, _, err := readMCPServerLedger(paths)
	if err != nil {
		t.Fatal(err)
	}
	if len(ledger.Agents["claude-code"]) != 2 || len(ledger.Agents["codex"]) != 3 || ledger.Agents["codex"]["foreign"] != "" {
		t.Fatalf("ledger = %+v", ledger.Agents)
	}
	if resource := inspectMCPServers(paths, contract); resource.State != Current || resource.Summary != "3 MCP servers current" {
		t.Fatalf("converged harnesses inspected as %+v", resource)
	}
}

func TestMCPServersCurrentHarnessesAreNotRewritten(t *testing.T) {
	paths := testPaths(t)
	claude := placeMCPHarness(t, paths, "claude-code", mcpFixture(t, "claude.json"))
	codex := placeMCPHarness(t, paths, "codex", mcpFixture(t, "codex.toml"))
	manager := testMCPManager(paths, testMCPServers())
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	// Claude Code rewrites its own file in its own key order. A reformat of
	// the same meaning is not drift.
	reformatted := bytes.ReplaceAll(mcpFixture(t, "claude.expected.json"), []byte("\"type\": \"stdio\",\n      \"command\": \"uvx\""), []byte("\"command\": \"uvx\",\n      \"type\": \"stdio\""))
	if err := os.WriteFile(claude, reformatted, 0o600); err != nil {
		t.Fatal(err)
	}
	before := map[string]os.FileInfo{}
	for _, path := range []string{claude, codex, mcpServerLedgerPath(paths)} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		before[path] = info
	}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	for path, info := range before {
		after, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if !os.SameFile(info, after) {
			t.Fatalf("%s was replaced by a no-op reconcile", path)
		}
	}
}

func TestMCPServersAddTheKeyToAHarnessWithoutOne(t *testing.T) {
	paths := testPaths(t)
	claude := placeMCPHarness(t, paths, "claude-code", []byte("{\n  \"numStartups\": 1\n}\n"))
	codex := placeMCPHarness(t, paths, "codex", []byte("model = \"gpt-5-codex\""))
	contract := MCPServers{Agents: []string{"claude-code", "codex"}, Servers: map[string]MCPServer{
		"docs": {URL: "https://mcp.example.com/mcp"},
	}}
	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(claude)
	want := "{\n  \"numStartups\": 1,\n  \"mcpServers\": {\n    \"docs\": {\n      \"type\": \"http\",\n      \"url\": \"https://mcp.example.com/mcp\",\n      \"headers\": {}\n    }\n  }\n}\n"
	if string(got) != want {
		t.Fatalf("claude.json:\n%s\nwant:\n%s", got, want)
	}
	got, _ = os.ReadFile(codex)
	want = "model = \"gpt-5-codex\"\n\n[mcp_servers.docs]\nurl = \"https://mcp.example.com/mcp\"\n"
	if string(got) != want {
		t.Fatalf("config.toml:\n%s\nwant:\n%s", got, want)
	}
	// Compact JSON stays compact.
	placeMCPHarness(t, paths, "claude-code", []byte(`{"a":1,"mcpServers":{}}`))
	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	got, _ = os.ReadFile(claude)
	if want := `{"a":1,"mcpServers":{"docs":{"type":"http","url":"https://mcp.example.com/mcp","headers":{}}}}`; string(got) != want {
		t.Fatalf("compact claude.json = %s, want %s", got, want)
	}
}

func TestMCPServersSkipAnAbsentHarness(t *testing.T) {
	paths := testPaths(t)
	contract := testMCPServers()
	var log bytes.Buffer
	manager := mcpServerManager{Paths: paths, Servers: contract, Log: Logger{Out: &log}}
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	for _, harness := range mcpHarnesses {
		if _, err := os.Lstat(harness.path(paths)); err == nil {
			t.Fatalf("%s was created", harness.Display)
		}
	}
	if !strings.Contains(log.String(), "Claude Code is not present") || !strings.Contains(log.String(), "Codex is not present") {
		t.Fatalf("absent harnesses were not reported: %s", log.String())
	}
	if _, err := os.Lstat(mcpServerLedgerPath(paths)); err == nil {
		t.Fatal("ownership was recorded for a harness Config never wrote")
	}
	resource := inspectMCPServers(paths, contract)
	if resource.State != Unavailable || resource.Failed() != 0 || resource.Allows(Apply) {
		t.Fatalf("absent harnesses inspected as %+v", resource)
	}

	codex := placeMCPHarness(t, paths, "codex", mcpFixture(t, "codex.toml"))
	if err := manager.Reconcile(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(codex)
	if !bytes.Equal(got, mcpFixture(t, "codex.expected.toml")) {
		t.Fatalf("present harness did not converge:\n%s", got)
	}
	if _, err := os.Lstat(paths.InHome(".claude.json")); err == nil {
		t.Fatal("~/.claude.json was created")
	}
	resource = inspectMCPServers(paths, contract)
	if resource.State != Current || resource.Failed() != 0 || !slices.ContainsFunc(resource.Checks, func(check Check) bool {
		return check.OK && strings.HasPrefix(check.Label, "Claude Code is not present")
	}) {
		t.Fatalf("one present harness inspected as %+v", resource)
	}
}

func TestMCPServersWriteThroughALinkIntoTheManagedCheckout(t *testing.T) {
	paths := testPaths(t)
	source := paths.InRoot("codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(source), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, mcpFixture(t, "codex.toml"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := paths.InHome(".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	contract := testMCPServers()
	contract.Agents = []string{"codex"}
	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("dotfile link was replaced: %v %v", info, err)
	}
	if target, _ := os.Readlink(link); target != source {
		t.Fatalf("dotfile link now points to %s", target)
	}
	got, _ := os.ReadFile(source)
	if !bytes.Equal(got, mcpFixture(t, "codex.expected.toml")) {
		t.Fatalf("managed checkout file did not converge:\n%s", got)
	}
	if info, _ := os.Stat(source); info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 644", info.Mode().Perm())
	}
	entries, _ := os.ReadDir(filepath.Dir(source))
	for _, entry := range entries {
		if strandedWrite(entry.Name()) {
			t.Fatalf("staging file %s was left beside the target", entry.Name())
		}
	}
	if resource := inspectMCPServers(paths, contract); resource.State != Current {
		t.Fatalf("linked harness inspected as %+v", resource)
	}
}

func TestMCPServersRefuseALinkOutsideTheManagedCheckout(t *testing.T) {
	paths := testPaths(t)
	elsewhere := filepath.Join(t.TempDir(), "config.toml")
	original := mcpFixture(t, "codex.toml")
	if err := os.WriteFile(elsewhere, original, 0o600); err != nil {
		t.Fatal(err)
	}
	link := paths.InHome(".codex", "config.toml")
	if err := os.MkdirAll(filepath.Dir(link), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(elsewhere, link); err != nil {
		t.Fatal(err)
	}
	claude := placeMCPHarness(t, paths, "claude-code", mcpFixture(t, "claude.json"))
	contract := testMCPServers()
	resource := inspectMCPServers(paths, contract)
	if resource.State != Drift || resource.Failed() != 2 || !resource.Allows(Apply) {
		t.Fatalf("linked-elsewhere harness inspected as %+v", resource)
	}
	if !strings.Contains(resource.Checks[0].Detail, "~/.codex/config.toml is a link to "+elsewhere) {
		t.Fatalf("conflict does not name the link: %+v", resource.Checks)
	}
	err := testMCPManager(paths, contract).Reconcile()
	if err == nil || !strings.Contains(err.Error(), "outside the managed checkout") {
		t.Fatalf("Reconcile() = %v", err)
	}
	if got, _ := os.ReadFile(elsewhere); !bytes.Equal(got, original) {
		t.Fatal("a link elsewhere was written through")
	}
	if info, _ := os.Lstat(link); info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("the link was replaced")
	}
	if got, _ := os.ReadFile(claude); !bytes.Equal(got, mcpFixture(t, "claude.expected.json")) {
		t.Fatal("the other harness did not converge independently")
	}
	ledger, _, _ := readMCPServerLedger(paths)
	if len(ledger.Agents["codex"]) != 0 {
		t.Fatalf("ownership was claimed on a refused harness: %+v", ledger.Agents)
	}
}

func TestMCPServersKeepTheCommentThatIntroducesTheNextTable(t *testing.T) {
	paths := testPaths(t)
	codex := placeMCPHarness(t, paths, "codex", []byte("[mcp_servers.blender]\ncommand = \"old\"\n\n# Sandbox\n[sandbox_workspace_write]\nnetwork_access = true\n"))
	contract := MCPServers{Agents: []string{"codex"}, Servers: map[string]MCPServer{"blender": {Command: "uvx"}}}
	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(codex)
	if want := "[mcp_servers.blender]\ncommand = \"uvx\"\n\n# Sandbox\n[sandbox_workspace_write]\nnetwork_access = true\n"; string(got) != want {
		t.Fatalf("config.toml:\n%s\nwant:\n%s", got, want)
	}
	// A refused rewrite claims nothing.
	placeMCPHarness(t, paths, "codex", []byte("[mcp_servers]\nblender.command = \"old\"\n"))
	if err := os.Remove(mcpServerLedgerPath(paths)); err != nil {
		t.Fatal(err)
	}
	if err := testMCPManager(paths, contract).Reconcile(); err == nil {
		t.Fatal("a dotted-key table was rewritten")
	}
	if _, err := os.Lstat(mcpServerLedgerPath(paths)); err == nil {
		t.Fatal("ownership was claimed for an entry Config refused to write")
	}
}

func TestMCPServersRewriteASubTableWithoutDoublingBlankLines(t *testing.T) {
	paths := testPaths(t)
	codex := placeMCPHarness(t, paths, "codex", []byte("[mcp_servers.blender]\ncommand = \"old\"\n\n[mcp_servers.blender.env]\nA = \"1\"\n\n[other]\nkey = true\n"))
	contract := MCPServers{Agents: []string{"codex"}, Servers: map[string]MCPServer{"blender": {Command: "uvx"}}}
	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(codex)
	if want := "[mcp_servers.blender]\ncommand = \"uvx\"\n\n[other]\nkey = true\n"; string(got) != want {
		t.Fatalf("config.toml:\n%s\nwant:\n%s", got, want)
	}
}

func TestMCPServersRefuseACodexFormTheyCannotRewrite(t *testing.T) {
	paths := testPaths(t)
	original := []byte("[mcp_servers]\nblender.command = \"old\"\n")
	codex := placeMCPHarness(t, paths, "codex", original)
	contract := testMCPServers()
	contract.Agents = []string{"codex"}
	err := testMCPManager(paths, contract).Reconcile()
	if err == nil || !strings.Contains(err.Error(), "blender is declared in a form Config cannot rewrite") {
		t.Fatalf("Reconcile() = %v", err)
	}
	if got, _ := os.ReadFile(codex); !bytes.Equal(got, original) {
		t.Fatalf("the file was rewritten:\n%s", got)
	}
}

func TestMCPServersPruneRemovesOnlyRecordedEntries(t *testing.T) {
	paths := testPaths(t)
	claude := placeMCPHarness(t, paths, "claude-code", mcpFixture(t, "claude.json"))
	codex := placeMCPHarness(t, paths, "codex", mcpFixture(t, "codex.toml"))
	if err := testMCPManager(paths, testMCPServers()).Reconcile(); err != nil {
		t.Fatal(err)
	}
	// docs leaves the declaration, blender narrows to Codex, and someone
	// edits the Claude Code scratch entry Config never wrote there.
	contract := testMCPServers()
	delete(contract.Servers, "docs")
	contract.Servers["blender"] = MCPServer{Command: "uvx", Args: []string{"blender-mcp"}, Env: map[string]string{"BLENDER_HOST": "localhost"}, Agents: []string{"codex"}}
	machine := testMachine()
	machine.MCPServers = &contract
	pruner := Pruner{Paths: paths, Machine: machine, Runner: converged{}, Log: Logger{Out: &bytes.Buffer{}}}

	plan, warnings := pruner.planMCPServers(nil)
	want := []pruneMCPServer{
		{Agent: "claude-code", Name: "blender", RemoveEntry: true},
		{Agent: "claude-code", Name: "docs", RemoveEntry: true},
		{Agent: "codex", Name: "docs", RemoveEntry: true},
	}
	if !slices.Equal(plan.Servers, want) || len(warnings) != 0 {
		t.Fatalf("plan = %+v, warnings = %v", plan.Servers, warnings)
	}
	if err := pruner.applyPruneMCPServers(plan); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(claude)
	if !bytes.Equal(got, mcpFixture(t, "claude.pruned.json")) {
		t.Fatalf("claude.json after prune:\n%s", got)
	}
	got, _ = os.ReadFile(codex)
	if !bytes.Equal(got, mcpFixture(t, "codex.pruned.toml")) {
		t.Fatalf("config.toml after prune:\n%s", got)
	}
	ledger, _, _ := readMCPServerLedger(paths)
	if len(ledger.Agents["claude-code"]) != 0 || len(ledger.Agents["codex"]) != 2 {
		t.Fatalf("ledger after prune = %+v", ledger.Agents)
	}
	if resource := inspectMCPServers(paths, contract); resource.State != Current {
		t.Fatalf("pruned harnesses inspected as %+v", resource)
	}

	// An owned entry someone changed is reported and kept; an owned entry
	// that is already gone only loses its record; a harness that is gone
	// loses its records without a write.
	delete(contract.Servers, "scratch")
	delete(contract.Servers, "blender")
	edited := bytes.Replace(got, []byte("\"-y\", \"@example/scratch-mcp\""), []byte("\"-y\", \"@example/other-mcp\""), 1)
	if err := os.WriteFile(codex, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	plan, warnings = pruner.planMCPServers(nil)
	if !slices.Equal(plan.Servers, []pruneMCPServer{{Agent: "codex", Name: "blender", RemoveEntry: true}}) ||
		!slices.Equal(warnings, []string{"scratch for Codex changed since Config wrote it; left untouched"}) {
		t.Fatalf("plan = %+v, warnings = %v", plan.Servers, warnings)
	}
	if err := os.Remove(codex); err != nil {
		t.Fatal(err)
	}
	plan, warnings = pruner.planMCPServers(nil)
	if !slices.Equal(plan.Servers, []pruneMCPServer{{Agent: "codex", Name: "blender"}, {Agent: "codex", Name: "scratch"}}) || len(warnings) != 0 {
		t.Fatalf("plan = %+v, warnings = %v", plan.Servers, warnings)
	}
	if err := pruner.applyPruneMCPServers(plan); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(codex); err == nil {
		t.Fatal("prune created the harness file")
	}
	ledger, _, _ = readMCPServerLedger(paths)
	if len(ledger.Agents) != 0 {
		t.Fatalf("ledger after prune = %+v", ledger.Agents)
	}
}

func TestMCPServersPruneLeavesTheObjectShapeClaudeCodeWrote(t *testing.T) {
	for _, test := range []struct{ name, before, after string }{
		{"only Config entries", "{\n  \"numStartups\": 1,\n  \"mcpServers\": {\n    \"blender\": {},\n    \"docs\": {}\n  },\n  \"projects\": {}\n}\n",
			"{\n  \"numStartups\": 1,\n  \"mcpServers\": {},\n  \"projects\": {}\n}\n"},
		{"Config entry first", "{\n  \"mcpServers\": {\n    \"blender\": {},\n    \"foreign\": {\n      \"command\": \"x\"\n    },\n    \"docs\": {}\n  }\n}\n",
			"{\n  \"mcpServers\": {\n    \"foreign\": {\n      \"command\": \"x\"\n    }\n  }\n}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := testPaths(t)
			claude := placeMCPHarness(t, paths, "claude-code", []byte(test.before))
			contract := testMCPServers()
			contract.Agents = []string{"claude-code"}
			delete(contract.Servers, "scratch")
			if err := testMCPManager(paths, contract).Reconcile(); err != nil {
				t.Fatal(err)
			}
			pruner := Pruner{Paths: paths, Machine: testMachine(), Runner: converged{}, Log: Logger{Out: &bytes.Buffer{}}}
			plan, warnings := pruner.planMCPServers(nil)
			if len(plan.Servers) != 2 || len(warnings) != 0 {
				t.Fatalf("plan = %+v, warnings = %v", plan.Servers, warnings)
			}
			if err := pruner.applyPruneMCPServers(plan); err != nil {
				t.Fatal(err)
			}
			if got, _ := os.ReadFile(claude); string(got) != test.after {
				t.Fatalf("claude.json:\n%s\nwant:\n%s", got, test.after)
			}
		})
	}
}

func TestMCPServersPruneRefusesAPlanWhoseStateMoved(t *testing.T) {
	paths := testPaths(t)
	codex := placeMCPHarness(t, paths, "codex", mcpFixture(t, "codex.toml"))
	contract := testMCPServers()
	contract.Agents = []string{"codex"}
	if err := testMCPManager(paths, contract).Reconcile(); err != nil {
		t.Fatal(err)
	}
	machine := testMachine()
	pruner := Pruner{Paths: paths, Machine: machine, Runner: converged{}, Log: Logger{Out: &bytes.Buffer{}}}
	plan, _ := pruner.planMCPServers(nil)
	if len(plan.Servers) != 3 {
		t.Fatalf("plan = %+v", plan.Servers)
	}
	before, _ := os.ReadFile(codex)
	edited := bytes.Replace(before, []byte("BLENDER_HOST = \"localhost\""), []byte("BLENDER_HOST = \"elsewhere\""), 1)
	if err := os.WriteFile(codex, edited, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pruner.applyPruneMCPServers(plan); err == nil || !strings.Contains(err.Error(), "changed after preview") {
		t.Fatalf("applyPruneMCPServers() = %v", err)
	}
	if got, _ := os.ReadFile(codex); !bytes.Equal(got, edited) {
		t.Fatal("a moved plan was applied")
	}
}

func TestCodexMCPServerRenderingReadsBackAsDeclared(t *testing.T) {
	server := MCPServer{
		Command: `C:\tools\mcp "quoted"`, Args: []string{"tab\there", "new\nline", "ünïcode"},
		Env: map[string]string{"ODD_KEY": "value with 'quotes'", "Z": "", "A": "1"},
	}
	rendered := renderCodexMCPServer("blender", server)
	entries, err := codexMCPEntries([]byte(rendered))
	if err != nil {
		t.Fatalf("%v\n%s", err, rendered)
	}
	if live := entries["blender"]; !live.OK || !sameMCPServer(live.Server, server) {
		t.Fatalf("rendered server read back as %+v\n%s", live, rendered)
	}
	http := MCPServer{URL: "https://mcp.example.com/mcp", Headers: map[string]string{"X-Region": "us", "Content-Type": "application/json"}}
	rendered = renderCodexMCPServer("docs", http)
	if !strings.Contains(rendered, "[mcp_servers.docs.http_headers]\nContent-Type = \"application/json\"\nX-Region = \"us\"\n") {
		t.Fatalf("http rendering:\n%s", rendered)
	}
	entries, _ = codexMCPEntries([]byte(rendered))
	if live := entries["docs"]; !live.OK || !sameMCPServer(live.Server, http) {
		t.Fatalf("rendered http server read back as %+v", live)
	}
}

func TestMCPServersRestoreFollowsAgentSkills(t *testing.T) {
	machine := testMachine()
	skills := testAgentSkills()
	servers := testMCPServers()
	machine.AgentSkills = &skills
	machine.MCPServers = &servers
	ids := restoreStepIDs(machine)
	skillsIndex := slices.Index(ids, restoreAgentSkillsStep)
	serversIndex := slices.Index(ids, restoreMCPServersStep)
	if skillsIndex < 0 || serversIndex < skillsIndex {
		t.Fatalf("restore steps = %v", ids)
	}
	machine.MCPServers = nil
	if slices.Contains(restoreStepIDs(machine), restoreMCPServersStep) {
		t.Fatal("an undeclared resource owes a restore step")
	}
}
