package testkit

import "path"

// AgentProject returns the files of a minimal Agent SDK project, laid out
// under dir (like "agents/pr-reviewer", or "" for a repo's root), for
// testkit's WriteFiles and Repo.Write. pkgName is its package.json name.
func AgentProject(dir, pkgName string) map[string]string {
	at := func(p string) string { return path.Join(dir, p) }
	return map[string]string{
		at("package.json"): `{
  "name": "` + pkgName + `",
  "private": true,
  "type": "module",
  "dependencies": { "@grok/agent-sdk": "^0.6.0", "zod": "^3.25.0" }
}
`,
		at("package-lock.json"): `{ "name": "` + pkgName + `", "lockfileVersion": 3, "packages": {} }
`,
		at("bot/agent.ts"): `import { defineAgent } from "@grok/agent-sdk";

export default defineAgent({
  hosting: { secretNames: ["LINEAR_TOKEN"] },
});
`,
		at("bot/instructions.md"): "# PR reviewer\n\nCall `inspect_pr` before you judge a pull request.\n",
		at("bot/channels/github.ts"): `import { defaultGitHubAuth, githubChannel } from "@grok/agent-sdk/channels/github";

export default githubChannel({
  cursorAccount: true,
  onPullRequest: (ctx, pr) => (pr.action === "opened" ? { auth: defaultGitHubAuth(ctx) } : null),
});
`,
	}
}
