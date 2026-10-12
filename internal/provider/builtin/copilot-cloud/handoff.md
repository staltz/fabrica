{{- if eq .Action "delete" -}}
# Delete Copilot automation for `{{.Routine}}` on `{{.Repo}}`

Fabrica no longer deploys this routine here, so its Copilot automation should go.

1. Open `{{.Repo}}` on GitHub, click **Agents**, then **Automations** in the sidebar.
2. Find the automation named `{{.Identity}}`{{if .RemoteID}} (its ID is `{{.RemoteID}}`){{end}}. The same name also appears at the top of its prompt.
3. Delete it.
4. If no other Fabrica routine runs on Copilot in this repository, also remove the `fabrica` server from the repository's MCP configuration (**Settings → Copilot → MCP servers**) and the `COPILOT_MCP_FABRICA_AUTHORIZATION` secret (**Settings → Secrets and variables → Agents**).
5. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```

Until you do, the old automation can still fire, but the Fabrica server refuses its runs.
{{- else -}}
# {{if eq .Action "create"}}Create{{else}}Update{{end}} Copilot automation for `{{.Routine}}` on `{{.Repo}}`

{{if eq .Action "update"}}This routine's shape changed (its trigger, connectors or memory setting), so the Copilot automation needs updating. Until it is, the Fabrica server refuses its runs.

{{end -}}
1. Open `{{.Repo}}` on GitHub, click **Agents**, then **Automations** in the sidebar{{if eq .Action "update"}}, and find the automation named `{{.Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}{{else}}, and click **Create new**{{end}}. Automations only work in private and internal repositories.
2. Name: `{{.Identity}}`
3. Repository: `{{.Repo}}` (each automation belongs to the repository you create it in).
4. Trigger: {{.Trigger.Description}}{{if eq .Trigger.On "schedule"}}. Copilot schedules recur hourly, daily or weekly, so pick the one closest to this cron, at the same time of day.{{end}}
5. MCP servers. Copilot reads them from the repository, not the automation, so open **Settings → Copilot → MCP servers** for `{{.Repo}}` and make sure Copilot has these (keep any servers the configuration already has):
{{- if .Fabrica.Header}}
   - **fabrica**: first add an Agents secret (**Settings → Secrets and variables → Agents**) named `COPILOT_MCP_FABRICA_AUTHORIZATION`, with the value `{{.Fabrica.Header}}`. Then add this server under `mcpServers`:

     ```json
     "fabrica": {"type": "http", "url": "{{.Fabrica.URL}}", "headers": {"Authorization": "$COPILOT_MCP_FABRICA_AUTHORIZATION"}, "tools": ["*"]}
     ```
{{- else}}
   - **fabrica**: add this server under `mcpServers`:

     ```json
     "fabrica": {"type": "http", "url": "{{.Fabrica.URL}}", "tools": ["*"]}
     ```
{{- end}}
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}

   MCP servers can only read secrets from Agents secrets whose names start with `COPILOT_MCP_`, written as `$COPILOT_MCP_…` in `env` or `headers`. If a `fabrica` server with a different URL is already there, another factory deploys to this repository on Copilot too. A repository has only one MCP configuration, so only one of them can work here.
6. Memory: {{if .Memory.Needed}}this routine keeps notes between runs, and Fabrica keeps them for it (they come with `routine_get`), so it doesn't need Copilot Memory.{{else}}this routine doesn't use memory.{{end}} Copilot Memory is a per-repository setting, so if it's on for this repository, notes from other work may reach this automation too. Turn it off if nothing else here relies on it.
7. Prompt. Replace the whole prompt with this, exactly:

   ```
{{indent 3 .Prompt}}
   ```

8. Tools: allow the MCP servers above, plus the actions this routine takes, like creating a pull request or updating issue labels. Allow nothing more.
9. Save it, copy the automation's ID from its URL, and tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```

   If you can't find an ID, run `fabrica confirm {{.ID}}` without `--remote-id`. Fabrica will then track this automation as unverifiable.
{{- end}}
