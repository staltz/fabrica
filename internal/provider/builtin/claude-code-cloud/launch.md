# Start the init session of factory `{{.Factory}}` in Claude Code on the web

Factory `{{.Factory}}` was just applied for the first time, so its blueprint's init routine, `{{.Routine}}`, should run once, now. Start it as a one-off session:

1. Open Claude Code on the web (claude.ai/code) and start a new session on repository `{{.Repo}}`.{{if gt (len .Repos) 1}} The factory also deploys to {{range $i, $r := .Repos}}{{if $i}}{{if gt $i 1}}, {{end}}`{{$r}}`{{end}}{{end}}; the session can reach those through its connectors.{{end}}
2. Connectors. Make sure the session has exactly these:
   - **fabrica** (a custom connector): URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, request header `Authorization: {{.Fabrica.Header}}`{{end}}
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
3. Model: {{.Model.Setup}}.
4. Send this as the first message, exactly:

   ```
{{indent 3 .Prompt}}
   ```

5. Once it's running, copy the session's URL and tell Fabrica:

   ```
   {{.ConfirmCommand}}
   ```

   Until you do, `fabrica plan` lists this init as waiting, and no apply starts it again.
