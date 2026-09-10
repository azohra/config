package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/pelletier/go-toml/v2"
)

const (
	mcpServersID           = "mcp-servers"
	mcpServersName         = "MCP servers"
	mcpServersLedgerSchema = 1
)

// MCPServers declares the user-scope MCP servers each agent harness should
// know about. Config owns where each harness keeps that state and how a
// declared entry converges there; the machine repository owns only these
// values. Every other byte in a harness file, including servers Config never
// wrote, belongs to the harness or the person.
type MCPServers struct {
	// Agents are the harnesses every server reaches unless a server narrows
	// its own list. Each one names a Config-known user-scope configuration.
	Agents  []string             `toml:"agents"`
	Servers map[string]MCPServer `toml:"servers"`
}

// MCPServer is one declared server: a stdio process or an HTTP endpoint.
type MCPServer struct {
	Command string            `toml:"command"`
	Args    []string          `toml:"args"`
	Env     map[string]string `toml:"env"`
	URL     string            `toml:"url"`
	Headers map[string]string `toml:"headers"`
	// Agents narrows this server below the resource default. Absent means
	// every declared agent.
	Agents []string `toml:"agents"`
}

// mcpHarness is one agent harness Config knows how to converge. Its path is
// harness state that the harness itself creates, so Config never makes it.
type mcpHarness struct {
	ID      string
	Name    string
	Display string
	path    func(Paths) string
}

var mcpHarnesses = []mcpHarness{
	{ID: "claude-code", Name: "Claude Code", Display: "~/.claude.json",
		path: func(paths Paths) string { return paths.InHome(".claude.json") }},
	{ID: "codex", Name: "Codex", Display: "~/.codex/config.toml",
		path: func(paths Paths) string { return paths.InHome(".codex", "config.toml") }},
}

func mcpHarnessByID(id string) (mcpHarness, bool) {
	for _, harness := range mcpHarnesses {
		if harness.ID == id {
			return harness, true
		}
	}
	return mcpHarness{}, false
}

