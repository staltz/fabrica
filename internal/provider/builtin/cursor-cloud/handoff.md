{{- if eq .Action "delete" -}}
# Delete Cursor automation for `{{.Routine}}` on `{{.Repo}}`

Fabrica no longer deploys this routine here, so its Cursor automation should go.

1. Open Cursor's automations (cursor.com → Cloud Agents → Automations).
2. Find the automation named `{{.Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}. The same name also appears at the top of its prompt.
3. Delete it.
4. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```

Until you do, the old automation can still fire, but the Fabrica server refuses its runs.
{{- else -}}
# {{if eq .Action "create"}}Create{{else}}Update{{end}} Cursor automation for `{{.Routine}}` on `{{.Repo}}`

{{if eq .Action "update"}}This routine's shape changed (its trigger, connectors or memory setting), so the Cursor automation needs updating. Until it is, the Fabrica server refuses its runs.

{{end -}}
1. Open Cursor's automations (cursor.com → Cloud Agents → Automations){{if eq .Action "update"}} and find the automation named `{{.Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}{{else}} and create a new automation{{end}}.
2. Name: `{{.Identity}}`
3. Repository: `{{.Repo}}`
4. Trigger: {{.Trigger.Description}}
5. MCP servers and integrations. Make sure the automation has exactly these:
   - **fabrica-{{.Factory}}** (custom MCP server, HTTP): URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, header `Authorization: {{.Fabrica.Header}}`{{end}}. Use exactly this name, the one Fabrica uses when it manages Cursor automations through Cursor's API.
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
6. Memory: {{if .Memory.Needed}}turn the automation's memory tool **on**. This routine keeps notes between runs, in the automation's own memory.{{else}}turn the automation's memory tool **off** (it's on by default). This routine doesn't use memory, so nothing from earlier runs should steer it.{{end}}
7. Prompt. Replace the whole prompt with this, exactly:

   ```
{{indent 3 .Prompt}}
   ```

8. Save it, copy the automation's ID, and tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```

   If you can't find an ID, run `fabrica confirm {{.ID}}` without `--remote-id`. Fabrica will then track this automation as unverifiable.
{{- end}}
