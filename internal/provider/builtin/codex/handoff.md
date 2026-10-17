{{- if eq .Action "delete" -}}
# Delete Codex task for `{{.Routine}}` on `{{.Repo}}`

Fabrica no longer deploys this routine here, so its Codex setup should go.

1. Open Codex on the web (chatgpt.com/codex) and go to your scheduled tasks or code review settings, wherever this one lives.
2. Find the task named `{{or .FormerIdentity .Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}. The same name also appears at the top of its prompt.
3. Delete it.
4. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```

Until you do, the old task can still fire, but the Fabrica server refuses its runs.
{{- else -}}
# {{if eq .Action "create"}}Create{{else}}Update{{end}} Codex task for `{{.Routine}}` on `{{.Repo}}`

{{if eq .Action "update"}}This routine's shape changed (its trigger, connectors, memory setting or model), so the Codex task needs updating. Until it is, the Fabrica server refuses its runs.

{{end -}}
1. Open Codex on the web (chatgpt.com/codex){{if eq .Action "update"}} and find the task named `{{or .FormerIdentity .Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}{{else}} and set up a new task{{end}}.
2. Name: `{{.Identity}}`{{if .FormerIdentity}} (it was `{{.FormerIdentity}}`){{end}}
3. Environment / repository: `{{.Repo}}`
4. Trigger: {{.Trigger.Description}}
5. Connectors. Make sure the task can use exactly these:
   - **fabrica** (custom MCP server): URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, header `Authorization: {{.Fabrica.Header}}`{{end}}
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
6. Memory: {{if .Memory.Needed}}this task keeps notes between runs, and Fabrica keeps them for it (they come with `routine_get`), so it doesn't need Codex's memory.{{else}}this task doesn't use memory.{{end}} If Codex's memory is on for this environment, turn it **off** if you can, so notes from other work don't steer this task.
7. Model: {{.Model.Setup}}.
8. Prompt. Replace the whole prompt with this, exactly:

   ```
{{indent 3 .Prompt}}
   ```

9. Save it and tell Fabrica you're done, including the task's ID if Codex shows one:

   ```
   {{.ConfirmCommand}}
   ```

   Without `--remote-id`, Fabrica tracks the task as unverifiable.
{{- end}}
