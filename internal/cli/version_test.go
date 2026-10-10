package cli

import (
	"runtime/debug"
	"testing"
)

func TestBuildVersion(t *testing.T) {
	vcs := func(modified string) []debug.BuildSetting {
		return []debug.BuildSetting{
			{Key: "vcs", Value: "git"},
			{Key: "vcs.revision", Value: "b79bb17c9bcee906094e74f9db924fd0e0a27e56"},
			{Key: "vcs.time", Value: "2026-10-10T12:34:19Z"},
			{Key: "vcs.modified", Value: modified},
		}
	}
	info := func(v string, s []debug.BuildSetting) *debug.BuildInfo {
		return &debug.BuildInfo{Main: debug.Module{Version: v}, Settings: s}
	}
	for _, tc := range []struct {
		name, override   string
		info             *debug.BuildInfo
		wantVer, wantCmt string
	}{
		{"tagged release", "", info("v0.1.0", vcs("false")), "0.1.0", "b79bb17 2026-10-10"},
		{"go install @v0.1.0", "", info("v0.1.0", nil), "0.1.0", ""},
		{"untagged commit", "", info("v0.0.0-20261010123419-b79bb17c9bce", vcs("false")), "dev", "b79bb17 2026-10-10"},
		{"after a tag", "", info("v0.1.1-0.20261010123419-b79bb17c9bce+dirty", vcs("true")), "dev", "b79bb17-dirty 2026-10-10"},
		{"after a pre-release tag", "", info("v0.2.0-rc.1.0.20261010123419-b79bb17c9bce", vcs("false")), "dev", "b79bb17 2026-10-10"},
		{"tagged with changes", "", info("v0.1.0+dirty", vcs("true")), "0.1.0", "b79bb17-dirty 2026-10-10"},
		{"pre-release", "", info("v0.2.0-rc.1", nil), "0.2.0-rc.1", ""},
		{"no git", "", info("(devel)", nil), "dev", ""},
		{"no build info", "", nil, "dev", ""},
		{"ldflags", "v0.1.0", info("(devel)", vcs("false")), "0.1.0", "b79bb17 2026-10-10"},
		{"ldflags dev", "dev", info("v0.1.0", nil), "dev", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ver, commit := buildVersion(tc.override, tc.info)
			if ver != tc.wantVer || commit != tc.wantCmt {
				t.Errorf("buildVersion = %q, %q; want %q, %q", ver, commit, tc.wantVer, tc.wantCmt)
			}
		})
	}
}
