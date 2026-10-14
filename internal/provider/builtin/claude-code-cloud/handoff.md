{{- if eq .Action "delete" -}}
# Delete Claude routine for `{{.Routine}}` on `{{.Repo}}`

Fabrica no longer deploys this routine here, so its routine on claude.ai should go.

1. Open your routines on claude.ai (Claude Code on the web → Routines).
2. Find the routine named `{{.Identity}}`{{if .RemoteID}} (its ID is `{{.RemoteID}}`){{end}}. The same name also appears at the top of its prompt.
3. Delete it.
4. Tell Fabrica you're done:

   ```
   {{.ConfirmCommand}}
   ```

Until you do, the old routine can still fire, but the Fabrica server refuses its runs.
{{- else -}}
# {{if eq .Action "create"}}Create{{else}}Update{{end}} Claude routine for `{{.Routine}}` on `{{.Repo}}`

{{if eq .Action "update"}}This routine's shape changed (its trigger, connectors, memory setting, auto-fix setting or model), so the routine on claude.ai needs updating. Until it is, the Fabrica server refuses its runs.

{{end -}}
1. Open your routines on claude.ai (Claude Code on the web → Routines){{if eq .Action "update"}} and find the routine named `{{.Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}{{else}} and start a new routine{{end}}. You can also do this from the Claude Code CLI: run `/schedule`, and give it the settings below.
2. Name: `{{.Identity}}`
3. Repository: `{{.Repo}}`
4. Trigger: {{.Trigger.Description}}
5. Connectors. Make sure the routine has exactly these:
   - **fabrica** (a custom connector): URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, request header `Authorization: {{.Fabrica.Header}}`{{end}}
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
6. Memory: {{if .Memory.Needed}}this routine keeps notes between runs, and Fabrica keeps them for it (they come with `routine_get`), so there's nothing to turn on.{{else}}this routine doesn't use memory.{{end}} If the routine has a memory setting of its own, leave it **off**.
7. Model: {{.Model.Setup}}.
8. Under **Advanced settings**, on the **Behavior** tab:
{{- range .SideEffects}}
   - {{.Setup}}.
{{- end}}
9. Prompt. Replace the whole prompt with this, exactly:

   ```
{{indent 3 .Prompt}}
   ```

10. Save it, copy the routine's ID (it looks like `trig_…`), and tell Fabrica you're done:

    ```
    {{.ConfirmCommand}}
    ```

    If you can't find the ID, run `fabrica confirm {{.ID}}` without `--remote-id`. Fabrica will then track this routine as unverifiable.
{{- end}}