var (
	environmentNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	headerNamePattern      = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+.^_`|~-]+$")
	tomlBareKeyPattern     = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

func (s MCPServers) Validate() error {
	if len(s.Agents) == 0 {
		return errors.New("agents must not be empty")
	}
	seenAgents := map[string]bool{}
	for _, agent := range s.Agents {
		if _, known := mcpHarnessByID(agent); !known {
			return fmt.Errorf("agent %q is not a known harness", agent)
		}
		if seenAgents[agent] {
			return fmt.Errorf("agents repeats %q", agent)
		}
		seenAgents[agent] = true
	}
	if len(s.Servers) == 0 {
		return errors.New("servers must not be empty")
	}
	for _, name := range sortedKeys(s.Servers) {
		if !contractIDPattern.MatchString(name) {
			return fmt.Errorf("server %q is invalid", name)
		}
		if err := s.Servers[name].validate(seenAgents); err != nil {
			return fmt.Errorf("server %q %w", name, err)
		}
	}
	return nil
}

func (s MCPServer) validate(declaredAgents map[string]bool) error {
	switch {
	case s.Command != "" && s.URL != "":
		return errors.New("declares both command and url")
	case s.Command == "" && s.URL == "":
		return errors.New("declares neither command nor url")
	case s.Command != "":
		if s.Headers != nil {
			return errors.New("headers belong to an http server")
		}
		for _, argument := range s.Args {
			if strings.ContainsRune(argument, 0) {
				return errors.New("args contain a NUL byte")
			}
		}
		for _, key := range sortedKeys(s.Env) {
			if !environmentNamePattern.MatchString(key) {
				return fmt.Errorf("env name %q is invalid", key)
			}
			if strings.ContainsRune(s.Env[key], 0) {
				return fmt.Errorf("env %s contains a NUL byte", key)
			}
		}
	default:
		if s.Args != nil || s.Env != nil {
			return errors.New("args and env belong to a stdio server")
		}
		parsed, err := url.Parse(s.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return errors.New("url must be an absolute http or https URL")
		}
		for _, key := range sortedKeys(s.Headers) {
			if !headerNamePattern.MatchString(key) {
				return fmt.Errorf("header %q is invalid", key)
			}
			if strings.ContainsAny(s.Headers[key], "\r\n\x00") {
				return fmt.Errorf("header %s contains a line break", key)
			}
		}
	}
	if s.Agents != nil && len(s.Agents) == 0 {
		return errors.New("agents must not be empty")
	}
	seen := map[string]bool{}
	for _, agent := range s.Agents {
		if !declaredAgents[agent] {
			return fmt.Errorf("agent %q is not declared by the resource", agent)
		}
		if seen[agent] {
			return fmt.Errorf("agents repeats %q", agent)
		}
		seen[agent] = true
	}
	return nil
}

func sortedKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// desired answers, for each declared harness, the servers it should carry.
func (s MCPServers) desired() map[string]map[string]MCPServer {
	desired := make(map[string]map[string]MCPServer, len(s.Agents))
	for _, agent := range s.Agents {
		desired[agent] = map[string]MCPServer{}
	}
	for name, server := range s.Servers {
		agents := server.Agents
		if len(agents) == 0 {
			agents = s.Agents
		}
		for _, agent := range agents {
			if _, declared := desired[agent]; declared {
				desired[agent][name] = server
			}
		}
	}
	return desired
}

// harnessOrder lists the declared agents in Config's harness order, so every
// surface reports them the same way.
func (s MCPServers) harnessOrder() []mcpHarness {
	var harnesses []mcpHarness
	for _, harness := range mcpHarnesses {
		if slices.Contains(s.Agents, harness.ID) {
			harnesses = append(harnesses, harness)
		}
	}
	return harnesses
}

// canonical is the harness-independent identity of one entry. Both harness
// shapes reduce to it, so a no-op compares meaning rather than bytes and the
// ledger digest survives a harness reformatting its own file.
func (s MCPServer) canonical() []byte {
	type form struct {
		Transport string            `json:"transport"`
		Command   string            `json:"command,omitempty"`
		Args      []string          `json:"args,omitempty"`
		Env       map[string]string `json:"env,omitempty"`
		URL       string            `json:"url,omitempty"`
		Headers   map[string]string `json:"headers,omitempty"`
	}
	entry := form{Command: s.Command, URL: s.URL}
	if s.Command != "" {
		entry.Transport = "stdio"
		if len(s.Args) > 0 {
			entry.Args = s.Args
		}
		if len(s.Env) > 0 {
			entry.Env = s.Env
		}
	} else {
		entry.Transport = "http"
		if len(s.Headers) > 0 {
			entry.Headers = s.Headers
		}
	}
	data, err := json.Marshal(entry)
	if err != nil {
		panic(err)
	}
	return data
}

func (s MCPServer) digest() string {
	return contentDigest(s.canonical())
}

func sameMCPServer(left, right MCPServer) bool {
	return bytes.Equal(left.canonical(), right.canonical())
}

// Ownership ledger

type mcpServerLedger struct {
	Schema int `json:"schema"`
	// Agents maps a harness id to the server names Config wrote there and the
	// canonical digest of each entry as written.
	Agents map[string]map[string]string `json:"agents"`
}

func emptyMCPServerLedger() mcpServerLedger {
	return mcpServerLedger{Schema: mcpServersLedgerSchema, Agents: map[string]map[string]string{}}
}

func mcpServerLedgerPath(paths Paths) string {
	return paths.InHome("Library", "Application Support", "Config", "mcp-servers.json")
}

func readMCPServerLedger(paths Paths) (mcpServerLedger, []byte, error) {
	data, err := os.ReadFile(mcpServerLedgerPath(paths))
	if errors.Is(err, os.ErrNotExist) {
		return emptyMCPServerLedger(), nil, nil
	}
	if err != nil {
		return mcpServerLedger{}, nil, err
	}
	var ledger mcpServerLedger
	if err := decodeExactJSON(data, &ledger); err != nil {
		return mcpServerLedger{}, data, err
	}
	if ledger.Schema != mcpServersLedgerSchema || ledger.Agents == nil {
		return mcpServerLedger{}, data, errors.New("unsupported ledger")
	}
	for agent, servers := range ledger.Agents {
		if _, known := mcpHarnessByID(agent); !known {
			return mcpServerLedger{}, data, fmt.Errorf("unknown harness %q", agent)
		}
		for name, digest := range servers {
			if !contractIDPattern.MatchString(name) || !validContentDigest(digest) {
				return mcpServerLedger{}, data, fmt.Errorf("invalid ownership for %q", name)
			}
		}
	}
	return ledger, data, nil
}

func writeMCPServerLedger(paths Paths, ledger mcpServerLedger) error {
	for agent, servers := range ledger.Agents {
		if len(servers) == 0 {
			delete(ledger.Agents, agent)
		}
	}
	data, err := json.MarshalIndent(ledger, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if current, readErr := os.ReadFile(mcpServerLedgerPath(paths)); readErr == nil && bytes.Equal(current, data) {
		return nil
	}
	return AtomicWrite(mcpServerLedgerPath(paths), data, 0o600)
}

// Targets

// mcpTarget is one harness file as Config found it. Resolved is where bytes
// are read and written: the file itself, or the file a link inside the
// managed checkout points at. Staging beside Resolved keeps a dotfile link
// intact; a rename at the link path would replace the link with a file.
type mcpTarget struct {
	Harness  mcpHarness
	Path     string
	Resolved string
	Mode     os.FileMode
	Absent   bool
	Conflict string
}

func resolveMCPTarget(paths Paths, harness mcpHarness) mcpTarget {
	target := mcpTarget{Harness: harness, Path: harness.path(paths)}
	info, err := os.Lstat(target.Path)
	if errors.Is(err, os.ErrNotExist) {
		target.Absent = true
		return target
	}
	if err != nil {
		target.Conflict = fmt.Sprintf("%s is unreadable: %v", harness.Display, err)
		return target
	}
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		link, readErr := os.Readlink(target.Path)
		if readErr != nil {
			target.Conflict = fmt.Sprintf("%s is unreadable: %v", harness.Display, readErr)
			return target
		}
		resolved, resolveErr := filepath.EvalSymlinks(target.Path)
		if resolveErr != nil {
			target.Conflict = fmt.Sprintf("%s is a link to %s, which does not resolve; left untouched", harness.Display, link)
			return target
		}
		root := paths.Root
		if resolvedRoot, rootErr := filepath.EvalSymlinks(root); rootErr == nil {
			root = resolvedRoot
		}
		if !pathInside(root, resolved) {
			target.Conflict = fmt.Sprintf("%s is a link to %s outside the managed checkout; left untouched", harness.Display, link)
			return target
		}
		resolvedInfo, statErr := os.Stat(resolved)
		if statErr != nil || !resolvedInfo.Mode().IsRegular() {
			target.Conflict = fmt.Sprintf("%s is a link to %s, which is not a regular file; left untouched", harness.Display, link)
			return target
		}
		target.Resolved = resolved
		target.Mode = resolvedInfo.Mode().Perm()
	case info.Mode().IsRegular():
		target.Resolved = target.Path
		target.Mode = info.Mode().Perm()
	default:
		target.Conflict = fmt.Sprintf("%s is not a regular file; left untouched", harness.Display)
	}
	return target
}

// mcpDocument is one harness file read into memory with the entries Config
// can interpret. Entries a harness spells in a way Config cannot represent
// are present with ok false, so they read as drift rather than as absent.
type mcpDocument struct {
	Target  mcpTarget
	Data    []byte
	Entries map[string]mcpLiveEntry
}

type mcpLiveEntry struct {
	Server MCPServer
	OK     bool
}

func readMCPDocument(target mcpTarget) (mcpDocument, error) {
	data, err := os.ReadFile(target.Resolved)
	if err != nil {
		return mcpDocument{}, err
	}
	document := mcpDocument{Target: target, Data: data}
	switch target.Harness.ID {
	case "claude-code":
		document.Entries, err = claudeMCPEntries(data)
	case "codex":
		document.Entries, err = codexMCPEntries(data)
	default:
		err = errors.New("unknown harness")
	}
	if err != nil {
		return mcpDocument{}, err
	}
	return document, nil
}

// edit replaces the declared entries and removes the named ones, preserving
// every other byte the harness file holds.
func (d mcpDocument) edit(set map[string]MCPServer, remove []string) ([]byte, error) {
	switch d.Target.Harness.ID {
	case "claude-code":
		return editClaudeMCPServers(d.Data, set, remove)
	case "codex":
		return editCodexMCPServers(d.Data, set, remove)
	}
	return nil, errors.New("unknown harness")
}

// Claude Code: the top-level mcpServers object of ~/.claude.json

type claudeMCPEntry struct {
	Type    string            `json:"type"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

func claudeMCPEntries(data []byte) (map[string]mcpLiveEntry, error) {
	_, members, _, err := jsonObjectMembers(data)
	if err != nil {
		return nil, err
	}
	entries := map[string]mcpLiveEntry{}
	member, found := members.find("mcpServers")
	if !found {
		return entries, nil
	}
	raw := data[member.ValueStart:member.End]
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return entries, nil
	}
	_, servers, _, err := jsonObjectMembers(raw)
	if err != nil {
		return nil, fmt.Errorf("mcpServers: %w", err)
	}
	for _, server := range servers {
		var entry claudeMCPEntry
		live := mcpLiveEntry{}
		if decodeExactJSON(raw[server.ValueStart:server.End], &entry) == nil {
			live.Server, live.OK = entry.server()
		}
		entries[server.Key] = live
	}
	return entries, nil
}

