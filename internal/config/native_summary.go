package config

import (
	"fmt"
	"path"
	"regexp"
	"strings"
)

// nativeSummary describes the changes in a native dry run in lines a person
// can review. mise prints the commands it would run; each command Config
// recognises becomes part of one line per kind of change. Commands it does not
// recognise are listed as they are, so the summary never hides a step.
type nativeSummary struct {
	Lines []string
	// Elevated is true when a step runs an installer or command as root.
	Elevated bool
}

var (
	nativeEnvironmentWord = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)
	nativeCommandWord     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9._-]*$`)
	nativePruneRemoval    = regexp.MustCompile(`^mise (\S+)@(\S+) \[dryrun\]\s+uninstall$`)
)

// Lines that belong to a change already counted from an earlier line.
var nativeChangeParts = []string{
	"install pkg ", "run installer ", "link app ", "link binary ", "link command wrapper ",
	"install artifact ", "install font ", "generate completions from ", "repo: ",
	"bootstrap: follow-up if applied", "- relaunch apps",
}

type nativeList struct {
	seen  map[string]bool
	items []string
}

func (l *nativeList) add(item string) {
	if l.seen == nil {
		l.seen = make(map[string]bool)
	}
	if !l.seen[item] {
		l.seen[item] = true
		l.items = append(l.items, item)
	}
}

func (l nativeList) line(verb, singular, plural, suffix string) string {
	if len(l.items) == 0 {
		return ""
	}
	names := l.items
	more := ""
	if len(names) > 8 {
		names, more = names[:8], fmt.Sprintf(" and %d more", len(l.items)-8)
	}
	return verb + " " + FormatCount(len(l.items), singular, plural) + ": " + strings.Join(names, ", ") + more + suffix
}

func summarizeNative(changes string) nativeSummary {
	var summary nativeSummary
	var apps, tools, clones, repositories, skillSources, preferences, edits, hooks, removedTools, removedPackages nativeList
	var plugins, others []string
	appStore := map[string]int{}
	for line := range strings.SplitSeq(changes, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "install pkg ") || strings.HasPrefix(line, "sudo ") ||
			strings.HasPrefix(line, "run installer ") && strings.HasSuffix(line, " with sudo"):
			summary.Elevated = true
			if strings.HasPrefix(line, "sudo ") {
				others = append(others, line)
			}
		case strings.HasPrefix(line, "install cask "):
			token, _, _ := strings.Cut(strings.TrimPrefix(line, "install cask "), "/")
			apps.add(token)
		case strings.HasPrefix(line, "Would install "):
			tool := strings.TrimPrefix(line, "Would install ")
			if at := strings.LastIndex(tool, "@"); at > 0 {
				tool = nativeToolName(tool[:at]) + " " + tool[at+1:]
			}
			tools.add(tool)
		case strings.HasPrefix(line, "mas install ") || strings.HasPrefix(line, "mas upgrade "):
			words := strings.Fields(line)
			appStore[words[1]] += len(words) - 2
		case strings.HasPrefix(line, "git ") || strings.HasPrefix(line, "cd "):
			words := nativeShellWords(line)
			if len(words) > 2 && words[1] == "clone" {
				clones.add(nativeRepositoryName(words[len(words)-1]))
				continue
			}
			dir := ""
			if len(words) > 1 && words[0] == "cd" {
				dir = words[1]
			} else if len(words) > 2 && words[1] == "-C" {
				dir = words[2]
			}
			switch {
			case dir == "":
				others = append(others, line)
			case strings.Contains(dir, "/skill-sources/"):
				skillSources.add(nativeSkillSource(dir))
			default:
				repositories.add(nativeRepositoryName(dir))
			}
		case strings.HasPrefix(line, "sh -o errexit -c "):
			hooks.add(nativeHookName(line))
		case strings.HasPrefix(line, "edit "):
			target, _, _ := strings.Cut(strings.TrimPrefix(line, "edit "), " (")
			edits.add(path.Base(target))
		case strings.HasPrefix(line, "macOS preference "):
			preferences.add(nativePreference(strings.TrimPrefix(line, "macOS preference ")))
		case strings.HasPrefix(line, "mise ["):
			plugin, command, _ := strings.Cut(strings.TrimPrefix(line, "mise ["), "] ")
			plugins = append(plugins, plugin+": "+command)
		case strings.HasPrefix(line, "remove "):
			pkg, version, _ := strings.Cut(strings.TrimPrefix(line, "remove "), "@")
			if strings.HasPrefix(pkg, "brew-cask:") {
				summary.Elevated = true
			}
			removedPackages.add(strings.TrimSpace(pkg + " " + version))
		case nativePruneRemoval.MatchString(line):
			match := nativePruneRemoval.FindStringSubmatch(line)
			removedTools.add(nativeToolName(match[1]) + " " + match[2])
		case strings.HasPrefix(line, "mise ") && (strings.Contains(line, " [dryrun] ") || strings.Contains(line, " is prunable: ")):
		case nativeChangePart(line):
		default:
			others = append(others, line)
		}
	}
	if summary.Elevated {
		summary.Lines = append(summary.Lines, "Needs your administrator password")
	}
	for _, line := range []string{
		apps.line("Install", "app", "apps", ""),
		tools.line("Upgrade", "tool", "tools", ""),
	} {
		if line != "" {
			summary.Lines = append(summary.Lines, line)
		}
	}
	for _, verb := range []string{"install", "upgrade"} {
		if count := appStore[verb]; count > 0 {
			label := "Update"
			if verb == "install" {
				label = "Install"
			}
			summary.Lines = append(summary.Lines, label+" "+FormatCount(count, "App Store app", "App Store apps"))
		}
	}
	for _, line := range []string{
		clones.line("Clone", "repository", "repositories", ""),
		repositories.line("Update", "repository", "repositories", "; fast-forward only"),
		skillSources.line("Refresh", "skill source", "skill sources", ""),
		preferences.line("Set", "preference", "preferences", ""),
		edits.line("Edit", "file", "files", " if it changed"),
		hooks.line("Run", "hook", "hooks", "; hooks run every time"),
		removedTools.line("Remove", "unused tool version", "unused tool versions", ""),
		removedPackages.line("Remove", "package", "packages", ""),
	} {
		if line != "" {
			summary.Lines = append(summary.Lines, line)
		}
	}
	summary.Lines = append(summary.Lines, plugins...)
	if len(summary.Lines) == 0 && len(others) == 0 && changes != "" {
		others = strings.Split(changes, "\n")
	}
	if len(others) > 0 {
		summary.Lines = append(summary.Lines, "Other steps:")
		for _, line := range others {
			summary.Lines = append(summary.Lines, "  "+line)
		}
	}
	return summary
}

func nativeChangePart(line string) bool {
	for _, prefix := range nativeChangeParts {
		if strings.HasPrefix(line, prefix) {
			return true
		}
	}
	return strings.HasPrefix(line, "install ") && strings.Contains(line, " completion ")
}

// nativeToolName drops a tool's backend and owner: "github:owner/tool" reads
// as "tool".
func nativeToolName(tool string) string {
	if _, name, found := strings.Cut(tool, ":"); found {
		tool = name
	}
	return path.Base(tool)
}

func nativeRepositoryName(dir string) string {
	return path.Base(strings.TrimSuffix(dir, "/"))
}

func nativeSkillSource(dir string) string {
	_, source, _ := strings.Cut(dir, "/skill-sources/")
	return source
}

// nativeHookName names a hook by its program and subcommand, such as
// "op-agent setup" or "killall Dock".
func nativeHookName(line string) string {
	words := nativeShellWords(line)
	script := words[len(words)-1]
	command := nativeShellWords(script)
	for len(command) > 0 && nativeEnvironmentWord.MatchString(command[0]) {
		command = command[1:]
	}
	if len(command) == 0 {
		return script
	}
	name := path.Base(command[0])
	if len(command) > 1 && nativeCommandWord.MatchString(command[1]) {
		name += " " + command[1]
	}
	return name
}

var nativePreferenceDomains = map[string]string{
	"com.apple.dock":          "Dock",
	"com.apple.finder":        "Finder",
	"NSGlobalDomain":          "global",
	"com.apple.screencapture": "screenshots",
}

func nativePreference(text string) string {
	setting, value, _ := strings.Cut(text, " = ")
	domain, key, found := strings.Cut(setting, " ")
	if !found {
		return text
	}
	if name, ok := nativePreferenceDomains[domain]; ok {
		domain = name
	}
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "[") {
		items := 0
		if value != "[]" {
			items = strings.Count(value, `","`) + 1
		}
		return fmt.Sprintf("%s %s (%s)", domain, key, FormatCount(items, "item", "items"))
	}
	return domain + " " + key + " = " + value
}

// nativeShellWords splits a command line printed by mise into words, honouring
// single quotes, double quotes and backslash escapes.
func nativeShellWords(line string) []string {
	var words []string
	var word strings.Builder
	inWord, quote, escaped := false, rune(0), false
	for _, r := range line {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case quote == '\'':
			if r == '\'' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\\':
			escaped, inWord = true, true
		case quote == '"':
			if r == '"' {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ' ' || r == '\t':
			if inWord {
				words = append(words, word.String())
				word.Reset()
				inWord = false
			}
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	if inWord {
		words = append(words, word.String())
	}
	return words
}
