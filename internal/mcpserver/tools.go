package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/staltz/fabrica/internal/blueprint"
	"github.com/staltz/fabrica/internal/provider"
	"github.com/staltz/fabrica/internal/repourl"
	"github.com/staltz/fabrica/internal/runs"
	"github.com/staltz/fabrica/internal/stub"
)

type noArgs struct{}

type nameArgs struct {
	Name string `json:"name" jsonschema:"the name, as listed"`
}

type skillFileArgs struct {
	Name string `json:"name" jsonschema:"the skill's name"`
	Path string `json:"path" jsonschema:"the file's path inside the skill's folder, as listed by skill_load"`
}

type runBeginArgs struct {
	Intent  string `json:"intent" jsonschema:"a summary of the original request in at most 20 words of casual plain English"`
	Routine string `json:"routine,omitempty" jsonschema:"for a routine run, the routine's name from your stub"`
	Runtime string `json:"runtime,omitempty" jsonschema:"for a routine run, the runtime from your stub"`
	Repo    string `json:"repo,omitempty" jsonschema:"for a routine run, the repo from your stub, like github.com/acme/app"`
	Stub    string `json:"stub,omitempty" jsonschema:"for a routine run, the stub fingerprint from your stub"`
}

type runReportArgs struct {
	RunID   string   `json:"run_id" jsonschema:"the run_id from run_begin"`
	Summary string   `json:"summary" jsonschema:"what happened, in at most 20 words of casual plain English"`
	Status  string   `json:"status,omitempty" jsonschema:"progress (the default), succeeded or failed"`
	Links   []string `json:"links,omitempty" jsonschema:"URLs of PRs, issues or commits this checkpoint is about"`
}

type memorySaveArgs struct {
	RunID   string `json:"run_id" jsonschema:"the run_id run_begin gave you for this routine run"`
	Content string `json:"content" jsonschema:"the routine's whole memory, as Markdown notes for its future runs; it replaces what was there, and empty forgets everything"`
}

type memorySaveOut struct {
	Saved bool   `json:"saved"`
	Bytes int    `json:"bytes"`
	Note  string `json:"note"`
}

type skillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Available   bool   `json:"available"`
	Reason      string `json:"unavailable_reason,omitempty"`
}

type expectedInfo struct {
	Name     string `json:"name"`
	Optional bool   `json:"optional"`
	// ProvidedByRepo is known locally, where Fabrica can see the workspace.
	ProvidedByRepo *bool  `json:"provided_by_repo,omitempty"`
	Note           string `json:"note"`
}

type skillsListOut struct {
	Skills   []skillInfo    `json:"skills"`
	Expected []expectedInfo `json:"expected_from_repo,omitempty"`
	HowTo    string         `json:"how_to_use"`
}

type runBeginOut struct {
	RunID string `json:"run_id"`
	Next  string `json:"next"`
}

type runReportOut struct {
	Recorded bool   `json:"recorded"`
	RunID    string `json:"run_id"`
	Status   string `json:"status"`
}

func (s *session) skillsList(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, skillsListOut, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, skillsListOut{}, err
	}
	out := skillsListOut{HowTo: "Load the skills that are relevant to your task with skill_load. "}
	if snap.remote() {
		out.HowTo += "Read a skill's other files with skill_file; relative paths in a skill resolve against its folder."
	} else {
		out.HowTo += "skill_load unpacks the skill's folder on disk and tells you where; relative paths in the skill resolve against that folder, so run its scripts from there."
	}
	for _, name := range snap.Blueprint.SkillNames() {
		sk := snap.Blueprint.Skills[name]
		info := skillInfo{Name: name, Description: sk.Description, Available: true}
		if snap.remote() && !sk.TextOnly {
			info.Available, info.Reason = false, "this skill needs its folder on disk ("+sk.NotTextOnlyReason+"), which cloud runtimes can't have in v0.1"
		}
		out.Skills = append(out.Skills, info)
	}
	for _, e := range snap.Blueprint.Manifest.Expects {
		ex := expectedInfo{Name: e.Name, Optional: e.Optional}
		_, fallback := snap.Blueprint.Skills[e.Name]
		if snap.Workspace != nil {
			_, provided := snap.Workspace.Skills[e.Name]
			ex.ProvidedByRepo = &provided
			switch {
			case provided:
				ex.Note = "the repo provides its own " + e.Name + " skill; use that one (your tool loads it from the repo)"
			case fallback:
				ex.Note = "the repo doesn't provide it, so use the blueprint's default with skill_load"
			default:
				ex.Note = "the repo doesn't provide it, and there's no default"
			}
		} else {
			ex.Note = "if the repo has its own " + e.Name + " skill, use that one; otherwise use the blueprint's default, if there is one"
		}
		out.Expected = append(out.Expected, ex)
	}
	return nil, out, nil
}