func (e claudeMCPEntry) server() (MCPServer, bool) {
	switch {
	case (e.Type == "stdio" || e.Type == "") && e.Command != "" && e.URL == "" && e.Headers == nil:
		return MCPServer{Command: e.Command, Args: e.Args, Env: e.Env}, true
	case e.Type == "http" && e.URL != "" && e.Command == "" && e.Args == nil && e.Env == nil:
		return MCPServer{URL: e.URL, Headers: e.Headers}, true
	}
	return MCPServer{}, false
}

// claudeEntry renders the documented user-scope shape: stdio entries always
// carry args and env, http entries always carry headers, and type is never
// omitted because Claude Code reads an entry without one as stdio.
func claudeEntry(server MCPServer) any {
	if server.Command != "" {
		return struct {
			Type    string            `json:"type"`
			Command string            `json:"command"`
			Args    []string          `json:"args"`
			Env     map[string]string `json:"env"`
		}{"stdio", server.Command, nonNil(server.Args), nonNilMap(server.Env)}
	}
	return struct {
		Type    string            `json:"type"`
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}{"http", server.URL, nonNilMap(server.Headers)}
}

func nonNil(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	return values
}

func editClaudeMCPServers(data []byte, set map[string]MCPServer, remove []string) ([]byte, error) {
	open, members, _, err := jsonObjectMembers(data)
	if err != nil {
		return nil, err
	}
	unit := jsonIndentUnit(data, open, members)
	member, found := members.find("mcpServers")
	var servers []byte
	if found && !bytes.Equal(bytes.TrimSpace(data[member.ValueStart:member.End]), []byte("null")) {
		servers = data[member.ValueStart:member.End]
	} else {
		servers = []byte("{}")
	}
	for _, name := range remove {
		if servers, err = removeJSONMember(servers, name); err != nil {
			return nil, fmt.Errorf("mcpServers: %w", err)
		}
	}
	for _, name := range sortedKeys(set) {
		value, err := renderJSONValue(claudeEntry(set[name]), unit, 2)
		if err != nil {
			return nil, err
		}
		if servers, err = setJSONMember(servers, name, value, unit, 1); err != nil {
			return nil, fmt.Errorf("mcpServers: %w", err)
		}
	}
	if found {
		return splice(data, member.ValueStart, member.End, servers), nil
	}
	return setJSONMember(data, "mcpServers", servers, unit, 0)
}

