# Start the init session of factory `{{.Factory}}` as a Cursor cloud agent

Factory `{{.Factory}}` was just applied for the first time, so its blueprint's init routine, `{{.Routine}}`, should run once, now. (With a Cursor API key, `fabrica apply` starts it by itself; `fabrica auth login cursor-cloud` sets one up.) Start it as a cloud agent:

1. Open Cursor's cloud agents (cursor.com/agents) and start a new agent on repository `{{.Repo}}`.{{if gt (len .Repos) 1}} The factory also deploys to {{range $i, $r := .Repos}}{{if $i}}{{if gt $i 1}}, {{end}}`{{$r}}`{{end}}{{end}}; add them to the agent too, if you can.{{end}}
2. MCP servers. Make sure the agent has these:
   - **fabrica-{{.Factory}}** (custom MCP server, HTTP): URL `{{.Fabrica.URL}}`{{if .Fabrica.Header}}, header `Authorization: {{.Fabrica.Header}}`{{end}}. The factory's automations use the same one, under the same name.
{{- range .Connectors}}
   - **{{.Name}}**: {{.Setup}}
{{- end}}
3. Model: {{.Model.Setup}}.
4. Send this as its prompt, exactly:

   ```
{{indent 3 .Prompt}}
   ```

5. Once it's running, copy the agent's ID (`bc-…`, in its URL) and tell Fabrica:

   ```
   {{.ConfirmCommand}}
   ```

   Until you do, `fabrica plan` lists this init as waiting, and no apply starts it again.