func (s *session) skillLoad(ctx context.Context, req *mcp.CallToolRequest, in nameArgs) (*mcp.CallToolResult, any, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, nil, err
	}
	if err := clashError(snap); err != nil {
		return nil, nil, err
	}
	sk := snap.Blueprint.Skills[in.Name]
	if sk == nil {
		return nil, nil, fmt.Errorf("there's no skill called %q; skills_list shows the ones there are", in.Name)
	}
	if snap.remote() && !sk.TextOnly {
		return nil, nil, fmt.Errorf("skill %s needs its whole folder on disk (%s), and cloud runtimes can't have that in v0.1, so it isn't available here", in.Name, sk.NotTextOnlyReason)
	}
	folder := ""
	if !snap.remote() {
		var err error
		if folder, err = unpackSkill(snap.SkillCache, sk); err != nil {
			return nil, nil, fmt.Errorf("couldn't unpack skill %s: %v", in.Name, err)
		}
	}
	s.recordSkillLoad(ctx, in.Name)
	return textResult(skillText(sk, folder)), nil, nil
}

func (s *session) skillFile(ctx context.Context, req *mcp.CallToolRequest, in skillFileArgs) (*mcp.CallToolResult, any, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, nil, err
	}
	if err := clashError(snap); err != nil {
		return nil, nil, err
	}
	sk := snap.Blueprint.Skills[in.Name]
	if sk == nil {
		return nil, nil, fmt.Errorf("there's no skill called %q", in.Name)
	}
	if snap.remote() && !sk.TextOnly {
		return nil, nil, fmt.Errorf("skill %s isn't available on cloud runtimes in v0.1 (%s)", in.Name, sk.NotTextOnlyReason)
	}
	p := strings.TrimPrefix(filepath.ToSlash(filepath.Clean("/"+in.Path)), "/")
	var found *blueprint.SkillFile
	for i := range sk.Files {
		if sk.Files[i].Path == p {
			found = &sk.Files[i]
		}
	}
	if found == nil {
		return nil, nil, fmt.Errorf("skill %s has no file %q; skill_load lists its files", in.Name, in.Path)
	}
	data, err := os.ReadFile(filepath.Join(sk.Dir, filepath.FromSlash(found.Path)))
	if err != nil {
		return nil, nil, err
	}
	if !utf8.Valid(data) || strings.ContainsRune(string(data), 0) {
		where := "it can't be read over MCP"
		if !snap.remote() {
			where = "use it from the folder skill_load gave you"
		}
		return nil, nil, fmt.Errorf("%s isn't a text file, so %s", found.Path, where)
	}
	return textResult(string(data)), nil, nil
}

func (s *session) rulesGet(ctx context.Context, req *mcp.CallToolRequest, _ noArgs) (*mcp.CallToolResult, any, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, nil, err
	}
	return textResult(rulesText(snap.Blueprint)), nil, nil
}

func (s *session) ruleLoad(ctx context.Context, req *mcp.CallToolRequest, in nameArgs) (*mcp.CallToolResult, any, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, nil, err
	}
	r := snap.Blueprint.Rules[in.Name]
	if r == nil {
		return nil, nil, fmt.Errorf("there's no rule called %q; rules_get lists them", in.Name)
	}
	return textResult(ruleText(r)), nil, nil
}