// JSON object surgery. Offsets come from the standard decoder, so the
// members a splice preserves are exactly the ones the harness will read.

type jsonMember struct {
	Key        string
	Start      int
	ValueStart int
	End        int
}

type jsonMembers []jsonMember

func (m jsonMembers) find(key string) (jsonMember, bool) {
	for _, member := range m {
		if member.Key == key {
			return member, true
		}
	}
	return jsonMember{}, false
}

func jsonObjectMembers(data []byte) (open int, members jsonMembers, closing int, err error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return 0, nil, 0, err
	}
	if token != json.Delim('{') {
		return 0, nil, 0, errors.New("document is not a JSON object")
	}
	open = int(decoder.InputOffset()) - 1
	previous := open + 1
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return 0, nil, 0, err
		}
		key, ok := token.(string)
		if !ok {
			return 0, nil, 0, errors.New("object key is not a string")
		}
		start := bytes.IndexByte(data[previous:], '"')
		if start < 0 {
			return 0, nil, 0, errors.New("object key has no opening quote")
		}
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return 0, nil, 0, err
		}
		end := int(decoder.InputOffset())
		if _, duplicate := members.find(key); duplicate {
			return 0, nil, 0, fmt.Errorf("object repeats key %q", key)
		}
		members = append(members, jsonMember{Key: key, Start: previous + start, ValueStart: end - len(raw), End: end})
		previous = end
	}
	token, err = decoder.Token()
	if err != nil {
		return 0, nil, 0, err
	}
	if token != json.Delim('}') {
		return 0, nil, 0, errors.New("object does not close")
	}
	closing = int(decoder.InputOffset()) - 1
	if len(bytes.TrimSpace(data[closing+1:])) != 0 {
		return 0, nil, 0, errors.New("JSON contains trailing data")
	}
	return open, members, closing, nil
}

// jsonIndentUnit reads the indentation the file already uses. A compact
// document yields an empty unit and stays compact.
func jsonIndentUnit(data []byte, open int, members jsonMembers) string {
	if len(members) == 0 {
		return "  "
	}
	between := string(data[open+1 : members[0].Start])
	newline := strings.LastIndexByte(between, '\n')
	if newline < 0 {
		return ""
	}
	return between[newline+1:]
}

func renderJSONValue(value any, unit string, depth int) ([]byte, error) {
	if unit == "" {
		return json.Marshal(value)
	}
	return json.MarshalIndent(value, strings.Repeat(unit, depth), unit)
}

func setJSONMember(object []byte, key string, value []byte, unit string, depth int) ([]byte, error) {
	open, members, closing, err := jsonObjectMembers(object)
	if err != nil {
		return nil, err
	}
	quoted, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	if member, found := members.find(key); found {
		return splice(object, member.ValueStart, member.End, value), nil
	}
	separator := ": "
	if unit == "" {
		separator = ":"
	}
	entry := append(append(append([]byte(nil), quoted...), separator...), value...)
	if len(members) == 0 {
		if unit == "" {
			return splice(object, open+1, closing, entry), nil
		}
		prefix := strings.Repeat(unit, depth+1)
		body := "\n" + prefix + string(entry) + "\n" + strings.Repeat(unit, depth)
		return splice(object, open+1, closing, []byte(body)), nil
	}
	last := members[len(members)-1]
	lead := ","
	if unit != "" {
		lead = ",\n" + strings.Repeat(unit, depth+1)
	}
	return splice(object, last.End, last.End, append([]byte(lead), entry...)), nil
}

func removeJSONMember(object []byte, key string) ([]byte, error) {
	open, members, closing, err := jsonObjectMembers(object)
	if err != nil {
		return nil, err
	}
	index := slices.IndexFunc(members, func(member jsonMember) bool { return member.Key == key })
	switch {
	case index < 0:
		return object, nil
	case len(members) == 1:
		return splice(object, open+1, closing, nil), nil
	case index == 0:
		return splice(object, members[0].Start, members[1].Start, nil), nil
	default:
		return splice(object, members[index-1].End, members[index].End, nil), nil
	}
}

func splice(data []byte, start, end int, replacement []byte) []byte {
	result := slices.Clone(data[:start])
	result = append(result, replacement...)
	return append(result, data[end:]...)
}

// Codex: [mcp_servers.<name>] tables in ~/.codex/config.toml

