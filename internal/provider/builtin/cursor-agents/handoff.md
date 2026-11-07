{{- if eq .Action "delete" -}}
# Delete Cursor agent `{{.Agent.Name}}` (deployment `{{.Agent.Slug}}`)

Fabrica no longer deploys this agent for factory {{.Factory}}, so its deployment should go.

**This can't be undone.** Deleting the deployment also deletes its hosted sessions, its KV store (`ctx.host.kv`), its files (`ctx.host.files`) and its eval runs. Copy out anything you want to keep first.

1. You need a Cursor team with agent hosting, and the `agent-sdk` command line (`npm install --global @grok/agent-sdk`, then `agent-sdk login`).
2. Delete it:

   ```
   agent-sdk deployment delete {{or .RemoteID .Agent.Slug}}
   ```

3. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```
{{- else if and (eq .Action "update") (not .Agent.Redeploy) -}}
# Update the repos Cursor agent `{{.Agent.Name}}` watches (deployment `{{.Agent.Slug}}`)

Factory {{.Factory}}'s repos changed, so the agent should watch these, and only these:
{{range .Agent.EventRepos}}
- `{{.}}`
{{- end}}

1. You need the `agent-sdk` command line (`npm install --global @grok/agent-sdk`, then `agent-sdk login`).
2. Replace the list (this doesn't redeploy the agent):

   ```
   agent-sdk deployment update {{.Agent.Slug}}{{range .Agent.EventRepos}} --event-repo {{.}}{{end}}
   ```

3. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```
{{- else -}}
# {{if eq .Action "create"}}Deploy{{else}}Redeploy{{end}} Cursor agent `{{.Agent.Name}}` (deployment `{{.Agent.Slug}}`)

{{if eq .Action "update"}}The agent changed in the blueprint, so its deployment needs the new version.{{else}}Factory {{.Factory}} deploys this agent from its blueprint.{{end}} Cursor builds it from git: `{{.Agent.Repo}}` at commit `{{.Agent.Ref}}`{{if .Agent.Path}}, in `{{.Agent.Path}}`{{end}}.

1. You need a Cursor team with agent hosting turned on, Node 22.13 or newer, and the `agent-sdk` command line (`npm install --global @grok/agent-sdk`, then `agent-sdk login`).
2. Check out that commit, and install the agent's dependencies (`agent-sdk deploy` loads the project before it sends Cursor the git ref):

   ```
   git clone {{.Agent.Repo}} agent-checkout
   cd agent-checkout
   git checkout {{.Agent.Ref}}
{{- if .Agent.Path}}
   cd {{.Agent.Path}}
{{- end}}
   npm ci
   ```

   (Use `pnpm install --frozen-lockfile` or `yarn install --frozen-lockfile` instead if the project has that lockfile. In an npm workspace, run it at the workspace's root.)
3. Deploy it, under exactly this slug, so Fabrica can find it:

   ```
   agent-sdk deploy --slug {{.Agent.Slug}} --repo {{.Agent.Repo}} --ref {{.Agent.Ref}}{{if .Agent.Path}} --path {{.Agent.Path}}{{end}}{{range .Agent.EventRepos}} --cursor-events-repo {{.}}{{end}}
   ```
{{- if eq .Action "create"}}

   The first deploy prints the deployment's alias token, once. Save it somewhere safe.
{{- end}}
4. Set each secret the agent declares in `hosting.secretNames` (in its `bot/agent.ts`) that isn't set yet, at the hidden prompt:

   ```
   agent-sdk secret list {{.Agent.Slug}}
   agent-sdk secret set {{.Agent.Slug}} <NAME>
   ```

5. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```
{{- end}}
