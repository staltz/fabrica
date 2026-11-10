package localtools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHeartbeatJob(t *testing.T) {
	job := &HeartbeatJob{Binary: "/opt/fabrica/bin/fabrica", Home: "/Users/me/my factories", Env: map[string]string{"PATH": "/opt/fabrica/bin:/usr/bin", "FABRICA_CACHE": "/tmp/c&d"}}
	for _, tc := range []struct {
		goos  string
		files []string
		want  []string
		load  []string
		drop  []string
	}{
		{
			goos:  "darwin",
			files: []string{"Library/LaunchAgents/dev.fabrica.heartbeat.plist"},
			want: []string{
				"<string>dev.fabrica.heartbeat</string>",
				"<string>/opt/fabrica/bin/fabrica</string>\n    <string>heartbeat</string>\n    <string>--home</string>\n    <string>/Users/me/my factories</string>",
				"<key>StartInterval</key>\n  <integer>60</integer>",
				"<key>AbandonProcessGroup</key>\n  <true/>",
				"<key>FABRICA_CACHE</key>\n    <string>/tmp/c&amp;d</string>",
				"heartbeat.log</string>",
			},
			load: []string{"launchctl bootstrap gui/"},
			drop: []string{"launchctl bootout gui/"},
		},
		{
			goos:  "linux",
			files: []string{".config/systemd/user/fabrica-heartbeat.service", ".config/systemd/user/fabrica-heartbeat.timer"},
			want: []string{
				`ExecStart=/opt/fabrica/bin/fabrica heartbeat --home "/Users/me/my factories"`,
				"KillMode=process",
				`Environment=PATH=/opt/fabrica/bin:/usr/bin`,
				"OnCalendar=minutely",
				"WantedBy=timers.target",
			},
			load: []string{"systemctl --user daemon-reload", "systemctl --user enable --now fabrica-heartbeat.timer"},
			drop: []string{"systemctl --user disable --now fabrica-heartbeat.timer", "systemctl --user daemon-reload"},
		},
	} {
		t.Run(tc.goos, func(t *testing.T) {
			home := t.TempDir()
			s, err := SchedulerFor(tc.goos, home, filepath.Join(home, "cache"))
			if err != nil {
				t.Fatal(err)
			}
			var ran []string
			s.Run = func(name string, args ...string) error {
				ran = append(ran, name+" "+strings.Join(args, " "))
				return nil
			}
			if got, _ := s.Read(); len(got) != 0 {
				t.Fatalf("nothing is installed yet: %v", got)
			}

			// Installing writes the files and loads them.
			if err := s.Apply(job); err != nil {
				t.Fatal(err)
			}
			var all strings.Builder
			for _, f := range tc.files {
				data, err := os.ReadFile(filepath.Join(home, f))
				if err != nil {
					t.Fatal(err)
				}
				all.Write(data)
			}
			for _, want := range tc.want {
				if !strings.Contains(all.String(), want) {
					t.Errorf("the job is missing %q:\n%s", want, all.String())
				}
			}
			for _, want := range tc.load {
				if !strings.Contains(strings.Join(ran, "\n"), want) {
					t.Errorf("didn't run %q: %v", want, ran)
				}
			}
			if got, _ := s.Read(); !SameFiles(got, s.Render(job)) {
				t.Error("Read should find what Apply wrote")
			}

			// Applying the same job again changes nothing.
			ran = nil
			if err := s.Apply(job); err != nil || len(ran) != 0 {
				t.Errorf("an unchanged job: %v, ran %v", err, ran)
			}

			// Removing it unloads it and deletes the files.
			if err := s.Apply(nil); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.drop {
				if !strings.Contains(strings.Join(ran, "\n"), want) {
					t.Errorf("didn't run %q: %v", want, ran)
				}
			}
			if got, _ := s.Read(); len(got) != 0 {
				t.Errorf("files left behind: %v", got)
			}
		})
	}
	if _, err := SchedulerFor("windows", t.TempDir(), t.TempDir()); err == nil {
		t.Error("Fabrica has no scheduler for Windows yet")
	}
}

func TestHeartbeatEnv(t *testing.T) {
	t.Setenv("FABRICA_CACHE", "/tmp/fabrica-cache")
	env := HeartbeatEnv("/opt/fabrica/bin/fabrica")
	if !strings.HasPrefix(env["PATH"], "/opt/fabrica/bin"+string(os.PathListSeparator)) || !strings.Contains(env["PATH"], "/usr/bin") {
		t.Errorf("PATH = %q", env["PATH"])
	}
	if env["FABRICA_CACHE"] != "/tmp/fabrica-cache" {
		t.Errorf("env = %v", env)
	}
}