func (s *session) routineGet(ctx context.Context, req *mcp.CallToolRequest, in nameArgs) (*mcp.CallToolResult, any, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, nil, err
	}
	r := snap.Blueprint.Routines[in.Name]
	if r == nil {
		return nil, nil, fmt.Errorf("there's no routine called %q", in.Name)
	}
	s.mu.Lock()
	reason, refused := s.refused[in.Name]
	run := s.began[in.Name]
	s.mu.Unlock()
	if refused {
		return nil, nil, fmt.Errorf("Fabrica refused to start routine %s in this session (%s), so it won't serve it", in.Name, reason)
	}
	if snap.remote() && run == nil {
		return nil, nil, fmt.Errorf("on the Fabrica server, routines only run from stubs: call run_begin with routine, runtime, repo and stub (exactly as your stub says) before routine_get")
	}
	// Locally, you can also run any routine by hand, without a stub; then
	// the workspace's repo and the tool's runtime apply.
	repo, runtime := snap.Repo, snap.Runtime
	if run != nil {
		repo, runtime = run.Repo, run.Runtime
	}
	var mem *runs.Memory
	if r.Memory && run != nil && memorySupport(snap.Blueprint, runtime) == provider.Emulated {
		m, err := s.core.Runs.Memory(ctx, run.Factory, r.Ref.Name, run.Repo)
		if err != nil {
			return nil, nil, fmt.Errorf("couldn't read routine %s's memory: %v", r.Ref.Name, err)
		}
		mem = &m
	}
	return textResult(routineText(snap, r, run, repo, runtime, mem)), nil, nil
}

// memorySupport says how a runtime gives routines their memory.
func memorySupport(f *blueprint.Blueprint, runtime string) provider.Support {
	if p, ok := runtimeProvider(f, runtime); ok {
		return p.Matrix.Memory
	}
	return provider.Unsupported
}

func (s *session) memorySave(ctx context.Context, req *mcp.CallToolRequest, in memorySaveArgs) (*mcp.CallToolResult, memorySaveOut, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := refusal(snap); err != nil {
		return nil, memorySaveOut{}, err
	}
	// Only a routine run begun in this session can write its routine's
	// memory, so one run can't overwrite another routine's notes.
	var run *runs.Run
	s.mu.Lock()
	for _, r := range s.began {
		if r.ID == in.RunID {
			run = r
		}
	}
	s.mu.Unlock()
	if run == nil {
		return nil, memorySaveOut{}, fmt.Errorf("run %q isn't a routine run begun in this session; memory_save needs the run_id run_begin gave you for your routine", in.RunID)
	}
	r := snap.Blueprint.Routines[run.Routine]
	if r == nil || !r.Memory {
		return nil, memorySaveOut{}, fmt.Errorf("routine %s doesn't use memory (its routine file has no `memory: true`), so it shouldn't keep notes between runs", run.Routine)
	}
	switch memorySupport(snap.Blueprint, run.Runtime) {
	case provider.Emulated:
	case provider.Native:
		return nil, memorySaveOut{}, fmt.Errorf("runtime %s has its own memory feature, and routine %s keeps its notes there; use that instead of memory_save", run.Runtime, run.Routine)
	default:
		return nil, memorySaveOut{}, fmt.Errorf("runtime %s can't keep memory for routines", run.Runtime)
	}
	content := strings.TrimSpace(in.Content)
	if !utf8.ValidString(content) {
		return nil, memorySaveOut{}, errors.New("the memory must be UTF-8 text")
	}
	if err := s.core.Runs.SaveMemory(ctx, run.Factory, run.Routine, run.Repo, content, run.ID, s.core.now()); err != nil {
		return nil, memorySaveOut{}, fmt.Errorf("couldn't save the memory: %v", err)
	}
	note := "Saved. The next run of " + run.Routine + " on " + run.Repo + " gets it with routine_get."
	if content == "" {
		note = "Forgotten. The next run of " + run.Routine + " on " + run.Repo + " starts with an empty memory."
	}
	return nil, memorySaveOut{Saved: true, Bytes: len(content), Note: note}, nil
}

