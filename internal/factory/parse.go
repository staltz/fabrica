package factory

import (
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/staltz/fabrica/internal/names"
	"github.com/staltz/fabrica/internal/yamlx"
)

// textExtensions are the file types a text-only skill may contain.
var textExtensions = map[string]bool{
	".md": true, ".txt": true, ".json": true, ".yaml": true, ".yml": true, ".csv": true,
}

// skillFrontmatter holds the two fields of SKILL.md that Fabrica cares
// about. Skills are a standard format that other tools extend, so unlike
// Fabrica's own files we allow (and keep) keys we don't know.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseSkill reads a skill folder. name is the folder's name, which is the
// skill's name.
func parseSkill(dir, name, file string) *Skill {
	s := &Skill{ItemInfo: ItemInfo{Ref: names.Ref{Kind: names.Skills, Name: name}, File: file}, Dir: dir}
	fail := func(format string, args ...any) *Skill {
		s.Problems.Errorf("skill.invalid", file, format, args...)
		return s
	}
	if err := names.Validate(name); err != nil {
		return fail("the folder name isn't a valid skill name: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		return fail("there's no SKILL.md in this folder")
	}
	fm, err := yamlx.SplitFrontmatter(data)
	if err != nil {
		return fail("SKILL.md: %v", err)
	}
	if !fm.Present {
		return fail("SKILL.md has no frontmatter; the Agent Skills format needs a `---` block with name and description at the top")
	}
	var front skillFrontmatter
	if err := yamlx.DecodeLenient(fm.YAML, &front); err != nil {
		return fail("SKILL.md frontmatter: %v", err)
	}
	switch {
	case front.Name == "":
		return fail("SKILL.md frontmatter has no name; add `name: %s`", name)
	case front.Name != name:
		return fail("SKILL.md says its name is %q, but its folder is called %q; the Agent Skills format needs them to match", front.Name, name)
	case strings.TrimSpace(front.Description) == "":
		return fail("SKILL.md frontmatter has no description; agents use it to decide when the skill is relevant")
	case len(front.Description) > 1024:
		return fail("SKILL.md description is %d characters; the Agent Skills format allows at most 1024", len(front.Description))
	}
	s.Description = strings.TrimSpace(front.Description)
	s.Body = fm.Body

	hash, files, err := hashTree(dir)
	if err != nil {
		return fail("%v", err)
	}
	s.Hash, s.Files = hash, files
	s.TextOnly = true
	for _, f := range files {
		ext := strings.ToLower(path.Ext(f.Path))
		switch {
		case f.Executable:
			s.TextOnly, s.NotTextOnlyReason = false, fmt.Sprintf("%s is executable", f.Path)
		case !textExtensions[ext]:
			s.TextOnly, s.NotTextOnlyReason = false, fmt.Sprintf("%s isn't Markdown, plain text or data (.md, .txt, .json, .yaml, .yml, .csv)", f.Path)
		}
		if !s.TextOnly {
			break
		}
	}
	return s
}

// routineFile is the frontmatter of routines/<name>.md.
type routineFile struct {
	Description string               `yaml:"description"`
	Trigger     *triggerFile         `yaml:"trigger"`
	Skills      []string             `yaml:"skills"`
	Connectors  []string             `yaml:"connectors"`
	SideEffects []yaml.Node          `yaml:"side_effects"`
	Params      map[string]paramFile `yaml:"params"`
}

type triggerFile struct {
	On   string `yaml:"on"`
	Cron string `yaml:"cron"`
}

type paramFile struct {
	Required    bool       `yaml:"required"`
	Default     *yaml.Node `yaml:"default"`
	Description string     `yaml:"description"`
}

// parseRoutine reads routines/<name>.md.
func parseRoutine(data []byte, name, file string) *Routine {
	r := &Routine{ItemInfo: ItemInfo{Ref: names.Ref{Kind: names.Routines, Name: name}, File: file, Hash: hashBytes(data)}}
	fail := func(format string, args ...any) *Routine {
		r.Problems.Errorf("routine.invalid", file, format, args...)
		return r
	}
	if err := names.Validate(name); err != nil {
		return fail("the file name isn't a valid routine name: %v", err)
	}
	fm, err := yamlx.SplitFrontmatter(data)
	if err != nil {
		return fail("%v", err)
	}
	if !fm.Present {
		return fail("a routine needs frontmatter (a `---` block at the top) with at least a trigger")
	}
	var f routineFile
	if err := yamlx.DecodeStrict(fm.YAML, &f); err != nil {
		return fail("%v", err)
	}
	r.Description = strings.TrimSpace(f.Description)
	r.Prompt = strings.TrimSpace(fm.Body)
	if r.Prompt == "" {
		return fail("the routine has no prompt; write its instructions below the frontmatter")
	}

	// Trigger.
	switch {
	case f.Trigger == nil || f.Trigger.On == "":
		return fail("trigger.on is missing; say what fires the routine, like `trigger: { on: github.pull_request.opened }`")
	case !triggerRe.MatchString(f.Trigger.On):
		return fail("trigger.on %q isn't a valid event name (like github.pull_request.opened or schedule)", f.Trigger.On)
	case f.Trigger.On == "schedule" && f.Trigger.Cron == "":
		return fail("a schedule trigger needs a cron expression, like `trigger: { on: schedule, cron: \"0 9 * * 1\" }`")
	case f.Trigger.On != "schedule" && f.Trigger.Cron != "":
		return fail("trigger.cron only makes sense with `on: schedule`")
	}
	if f.Trigger.Cron != "" {
		if err := validateCron(f.Trigger.Cron); err != nil {
			return fail("%v", err)
		}
	}
	r.Trigger = Trigger{On: f.Trigger.On, Cron: strings.Join(strings.Fields(f.Trigger.Cron), " ")}

	// Skills.
	seen := map[string]bool{}
	for _, s := range f.Skills {
		if err := names.Validate(s); err != nil {
			return fail("skills: %v", err)
		}
		if seen[s] {
			return fail("skills: lists %q twice", s)
		}
		seen[s] = true
		r.Skills = append(r.Skills, s)
	}

	// Connectors.
	r.Connectors, err = names.ParseMaybeNames(f.Connectors)
	if err != nil {
		return fail("connectors: %v", err)
	}
	for _, c := range r.Connectors {
		if c.Name == "fabrica" {
			return fail("connectors: don't list fabrica; Fabrica's own MCP server is added to every routine automatically")
		}
	}

	// Params.
	for pname, p := range f.Params {
		if !paramNameRe.MatchString(pname) {
			return fail("params: %q isn't a valid param name; use lowercase letters, digits and underscores (like slack_channel)", pname)
		}
		param := Param{Name: pname, Required: p.Required, Description: strings.TrimSpace(p.Description)}
		if p.Default != nil {
			if p.Default.Kind != yaml.ScalarNode {
				return fail("params.%s.default must be a single value, not a list or map", pname)
			}
			if p.Required {
				return fail("params.%s is required, so a default would never be used; drop one of them", pname)
			}
			param.Default, param.HasDefault = p.Default.Value, true
		}
		r.Params = append(r.Params, param)
	}
	sort.Slice(r.Params, func(i, j int) bool { return r.Params[i].Name < r.Params[j].Name })

	// Side effects.
	connectorSet := map[string]bool{}
	for _, c := range r.Connectors {
		connectorSet[c.Name] = true
	}
	seenEffects := map[string]bool{}
	for _, node := range f.SideEffects {
		se, err := parseSideEffect(&node)
		if err != nil {
			return fail("side_effects: %v", err)
		}
		if seenEffects[se.Name] {
			return fail("side_effects: lists %q twice", se.Name)
		}
		seenEffects[se.Name] = true
		if !connectorSet[se.Connector()] {
			return fail("side effect %q goes through connector %q, which isn't in this routine's connectors: list", se.Name, se.Connector())
		}
		r.SideEffects = append(r.SideEffects, se)
	}

	// Every {{params.x}} must be declared.
	texts := []string{r.Prompt}
	for _, se := range r.SideEffects {
		for _, a := range se.Args {
			texts = append(texts, a.Value)
		}
	}
	for _, text := range texts {
		refs, err := ParamRefs(text)
		if err != nil {
			return fail("%v", err)
		}
		for _, ref := range refs {
			if _, ok := r.Param(ref); !ok {
				return fail("{{params.%s}} is used, but there's no %q under params:", ref, ref)
			}
		}
	}
	return r
}

func parseSideEffect(node *yaml.Node) (SideEffect, error) {
	var se SideEffect
	switch node.Kind {
	case yaml.ScalarNode:
		se.Name = node.Value
	case yaml.MappingNode:
		if len(node.Content) != 2 {
			return se, fmt.Errorf("line %d: each side effect is either a name or a single `name: {settings}` pair", node.Line)
		}
		se.Name = node.Content[0].Value
		args := node.Content[1]
		switch {
		case args.Kind == yaml.ScalarNode && args.Tag == "!!null":
		case args.Kind == yaml.MappingNode:
			for i := 0; i < len(args.Content); i += 2 {
				k, v := args.Content[i], args.Content[i+1]
				if v.Kind != yaml.ScalarNode {
					return se, fmt.Errorf("line %d: setting %q of %s must be a single value", v.Line, k.Value, se.Name)
				}
				se.Args = append(se.Args, Arg{Key: k.Value, Value: v.Value})
			}
		default:
			return se, fmt.Errorf("line %d: the settings of %s must be a map, like `{ channel: \"#eng\" }`", args.Line, se.Name)
		}
	default:
		return se, fmt.Errorf("line %d: each side effect is either a name or a single `name: {settings}` pair", node.Line)
	}
	if !sideEffectRe.MatchString(se.Name) {
		return se, fmt.Errorf("%q isn't a valid side effect; write it as connector.action, like github.pr_comment", se.Name)
	}
	return se, nil
}

// ruleFile is the frontmatter of a rule in YAML form.
type ruleFile struct {
	Description string    `yaml:"description"`
	Globs       yaml.Node `yaml:"globs"`
	AlwaysApply bool      `yaml:"alwaysApply"`
}

// parseRule reads a rule file in Cursor's .mdc format.
func parseRule(data []byte, name, file string) *Rule {
	r := &Rule{ItemInfo: ItemInfo{Ref: names.Ref{Kind: names.Rules, Name: name}, File: file, Hash: hashBytes(data)}}
	fail := func(format string, args ...any) *Rule {
		r.Problems.Errorf("rule.invalid", file, format, args...)
		return r
	}
	if err := names.Validate(name); err != nil {
		return fail("the file name isn't a valid rule name: %v", err)
	}
	fm, err := yamlx.SplitFrontmatter(data)
	if err != nil {
		return fail("%v", err)
	}
	r.Body = strings.TrimSpace(fm.Body)
	if r.Body == "" {
		return fail("the rule is empty; write its instructions below the frontmatter")
	}
	if !fm.Present {
		return r // no frontmatter: only applies when asked for by name
	}
	desc, globs, always, err := parseRuleFrontmatter(fm.YAML)
	if err != nil {
		return fail("%v", err)
	}
	r.Description, r.Globs, r.AlwaysApply = desc, globs, always
	return r
}

// parseRuleFrontmatter reads a rule's three settings. Cursor's own files
// aren't always valid YAML (`globs: *.ts` would be a YAML alias), so when
// YAML can't read the frontmatter we fall back to Cursor's simpler
// `key: value` lines. Either way, only Cursor's three keys are allowed.
func parseRuleFrontmatter(front []byte) (description string, globs []string, alwaysApply bool, err error) {
	var f ruleFile
	if yamlErr := yamlx.DecodeStrict(front, &f); yamlErr == nil {
		globs, err := globsFromNode(&f.Globs)
		return strings.TrimSpace(f.Description), globs, f.AlwaysApply, err
	} else if strings.Contains(yamlErr.Error(), "unknown key") {
		return "", nil, false, fmt.Errorf("%v (rules only have description, globs and alwaysApply)", yamlErr)
	}
	return parseCursorLines(string(front))
}

func globsFromNode(n *yaml.Node) ([]string, error) {
	switch {
	case n.Kind == 0, n.Kind == yaml.ScalarNode && (n.Tag == "!!null" || n.Value == ""):
		return nil, nil
	case n.Kind == yaml.ScalarNode:
		return splitGlobs(n.Value), nil
	case n.Kind == yaml.SequenceNode:
		var out []string
		for _, item := range n.Content {
			if item.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("globs: every entry must be a pattern like \"src/**/*.ts\"")
			}
			out = append(out, splitGlobs(item.Value)...)
		}
		return out, nil
	}
	return nil, fmt.Errorf("globs: must be a pattern or a list of patterns")
}