func codexMCPEntries(data []byte) (map[string]mcpLiveEntry, error) {
	document, err := parseTOML(data)
	if err != nil {
		return nil, err
	}
	entries := map[string]mcpLiveEntry{}
	servers, _ := document["mcp_servers"].(map[string]any)
	for name, value := range servers {
		live := mcpLiveEntry{}
		if table, ok := value.(map[string]any); ok {
			live.Server, live.OK = codexServer(table)
		}
		entries[name] = live
	}
	return entries, nil
}

func parseTOML(data []byte) (map[string]any, error) {
	document := map[string]any{}
	if err := toml.Unmarshal(data, &document); err != nil {
		return nil, err
	}
	return document, nil
}

func codexServer(table map[string]any) (MCPServer, bool) {
	var server MCPServer
	for key, value := range table {
		ok := true
		switch key {
		case "command":
			server.Command, ok = value.(string)
		case "url":
			server.URL, ok = value.(string)
		case "args":
			server.Args, ok = tomlStrings(value)
		case "env":
			server.Env, ok = tomlStringTable(value)
		case "http_headers":
			server.Headers, ok = tomlStringTable(value)
		default:
			ok = false
		}
		if !ok {
			return MCPServer{}, false
		}
	}
	switch {
	case server.Command != "" && server.URL == "" && server.Headers == nil:
		return server, true
	case server.URL != "" && server.Command == "" && server.Args == nil && server.Env == nil:
		return server, true
	}
	return MCPServer{}, false
}

func tomlStrings(value any) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}
	strings := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		strings = append(strings, text)
	}
	return strings, true
}

func tomlStringTable(value any) (map[string]string, bool) {
	table, ok := value.(map[string]any)
	if !ok {
		return nil, false
	}
	result := make(map[string]string, len(table))
	for key, item := range table {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		result[key] = text
	}
	return result, true
}

// renderCodexMCPServer spells one server the way the Codex documentation
// does: a table for the server and a sub-table for env or http_headers.
func renderCodexMCPServer(name string, server MCPServer) string {
	var out strings.Builder
	header := "mcp_servers." + tomlKey(name)
	fmt.Fprintf(&out, "[%s]\n", header)
	var sub string
	var table map[string]string
	if server.Command != "" {
		fmt.Fprintf(&out, "command = %s\n", tomlString(server.Command))
		if len(server.Args) > 0 {
			items := make([]string, len(server.Args))
			for index, argument := range server.Args {
				items[index] = tomlString(argument)
			}
			fmt.Fprintf(&out, "args = [%s]\n", strings.Join(items, ", "))
		}
		sub, table = "env", server.Env
	} else {
		fmt.Fprintf(&out, "url = %s\n", tomlString(server.URL))
		sub, table = "http_headers", server.Headers
	}
	if len(table) > 0 {
		fmt.Fprintf(&out, "\n[%s.%s]\n", header, sub)
		for _, key := range sortedKeys(table) {
			fmt.Fprintf(&out, "%s = %s\n", tomlKey(key), tomlString(table[key]))
		}
	}
	return out.String()
}

func tomlKey(key string) string {
	if tomlBareKeyPattern.MatchString(key) {
		return key
	}
	return tomlString(key)
}

// tomlString is a TOML basic string: every value Config writes is quoted the
// same way, whatever it contains.
func tomlString(value string) string {
	var out strings.Builder
	out.WriteByte('"')
	for _, character := range value {
		switch character {
		case '"':
			out.WriteString(`\"`)
		case '\\':
			out.WriteString(`\\`)
		case '\b':
			out.WriteString(`\b`)
		case '\t':
			out.WriteString(`\t`)
		case '\n':
			out.WriteString(`\n`)
		case '\f':
			out.WriteString(`\f`)
		case '\r':
			out.WriteString(`\r`)
		default:
			if character < 0x20 || character == 0x7f || character == utf8.RuneError {
				fmt.Fprintf(&out, `\u%04X`, character)
			} else {
				out.WriteRune(character)
			}
		}
	}
	out.WriteByte('"')
	return out.String()
}