func (s *session) runBegin(ctx context.Context, req *mcp.CallToolRequest, in runBeginArgs) (*mcp.CallToolResult, runBeginOut, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := runs.CheckWords("intent", in.Intent); err != nil {
		return nil, runBeginOut{}, err // not recorded: the agent should just try again
	}
	now := s.core.now()
	run := runs.Run{
		ID: runs.NewID(now), StartedAt: now, Intent: strings.Join(strings.Fields(in.Intent), " "),
		Routine: in.Routine, Stub: in.Stub, ServedBy: snap.Mode,
		Factory: snap.factoryName(), BlueprintSHA: snap.BlueprintSHA(),
	}
	refuse := func(err error) (*mcp.CallToolResult, runBeginOut, error) {
		return nil, runBeginOut{}, s.recordRefusal(ctx, run, err)
	}

	// Who and where. Locally Fabrica knows both; remotely they come from
	// the stub.
	if snap.remote() {
		run.Runtime = in.Runtime
		if in.Repo != "" {
			repo, err := repourl.Normalize(in.Repo)
			if err != nil {
				return refuse(fmt.Errorf("repo: %v", err))
			}
			run.Repo = repo
		}
	} else {
		run.Runtime, run.Repo = snap.Runtime, snap.Repo
		if in.Runtime != "" && in.Runtime != snap.Runtime {
			return refuse(fmt.Errorf("you said runtime %s, but this Fabrica server serves %s", in.Runtime, snap.Runtime))
		}
		if in.Repo != "" {
			if repo, err := repourl.Normalize(in.Repo); err != nil || repo != snap.Repo {
				return refuse(fmt.Errorf("you said repo %s, but this workspace is %s", in.Repo, snap.Repo))
			}
		}
	}
	if err := refusal(snap); err != nil {
		return refuse(err)
	}
	if err := clashError(snap); err != nil {
		return refuse(err)
	}

	if in.Routine != "" {
		if snap.remote() && (in.Runtime == "" || in.Repo == "" || in.Stub == "") {
			return refuse(errors.New("on the Fabrica server, routines only run from stubs, so run_begin needs routine, runtime, repo and stub, exactly as your stub says"))
		}
		r := snap.Blueprint.Routines[in.Routine]
		if r == nil {
			return refuse(fmt.Errorf("there's no routine called %q in this blueprint", in.Routine))
		}
		if in.Stub != "" {
			if err := s.checkStub(ctx, snap, r, run); err != nil {
				return refuse(err)
			}
		}
	}

	if err := s.core.Runs.Begin(ctx, run); err != nil {
		return nil, runBeginOut{}, fmt.Errorf("couldn't record the run: %v", err)
	}
	s.mu.Lock()
	s.run = &run
	if in.Routine != "" {
		s.began[in.Routine] = &run
		delete(s.refused, in.Routine)
	}
	s.mu.Unlock()

	next := "Call rules_get and follow the rules, then skills_list and skill_load for the skills your task needs. Record progress with run_report."
	if in.Routine != "" {
		next = fmt.Sprintf("Call routine_get with name %q and follow it. Record progress with run_report.", in.Routine)
	}
	return nil, runBeginOut{RunID: run.ID, Next: next}, nil
}

// checkStub runs the two stale-stub checks, in order: the stub is still
// deployed, and its shape still matches. Both compare the latest home repo
// with the values inside the stub, never with state.
func (s *session) checkStub(ctx context.Context, snap *Snapshot, r *blueprint.Routine, run runs.Run) error {
	d, why := s.core.Backend.LatestFactory(ctx, snap, run.Repo, run.Runtime)
	switch {
	case d == nil:
		return fmt.Errorf("this stub is no longer deployed: %s. The factory needs `fabrica apply` to remove the stub", why)
	case !d.HasRoutine(r.Ref.Name):
		return fmt.Errorf("this stub is no longer deployed: routine %s isn't in factory %s anymore. The factory needs `fabrica apply` to remove the stub", r.Ref.Name, d.Name)
	case !d.HasRuntime(run.Runtime):
		return fmt.Errorf("this stub is no longer deployed: runtime %s isn't in factory %s anymore. The factory needs `fabrica apply` to remove the stub", run.Runtime, d.Name)
	case !d.Runs(r.Ref.Name, run.Runtime):
		return fmt.Errorf("this stub is no longer deployed: routine %s doesn't run on runtime %s in factory %s anymore. The factory needs `fabrica apply` to remove the stub", r.Ref.Name, run.Runtime, d.Name)
	case !d.Covers(run.Repo) && !(snap.Mode == runs.ServedLocal && d.IsWildcard()):
		return fmt.Errorf("this stub is no longer deployed: repo %s isn't in factory %s anymore. The factory needs `fabrica apply` to remove the stub", run.Repo, d.Name)
	}
	p, ok := runtimeProvider(snap.Blueprint, run.Runtime)
	if !ok {
		return fmt.Errorf("this stub is outdated: the blueprint doesn't know runtime %s. The factory needs `fabrica apply`", run.Runtime)
	}
	endpoint := ""
	if snap.remote() {
		endpoint = d.Endpoint()
	}
	ev := stub.Evaluate(snap.Blueprint, r, stub.Target{Factory: d.Name, Runtime: p, Endpoint: endpoint})
	if fp := ev.Shape.Fingerprint(); fp != run.Stub || !ev.OK() {
		return fmt.Errorf("this stub is outdated: routine %s's shape (its trigger, connectors or memory setting) changed, so the stub (%s) doesn't match the blueprint (%s) anymore. The factory needs `fabrica apply`", r.Ref.Name, run.Stub, fp)
	}
	return nil
}

