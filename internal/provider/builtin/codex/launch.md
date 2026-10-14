# Start the init session of factory `{{.Factory}}` in Codex

Factory `{{.Factory}}` was just applied for the first time, so its blueprint's init routine, `{{.Routine}}`, should run once, now. Start it as a one-off Codex cloud task:

1. Open Codex on the web (chatgpt.com/codex) and pick the environment for repository `{{.Repo}}`.{{if gt (len .Repos) 1}} The factory also deploys to {{range $i, $r := .Repos}}{{if $i}}{{if gt $i 1}}, {{end}}`{{$r}}`{{end}}{{end}}; the task can reach those through its connectors.{{end}}
2. Connectors. Make sure the task can use exactly these:
   - **fabrica** (custom MCP server): URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, header `Authorization: {{.Fabrica.Header}}`{{end}}
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
3. Model: {{.Model.Setup}}.
4. Start a task with this prompt, exactly:

   ```
{{indent 3 .Prompt}}
   ```

5. Once it's running, tell Fabrica, with the task's ID if Codex shows one:

   ```
   {{.ConfirmCommand}}
   ```

   Until you do, `fabrica plan` lists this init as waiting, and no apply starts it again.