// tomlHeaderPath reads a table header line into its key path, or nil when the
// line is not a header. Array-of-table headers are headers too: one for a
// declared name is a form Config replaces rather than one it preserves.
func tomlHeaderPath(line string) []string {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "[") {
		return nil
	}
	trimmed = strings.TrimPrefix(trimmed, "[")
	array := strings.HasPrefix(trimmed, "[")
	trimmed = strings.TrimPrefix(trimmed, "[")
	closing := "]"
	if array {
		closing = "]]"
	}
	end := -1
	quote := byte(0)
	for index := 0; index < len(trimmed); index++ {
		character := trimmed[index]
		switch {
		case quote != 0:
			if character == '\\' && quote == '"' {
				index++
			} else if character == quote {
				quote = 0
			}
		case character == '"' || character == '\'':
			quote = character
		case strings.HasPrefix(trimmed[index:], closing):
			end = index
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	rest := strings.TrimSpace(trimmed[end+len(closing):])
	if rest != "" && !strings.HasPrefix(rest, "#") {
		return nil
	}
	return tomlKeyPath(trimmed[:end])
}

// tomlKeyPath splits a dotted key into its parts, unquoting each one. It
// returns nil for a key it cannot read, and the caller treats that line as
// one to preserve.
func tomlKeyPath(text string) []string {
	var path []string
	text = strings.TrimSpace(text)
	for text != "" {
		var part string
		switch text[0] {
		case '"':
			closing := 1
			for closing < len(text) && text[closing] != '"' {
				if text[closing] == '\\' {
					closing++
				}
				closing++
			}
			if closing >= len(text) {
				return nil
			}
			unquoted, err := strconv.Unquote(text[:closing+1])
			if err != nil {
				return nil
			}
			part, text = unquoted, text[closing+1:]
		case '\'':
			closing := strings.IndexByte(text[1:], '\'')
			if closing < 0 {
				return nil
			}
			part, text = text[1:closing+1], text[closing+2:]
		default:
			end := strings.IndexAny(text, ". \t")
			if end < 0 {
				end = len(text)
			}
			part, text = text[:end], text[end:]
			if !tomlBareKeyPattern.MatchString(part) {
				return nil
			}
		}
		path = append(path, part)
		text = strings.TrimSpace(text)
		if text == "" {
			break
		}
		if text[0] != '.' {
			return nil
		}
		text = strings.TrimSpace(text[1:])
		if text == "" {
			return nil
		}
	}
	return path
}

// codexServerRanges finds every header-owned line range for one server: a
// header whose path starts with mcp_servers.<name>, through its last key.
// Blank and comment lines before the next header stay, because a comment
// there usually introduces the table that follows.
func codexServerRanges(lines []string, name string) [][2]int {
	var ranges [][2]int
	for index := 0; index < len(lines); index++ {
		path := tomlHeaderPath(lines[index])
		if len(path) < 2 || path[0] != "mcp_servers" || path[1] != name {
			continue
		}
		next := index + 1
		for next < len(lines) && tomlHeaderPath(lines[next]) == nil {
			next++
		}
		end := next
		for end > index+1 && tomlBlankOrComment(lines[end-1]) {
			end--
		}
		ranges = append(ranges, [2]int{index, end})
		index = next - 1
	}
	return ranges
}

func tomlBlankOrComment(line string) bool {
	trimmed := strings.TrimSpace(line)
	return trimmed == "" || strings.HasPrefix(trimmed, "#")
}

// editCodexMCPServers rewrites only the tables of the named servers and
// proves the result: the document minus those names must parse to exactly
// what it parsed to before, and each written name must read back as declared.
func editCodexMCPServers(data []byte, set map[string]MCPServer, remove []string) ([]byte, error) {
	before, err := parseTOML(data)
	if err != nil {
		return nil, err
	}
	names := append(sortedKeys(set), remove...)
	expected := tomlWithoutServers(before, names)
	lines := strings.SplitAfter(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	for _, name := range names {
		ranges := codexServerRanges(lines, name)
		insertAt := len(lines)
		for index := len(ranges) - 1; index >= 0; index-- {
			lines = slices.Delete(lines, ranges[index][0], ranges[index][1])
			insertAt = ranges[index][0]
		}
		remainder, err := parseTOML([]byte(strings.Join(lines, "")))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		if servers, _ := remainder["mcp_servers"].(map[string]any); servers[name] != nil {
			return nil, fmt.Errorf("%s is declared in a form Config cannot rewrite; left untouched", name)
		}
		server, declared := set[name]
		if !declared {
			// A removed table leaves the blank line before it and the one
			// after it side by side; keep one.
			for insertAt < len(lines) && strings.TrimSpace(lines[insertAt]) == "" &&
				(insertAt == 0 || strings.TrimSpace(lines[insertAt-1]) == "") {
				lines = slices.Delete(lines, insertAt, insertAt+1)
			}
			continue
		}
		block := renderCodexMCPServer(name, server)
		if insertAt < len(lines) {
			if strings.TrimSpace(lines[insertAt]) != "" {
				block += "\n"
			}
		} else if len(lines) > 0 {
			last := lines[len(lines)-1]
			if !strings.HasSuffix(last, "\n") {
				lines[len(lines)-1] = last + "\n"
				last += "\n"
			}
			if strings.TrimSpace(last) != "" {
				block = "\n" + block
			}
		}
		lines = slices.Insert(lines, insertAt, block)
	}
	result := []byte(strings.Join(lines, ""))
	after, err := parseTOML(result)
	if err != nil {
		return nil, fmt.Errorf("rewritten document does not parse: %w", err)
	}
	if !reflect.DeepEqual(tomlWithoutServers(after, names), expected) {
		return nil, errors.New("rewriting the declared servers would change other configuration; left untouched")
	}
	servers, _ := after["mcp_servers"].(map[string]any)
	for name, server := range set {
		table, _ := servers[name].(map[string]any)
		live, ok := codexServer(table)
		if !ok || !sameMCPServer(live, server) {
			return nil, fmt.Errorf("%s did not read back as declared", name)
		}
	}
	for _, name := range remove {
		if servers[name] != nil {
			return nil, fmt.Errorf("%s did not read back as removed", name)
		}
	}
	return result, nil
}

func tomlWithoutServers(document map[string]any, names []string) map[string]any {
	result := make(map[string]any, len(document))
	for key, value := range document {
		result[key] = value
	}
	servers, ok := document["mcp_servers"].(map[string]any)
	if !ok {
		return result
	}
	remaining := make(map[string]any, len(servers))
	for key, value := range servers {
		if !slices.Contains(names, key) {
			remaining[key] = value
		}
	}
	if len(remaining) == 0 {
		delete(result, "mcp_servers")
	} else {
		result["mcp_servers"] = remaining
	}
	return result
}

// Inspection and reconciliation

type mcpHarnessState struct {
	Target   mcpTarget
	Document mcpDocument
	Drifted  []string
	Pending  map[string]MCPServer
}

// assessMCPHarness reads one harness and names what its declared entries
// still need. It never writes.
func assessMCPHarness(paths Paths, ledger mcpServerLedger, harness mcpHarness, desired map[string]MCPServer) mcpHarnessState {
	state := mcpHarnessState{Target: resolveMCPTarget(paths, harness), Pending: map[string]MCPServer{}}
	if state.Target.Absent || state.Target.Conflict != "" {
		return state
	}
	document, err := readMCPDocument(state.Target)
	if err != nil {
		state.Target.Conflict = fmt.Sprintf("%s is unreadable: %v", harness.Display, err)
		return state
	}
	state.Document = document
	owned := ledger.Agents[harness.ID]
	for _, name := range sortedKeys(desired) {
		server := desired[name]
		live, found := document.Entries[name]
		switch {
		case !found:
			state.Drifted = append(state.Drifted, name+" is missing for "+harness.Name)
			state.Pending[name] = server
		case !live.OK || !sameMCPServer(live.Server, server):
			state.Drifted = append(state.Drifted, name+" differs for "+harness.Name)
			state.Pending[name] = server
		case owned[name] != server.digest():
			state.Drifted = append(state.Drifted, name+" ownership needs adoption for "+harness.Name)
		}
	}
	return state
}

func inspectMCPServers(paths Paths, servers MCPServers) Resource {
	resource := Resource{ID: mcpServersID, Name: mcpServersName, Authoritative: true}
	ledger, _, err := readMCPServerLedger(paths)
	if err != nil {
		resource.State = Unavailable
		resource.Summary = "MCP server ownership is unreadable"
		resource.Checks = []Check{no("MCP server ownership readable", err.Error())}
		return resource
	}
	desired := servers.desired()
	var drifted, conflicts, absent []string
	present := 0
	for _, harness := range servers.harnessOrder() {
		state := assessMCPHarness(paths, ledger, harness, desired[harness.ID])
		switch {
		case state.Target.Absent:
			absent = append(absent, harness.Name+" is not present; "+harness.Display+" converges once it exists")
		case state.Target.Conflict != "":
			conflicts = append(conflicts, state.Target.Conflict)
		default:
			present++
			drifted = append(drifted, state.Drifted...)
		}
	}
	resource.Details = append(resource.Details, drifted...)
	resource.Details = append(resource.Details, conflicts...)
	if len(conflicts) > 0 {
		resource.Checks = append(resource.Checks, no("MCP servers safe to manage", strings.Join(conflicts, ", ")))
	}
	switch {
	case len(drifted) > 0:
		resource.Checks = append(resource.Checks, no("Declared MCP servers current", strings.Join(drifted, ", ")))
		resource.Actions = []Action{Apply}
		resource.ActionLabels = map[Action]string{Apply: "Reconcile MCP servers"}
	case present > 0:
		resource.Checks = append(resource.Checks, yes("Declared MCP servers current"))
	}
	for _, note := range absent {
		resource.Checks = append(resource.Checks, yes(note))
	}
	switch {
	case len(conflicts) > 0:
		resource.State = Drift
		resource.Summary = FormatCount(len(conflicts), "harness conflicts with Config ownership", "harnesses conflict with Config ownership")
	case len(drifted) > 0:
		resource.State = Drift
		resource.Summary = FormatCount(len(drifted), "MCP server change needs reconciliation", "MCP server changes need reconciliation")
	case present == 0:
		resource.State = Unavailable
		resource.Summary = "No declared agent harness is present"
	default:
		resource.State = Current
		resource.Summary = FormatCount(len(servers.Servers), "MCP server current", "MCP servers current")
	}
	return resource
}

type mcpServerManager struct {
	Paths   Paths
	Servers MCPServers
	Log     Logger
}

// Reconcile converges every present harness independently. A harness that is
// absent is skipped and said so; a harness Config must not write is a failure
// beside the others, not a reason to stop them.
func (m mcpServerManager) Reconcile() error {
	ledger, _, err := readMCPServerLedger(m.Paths)
	if err != nil {
		return fmt.Errorf("read MCP server ownership: %w", err)
	}
	desired := m.Servers.desired()
	var failures []error
	for _, harness := range m.Servers.harnessOrder() {
		state := assessMCPHarness(m.Paths, ledger, harness, desired[harness.ID])
		if state.Target.Absent {
			m.Log.Info(harness.Name + " is not present; " + harness.Display + " converges once it exists")
			continue
		}
		if state.Target.Conflict != "" {
			failures = append(failures, errors.New(state.Target.Conflict))
			continue
		}
		var data []byte
		if len(state.Pending) > 0 {
			var err error
			if data, err = state.Document.edit(state.Pending, nil); err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", harness.Name, err))
				continue
			}
		}
		// Ownership is recorded before the write it claims. A claim on an
		// entry that is not there is completed by the next apply and ignored
		// by prune; an entry no record accounts for could never be pruned.
		owned := ledger.Agents[harness.ID]
		if owned == nil {
			owned = map[string]string{}
			ledger.Agents[harness.ID] = owned
		}
		for name, server := range desired[harness.ID] {
			owned[name] = server.digest()
		}
		if err := writeMCPServerLedger(m.Paths, ledger); err != nil {
			failures = append(failures, fmt.Errorf("record MCP server ownership: %w", err))
			continue
		}
		if data == nil {
			m.Log.OK(harness.Name + ": " + FormatCount(len(desired[harness.ID]), "server current", "servers current"))
			continue
		}
		if err := AtomicWrite(state.Target.Resolved, data, state.Target.Mode); err != nil {
			failures = append(failures, fmt.Errorf("write %s: %w", harness.Display, err))
			continue
		}
		m.Log.OK(harness.Name + ": " + FormatCount(len(state.Pending), "server written to "+harness.Display, "servers written to "+harness.Display))
	}
	return errors.Join(failures...)
}

func (e Applier) mcpServerManager() mcpServerManager {
	return mcpServerManager{Paths: e.Paths, Servers: *e.Machine.MCPServers, Log: e.Log}
}

// Pruning

// planMCPServers names the entries Config wrote that the machine no longer
// declares for that harness. An entry is removed only while it still reads as
// what Config wrote; anything else is reported and left alone.
func (p Pruner) planMCPServers(warnings []string) (pruneMCPServers, []string) {
	ledger, data, err := readMCPServerLedger(p.Paths)
	if err != nil {
		return pruneMCPServers{}, append(warnings, "MCP server ownership is unrecognised; left untouched")
	}
	if len(data) == 0 || len(ledger.Agents) == 0 {
		return pruneMCPServers{}, warnings
	}
	var desired map[string]map[string]MCPServer
	if p.Machine.MCPServers != nil {
		desired = p.Machine.MCPServers.desired()
	}
	plan := pruneMCPServers{LedgerDigest: contentDigest(data)}
	for _, harness := range mcpHarnesses {
		owned := ledger.Agents[harness.ID]
		var candidates []string
		for _, name := range sortedKeys(owned) {
			if _, declared := desired[harness.ID][name]; !declared {
				candidates = append(candidates, name)
			}
		}
		if len(candidates) == 0 {
			continue
		}
		target := resolveMCPTarget(p.Paths, harness)
		if target.Conflict != "" {
			warnings = append(warnings, target.Conflict)
			continue
		}
		var document mcpDocument
		if !target.Absent {
			document, err = readMCPDocument(target)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("%s is unreadable; left untouched: %v", harness.Display, err))
				continue
			}
		}
		for _, name := range candidates {
			live, found := document.Entries[name]
			switch {
			case target.Absent || !found:
				plan.Servers = append(plan.Servers, pruneMCPServer{Agent: harness.ID, Name: name})
			case live.OK && live.Server.digest() == owned[name]:
				plan.Servers = append(plan.Servers, pruneMCPServer{Agent: harness.ID, Name: name, RemoveEntry: true})
			default:
				warnings = append(warnings, name+" for "+harness.Name+" changed since Config wrote it; left untouched")
			}
		}
	}
	return plan, warnings
}

