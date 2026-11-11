package localtools

import (
	"bytes"
	"errors"
	"fmt"
	"html"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// The heartbeat job.
//
// Local factories have no server running all the time to keep their
// pace, so when one of them runs a routine with a pace trigger, `fabrica
// apply` installs a job in your system's per-user scheduler that runs
// `fabrica heartbeat` every minute: a launchd agent on macOS, a systemd
// user timer on Linux. It's one job for the whole machine, whatever the
// factories, and Fabrica owns its files outright, so it writes them whole.

// HeartbeatJob is what the job runs.
type HeartbeatJob struct {
	// Binary is the fabrica command, and Home your personal home repo.
	Binary string
	Home   string
	// Env is the job's environment. Schedulers start jobs with a bare one,
	// so it has the PATH that finds git and the tools' commands.
	Env map[string]string
}

// Scheduler kinds.
const (
	Launchd = "launchd"
	Systemd = "systemd"
)

const (
	launchdLabel = "dev.fabrica.heartbeat"
	systemdUnit  = "fabrica-heartbeat"
)

// Scheduler installs the heartbeat job in one system's per-user scheduler.
type Scheduler struct {
	Kind string
	home string
	// logFile is where a launchd job's output goes (systemd keeps it in
	// the journal).
	logFile string
	// Run runs one of the scheduler's commands (launchctl, systemctl).
	// Tests replace it.
	Run func(name string, args ...string) error
}

// SchedulerFor returns the scheduler of an operating system (a GOOS), for
// the user whose home folder is userHome. cacheDir is Fabrica's cache,
// where a launchd job logs. It fails on systems Fabrica can't install a
// job on.
func SchedulerFor(goos, userHome, cacheDir string) (*Scheduler, error) {
	s := &Scheduler{home: userHome, logFile: filepath.Join(cacheDir, "heartbeat.log"), Run: runQuietly}
	switch goos {
	case "darwin":
		s.Kind = Launchd
	case "linux":
		s.Kind = Systemd
	default:
		return nil, fmt.Errorf("Fabrica can't install a job on %s yet", goos)
	}
	return s, nil
}

// Files are the job's files.
func (s *Scheduler) Files() []string {
	if s.Kind == Launchd {
		return []string{filepath.Join(s.home, "Library", "LaunchAgents", launchdLabel+".plist")}
	}
	dir := filepath.Join(s.home, ".config", "systemd", "user")
	return []string{filepath.Join(dir, systemdUnit+".service"), filepath.Join(dir, systemdUnit+".timer")}
}

// Describe says what the job is, like "a launchd agent
// (~/Library/LaunchAgents/dev.fabrica.heartbeat.plist)".
func (s *Scheduler) Describe() string {
	if s.Kind == Launchd {
		return "a launchd agent, " + launchdLabel
	}
	return "a systemd user timer, " + systemdUnit + ".timer"
}

// Read returns the job's files as they are now, by path. Missing files
// aren't in it, so no job is an empty map.
func (s *Scheduler) Read() (map[string][]byte, error) {
	out := map[string][]byte{}
	for _, f := range s.Files() {
		data, err := os.ReadFile(f)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[f] = data
	}
	return out, nil
}

// Render returns the job's files for a job, by path, or none for nil.
func (s *Scheduler) Render(job *HeartbeatJob) map[string][]byte {
	out := map[string][]byte{}
	if job == nil {
		return out
	}
	files := s.Files()
	if s.Kind == Launchd {
		out[files[0]] = launchdPlist(job, s.logFile)
		return out
	}
	out[files[0]], out[files[1]] = systemdService(job), systemdTimer()
	return out
}

// Apply installs, updates or removes the job (for a nil job), and has the
// scheduler pick the change up.
func (s *Scheduler) Apply(job *HeartbeatJob) error {
	want := s.Render(job)
	have, err := s.Read()
	if err != nil {
		return err
	}
	if SameFiles(have, want) {
		return nil
	}
	if s.Kind == Launchd {
		return s.applyLaunchd(want, len(have) > 0)
	}
	return s.applySystemd(want, len(have) > 0)
}

func (s *Scheduler) applyLaunchd(want map[string][]byte, installed bool) error {
	file := s.Files()[0]
	domain := "gui/" + strconv.Itoa(os.Getuid())
	if installed {
		// Unload the old job first; it may not be loaded, which is fine.
		_ = s.Run("launchctl", "bootout", domain+"/"+launchdLabel)
	}
	if len(want) == 0 {
		return removeFile(file)
	}
	if err := os.MkdirAll(filepath.Dir(s.logFile), 0o700); err != nil {
		return err
	}
	if err := writeWhole(file, want[file]); err != nil {
		return err
	}
	if err := s.Run("launchctl", "bootstrap", domain, file); err != nil {
		return fmt.Errorf("wrote %s, but launchd didn't load it (it will at your next login): %w", file, err)
	}
	return nil
}

func (s *Scheduler) applySystemd(want map[string][]byte, installed bool) error {
	timer := systemdUnit + ".timer"
	if len(want) == 0 {
		if installed {
			_ = s.Run("systemctl", "--user", "disable", "--now", timer)
		}
		for _, f := range s.Files() {
			if err := removeFile(f); err != nil {
				return err
			}
		}
		_ = s.Run("systemctl", "--user", "daemon-reload")
		return nil
	}
	for _, f := range s.Files() {
		if err := writeWhole(f, want[f]); err != nil {
			return err
		}
	}
	if err := s.Run("systemctl", "--user", "daemon-reload"); err != nil {
		return fmt.Errorf("wrote the heartbeat's units, but systemd didn't reload them: %w", err)
	}
	if err := s.Run("systemctl", "--user", "enable", "--now", timer); err != nil {
		return fmt.Errorf("wrote the heartbeat's units, but systemd didn't start %s: %w", timer, err)
	}
	return nil
}

func launchdPlist(job *HeartbeatJob, logFile string) []byte {
	var b strings.Builder
	str := func(s string) string { return "<string>" + html.EscapeString(s) + "</string>" }
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<!-- Written by Fabrica (fabrica apply). Don't edit by hand: it runs the
     routines with a pace trigger of your local factories. -->
<plist version="1.0">
<dict>
  <key>Label</key>
  ` + str(launchdLabel) + `
  <key>ProgramArguments</key>
  <array>
    ` + str(job.Binary) + `
    ` + str("heartbeat") + `
    ` + str("--home") + `
    ` + str(job.Home) + `
  </array>
  <key>StartInterval</key>
  <integer>60</integer>
  <key>RunAtLoad</key>
  <true/>
  <!-- The sessions it starts outlive it. -->
  <key>AbandonProcessGroup</key>
  <true/>
  <key>StandardOutPath</key>
  ` + str(logFile) + `
  <key>StandardErrorPath</key>
  ` + str(logFile) + `
`)
	if len(job.Env) > 0 {
		b.WriteString("  <key>EnvironmentVariables</key>\n  <dict>\n")
		for _, k := range sortedKeys(job.Env) {
			fmt.Fprintf(&b, "    <key>%s</key>\n    %s\n", html.EscapeString(k), str(job.Env[k]))
		}
		b.WriteString("  </dict>\n")
	}
	b.WriteString("</dict>\n</plist>\n")
	return []byte(b.String())
}

func systemdService(job *HeartbeatJob) []byte {
	var b strings.Builder
	b.WriteString(`# Written by Fabrica (fabrica apply). Don't edit by hand: it runs the
# routines with a pace trigger of your local factories.
[Unit]
Description=Fabrica heartbeat: start the local routines whose pace says they're due

[Service]
Type=oneshot
ExecStart=` + systemdQuote(job.Binary) + ` heartbeat --home ` + systemdQuote(job.Home) + `
# The sessions it starts outlive it.
KillMode=process
`)
	for _, k := range sortedKeys(job.Env) {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote(k+"="+job.Env[k]))
	}
	return []byte(b.String())
}

func systemdTimer() []byte {
	return []byte(`# Written by Fabrica (fabrica apply). Don't edit by hand.
[Unit]
Description=Run the Fabrica heartbeat every minute

[Timer]
OnCalendar=minutely
AccuracySec=1s

[Install]
WantedBy=timers.target
`)
}

// systemdQuote quotes a word for a unit file, when it needs it.
func systemdQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"'\\$%;") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "$$", "%", "%%")
	return `"` + r.Replace(s) + `"`
}

// HeartbeatEnv is the environment the job runs in: a PATH that finds the
// fabrica command, git and Claude Code's command line (which starts the
// sessions), plus Fabrica's own settings that are set now.
func HeartbeatEnv(binary string) map[string]string {
	dirs := []string{}
	add := func(dir string) {
		if dir != "" && dir != "." && !slices.Contains(dirs, dir) {
			dirs = append(dirs, dir)
		}
	}
	if filepath.IsAbs(binary) {
		add(filepath.Dir(binary))
	}
	claude := os.Getenv("FABRICA_CLAUDE_BIN")
	if claude == "" {
		claude = "claude"
	}
	for _, cmd := range []string{claude, "git"} {
		if p, err := exec.LookPath(cmd); err == nil {
			if abs, err := filepath.Abs(p); err == nil {
				add(filepath.Dir(abs))
			}
		}
	}
	for _, dir := range []string{"/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"} {
		add(dir)
	}
	env := map[string]string{"PATH": strings.Join(dirs, string(os.PathListSeparator))}
	for _, k := range []string{"FABRICA_CACHE", "FABRICA_CLAUDE_BIN"} {
		if v := os.Getenv(k); v != "" {
			env[k] = v
		}
	}
	return env
}

// writeWhole writes a file Fabrica owns, atomically.
func writeWhole(file string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(file), "."+filepath.Base(file)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), file)
}

func removeFile(file string) error {
	if err := os.Remove(file); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// SameFiles reports whether two sets of job files, by path, are the same.
func SameFiles(a, b map[string][]byte) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if w, ok := b[k]; !ok || !bytes.Equal(v, w) {
			return false
		}
	}
	return true
}

// runQuietly runs a command, and returns its output in the error when it
// fails.
func runQuietly(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}