func splitGlobs(s string) []string {
	var out []string
	for _, g := range strings.Split(s, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

func parseCursorLines(front string) (description string, globs []string, alwaysApply bool, err error) {
	seen := map[string]bool{}
	for i, line := range strings.Split(front, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok {
			return "", nil, false, fmt.Errorf("frontmatter line %d isn't a `key: value` line", i+1)
		}
		if seen[key] {
			return "", nil, false, fmt.Errorf("frontmatter sets %s twice", key)
		}
		seen[key] = true
		switch key {
		case "description":
			description = unquote(value)
		case "globs":
			globs = splitGlobs(unquote(value))
		case "alwaysApply":
			switch value {
			case "true":
				alwaysApply = true
			case "false", "":
			default:
				return "", nil, false, fmt.Errorf("alwaysApply must be true or false, not %q", value)
			}
		default:
			return "", nil, false, fmt.Errorf("unknown key %q (rules only have description, globs and alwaysApply)", key)
		}
	}
	return description, globs, alwaysApply, nil
}

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// connectorFile is connectors/<name>.yaml.
type connectorFile struct {
	Kind        string   `yaml:"kind"`
	Description string   `yaml:"description"`
	URL         string   `yaml:"url"`
	Transport   string   `yaml:"transport"`
	Auth        string   `yaml:"auth"`
	Secret      string   `yaml:"secret"`
	Command     string   `yaml:"command"`
	Args        []string `yaml:"args"`
	Env         []string `yaml:"env"`
	Fallback    *mcpFile `yaml:"fallback"`
}

type mcpFile struct {
	URL       string   `yaml:"url"`
	Transport string   `yaml:"transport"`
	Auth      string   `yaml:"auth"`
	Secret    string   `yaml:"secret"`
	Command   string   `yaml:"command"`
	Args      []string `yaml:"args"`
	Env       []string `yaml:"env"`
}

// parseConnector reads connectors/<name>.yaml.
func parseConnector(data []byte, name, file string) *Connector {
	c := &Connector{ItemInfo: ItemInfo{Ref: names.Ref{Kind: names.Connectors, Name: name}, File: file, Hash: hashBytes(data)}}
	fail := func(format string, args ...any) *Connector {
		c.Problems.Errorf("connector.invalid", file, format, args...)
		return c
	}
	if err := names.Validate(name); err != nil {
		return fail("the file name isn't a valid connector name: %v", err)
	}
	if name == "fabrica" {
		return fail("the name fabrica is taken: Fabrica's own MCP server is added to every deployment automatically")
	}
	var f connectorFile
	if err := yamlx.DecodeStrict(data, &f); err != nil {
		return fail("%v", err)
	}
	c.Kind, c.Description = f.Kind, strings.TrimSpace(f.Description)
	server := mcpFile{URL: f.URL, Transport: f.Transport, Auth: f.Auth, Secret: f.Secret, Command: f.Command, Args: f.Args, Env: f.Env}
	switch f.Kind {
	case NativeConnector:
		if server.URL != "" || server.Command != "" || server.Transport != "" || server.Auth != "" || server.Secret != "" {
			return fail("a native connector maps to the runtime's built-in integration, so it can't have url, command, transport, auth or secret; put an MCP server under fallback: instead")
		}
		if f.Fallback != nil {
			srv, err := parseMCPServer(*f.Fallback)
			if err != nil {
				return fail("fallback: %v", err)
			}
			c.Fallback = srv
		}
	case MCPConnector:
		if f.Fallback != nil {
			return fail("fallback: only makes sense for native connectors")
		}
		srv, err := parseMCPServer(server)
		if err != nil {
			return fail("%v", err)
		}
		c.Server = srv
	case "":
		return fail("kind is missing; set it to native (the runtime's built-in integration) or mcp")
	default:
		return fail("kind is %q; use native or mcp", f.Kind)
	}
	return c
}

func parseMCPServer(f mcpFile) (*MCPServer, error) {
	s := &MCPServer{URL: f.URL, Transport: f.Transport, Auth: f.Auth, Secret: f.Secret, Command: f.Command, Args: f.Args, Env: f.Env}
	switch {
	case s.URL != "" && s.Command != "":
		return nil, fmt.Errorf("set either url (a remote server) or command (a local one), not both")
	case s.URL == "" && s.Command == "":
		return nil, fmt.Errorf("an MCP server needs a url (remote) or a command (local)")
	}
	if s.Transport == "" {
		s.Transport = "http"
		if s.Command != "" {
			s.Transport = "stdio"
		}
	}
	switch s.Transport {
	case "http", "sse":
		if s.URL == "" {
			return nil, fmt.Errorf("transport %s needs a url", s.Transport)
		}
		u, err := url.Parse(s.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLocalHost(u.Hostname()))) {
			return nil, fmt.Errorf("url %q must be an https URL (plain http is only allowed for localhost)", s.URL)
		}
		if len(s.Args) > 0 || len(s.Env) > 0 {
			return nil, fmt.Errorf("args and env are only for local (stdio) servers")
		}
	case "stdio":
		if s.Command == "" {
			return nil, fmt.Errorf("transport stdio needs a command")
		}
	default:
		return nil, fmt.Errorf("transport is %q; use http, sse or stdio", s.Transport)
	}
	if s.Auth == "" {
		s.Auth = "none"
	}
	switch s.Auth {
	case "none", "oauth":
		if s.Secret != "" {
			return nil, fmt.Errorf("secret only makes sense with auth: bearer")
		}
	case "bearer":
		if s.Secret == "" {
			return nil, fmt.Errorf("auth: bearer needs secret: set to the name of the secret holding the token (like LINEAR_API_KEY); the token itself never goes in a factory")
		}
	default:
		return nil, fmt.Errorf("auth is %q; use none, oauth or bearer", s.Auth)
	}
	if s.Secret != "" && !secretNameRe.MatchString(s.Secret) {
		return nil, fmt.Errorf("secret %q should be a name like LINEAR_API_KEY (uppercase letters, digits and underscores), not the secret itself", s.Secret)
	}
	if s.Transport == "stdio" && s.Auth != "none" {
		return nil, fmt.Errorf("local (stdio) servers take their credentials from env, so auth must be none")
	}
	for _, e := range s.Env {
		if !secretNameRe.MatchString(e) {
			return nil, fmt.Errorf("env lists %q; list the names of environment variables to pass through (like LINEAR_API_KEY), never their values", e)
		}
	}
	return s, nil
}

func isLocalHost(h string) bool { return h == "localhost" || h == "127.0.0.1" || h == "::1" }

// isSymlink reports whether a directory entry is a symlink.
func isSymlink(d fs.DirEntry) bool { return d.Type()&fs.ModeSymlink != 0 }