func (p Pruner) applyPruneMCPServers(plan pruneMCPServers) error {
	ledger, data, err := readMCPServerLedger(p.Paths)
	if err != nil {
		return err
	}
	if contentDigest(data) != plan.LedgerDigest {
		return errors.New("MCP server ownership changed after preview")
	}
	var failures []error
	for _, harness := range mcpHarnesses {
		var forget, remove []string
		for _, server := range plan.Servers {
			if server.Agent != harness.ID {
				continue
			}
			forget = append(forget, server.Name)
			if server.RemoveEntry {
				remove = append(remove, server.Name)
			}
		}
		if len(remove) > 0 {
			target := resolveMCPTarget(p.Paths, harness)
			if target.Absent || target.Conflict != "" {
				failures = append(failures, fmt.Errorf("%s changed after preview; left untouched", harness.Display))
				continue
			}
			document, err := readMCPDocument(target)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", harness.Display, err))
				continue
			}
			changed := false
			for _, name := range remove {
				live, found := document.Entries[name]
				if !found || !live.OK || live.Server.digest() != ledger.Agents[harness.ID][name] {
					changed = true
				}
			}
			if changed {
				failures = append(failures, fmt.Errorf("%s entries changed after preview; left untouched", harness.Name))
				continue
			}
			edited, err := document.edit(nil, remove)
			if err != nil {
				failures = append(failures, fmt.Errorf("%s: %w", harness.Name, err))
				continue
			}
			if err := AtomicWrite(target.Resolved, edited, target.Mode); err != nil {
				failures = append(failures, fmt.Errorf("write %s: %w", harness.Display, err))
				continue
			}
		}
		for _, name := range forget {
			delete(ledger.Agents[harness.ID], name)
		}
	}
	if err := writeMCPServerLedger(p.Paths, ledger); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
