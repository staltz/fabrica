{{- if eq .Action "delete" -}}
# Delete the Acme Runner job for `{{.Routine}}` on `{{.Repo}}`

1. Open Acme Runner and find the job named `{{.Identity}}`{{if .RemoteID}} (ID `{{.RemoteID}}`){{end}}.
2. Delete it.
3. Run:

   ```
   {{.ConfirmCommand}}
   ```
{{- else -}}
# {{if eq .Action "create"}}Create{{else}}Update{{end}} the Acme Runner job for `{{.Routine}}` on `{{.Repo}}`

1. Open Acme Runner and {{if eq .Action "create"}}create a new job{{else}}open the job named `{{.Identity}}`{{end}}.
2. Name: `{{.Identity}}`
3. Trigger: {{.Trigger.Description}}
4. MCP servers:
   - **fabrica**: URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, header `Authorization: {{.Fabrica.Header}}`{{end}}
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
5. Prompt, exactly:

   ```
{{indent 3 .Prompt}}
   ```

6. Save it, then run:

   ```
   {{.ConfirmCommand}}
   ```
{{- end}}