// recordRefusal records a refused run as failed, and remembers the routine
// so routine_get won't serve it in this session. It returns the error to
// give the agent.
func (s *session) recordRefusal(ctx context.Context, run runs.Run, cause error) error {
	summary := "Fabrica refused to start this run; see the error it returned"
	switch {
	case strings.Contains(cause.Error(), "no longer deployed"):
		summary = "Refused: stub is no longer deployed; the factory needs fabrica apply"
	case strings.Contains(cause.Error(), "outdated"):
		summary = "Refused: stub is outdated; the factory needs fabrica apply"
	case strings.Contains(cause.Error(), "Fabrica line"):
		summary = "Refused: the repo's AGENTS.md is missing the Fabrica line"
	case strings.Contains(cause.Error(), "same names as blueprint skills"):
		summary = "Refused: repo skills clash with blueprint skills"
	}
	if err := s.core.Runs.Begin(ctx, run); err == nil {
		_ = s.core.Runs.AddCheckpoint(ctx, run.ID, runs.Checkpoint{At: s.core.now(), Author: runs.ByFabrica, Status: runs.Failed, Summary: summary})
	}
	if run.Routine != "" {
		s.mu.Lock()
		s.refused[run.Routine] = cause.Error()
		delete(s.began, run.Routine)
		s.mu.Unlock()
	}
	return fmt.Errorf("%v (recorded as failed run %s; tell the user)", cause, run.ID)
}

func (s *session) runReport(ctx context.Context, req *mcp.CallToolRequest, in runReportArgs) (*mcp.CallToolResult, runReportOut, error) {
	snap := s.snapshot(ctx, req.Session)
	if err := runs.CheckWords("summary", in.Summary); err != nil {
		return nil, runReportOut{}, err
	}
	status, err := runs.CheckStatus(in.Status)
	if err != nil {
		return nil, runReportOut{}, err
	}
	if err := runs.CheckLinks(in.Links); err != nil {
		return nil, runReportOut{}, err
	}
	run, err := s.core.Runs.Get(ctx, in.RunID)
	if errors.Is(err, runs.ErrNoRun) {
		return nil, runReportOut{}, fmt.Errorf("there's no run %q; use the run_id run_begin gave you", in.RunID)
	}
	if err != nil {
		return nil, runReportOut{}, err
	}
	if snap.remote() && run.Factory != snap.factoryName() {
		return nil, runReportOut{}, fmt.Errorf("run %s doesn't belong to this factory", in.RunID)
	}
	if err := s.core.Runs.AddCheckpoint(ctx, run.ID, runs.Checkpoint{
		At: s.core.now(), Author: runs.ByAgent, Status: status,
		Summary: strings.Join(strings.Fields(in.Summary), " "), Links: in.Links,
	}); err != nil {
		return nil, runReportOut{}, fmt.Errorf("couldn't record the checkpoint: %v", err)
	}
	return nil, runReportOut{Recorded: true, RunID: run.ID, Status: status}, nil
}

func (s *session) recordSkillLoad(ctx context.Context, skill string) {
	s.mu.Lock()
	run := s.run
	s.mu.Unlock()
	if run != nil {
		_ = s.core.Runs.SkillLoaded(ctx, run.ID, skill, s.core.now())
	}
}
