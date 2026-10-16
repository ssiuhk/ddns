package admin

import "html/template"

var templates = template.Must(template.New("admin").Parse(`
{{define "head"}}<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<title>{{.Title}} · DDNS admin</title>
<style>
:root { --bg:#f6f7f9; --card:#fff; --text:#1d2330; --muted:#5d6675; --line:#dde1e7; --accent:#2457c5; --danger:#b3261e; --ok-bg:#e7f4ea; --ok:#1e6b34; --err-bg:#fbe9e7; }
@media (prefers-color-scheme: dark) {
  :root { --bg:#14171c; --card:#1d2128; --text:#e6e8ec; --muted:#9aa3b1; --line:#323843; --accent:#7ea2ff; --danger:#ff8a80; --ok-bg:#183424; --ok:#8fd6a4; --err-bg:#3a1d1b; }
}
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--text); font:15px/1.5 system-ui, -apple-system, "Segoe UI", sans-serif; }
header { background:var(--card); border-bottom:1px solid var(--line); }
header .wrap { display:flex; align-items:center; justify-content:space-between; gap:12px; padding-top:12px; padding-bottom:12px; }
header a { color:var(--text); text-decoration:none; font-weight:600; }
.wrap { max-width:960px; margin:0 auto; padding:0 16px; }
main { padding:24px 0 48px; }
h1 { font-size:22px; margin:0 0 16px; }
h2 { font-size:17px; margin:0 0 12px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:10px; padding:20px; margin-bottom:20px; }
.muted { color:var(--muted); }
.notice { padding:10px 14px; border-radius:8px; margin-bottom:16px; background:var(--ok-bg); color:var(--ok); }
.error { padding:10px 14px; border-radius:8px; margin-bottom:16px; background:var(--err-bg); color:var(--danger); }
table { width:100%; border-collapse:collapse; }
th, td { text-align:left; padding:9px 8px; border-bottom:1px solid var(--line); vertical-align:top; }
th { color:var(--muted); font-weight:500; font-size:13px; }
td a { color:var(--accent); text-decoration:none; font-weight:500; }
code, .mono { font-family:ui-monospace, SFMono-Regular, Menlo, monospace; font-size:13px; word-break:break-all; }
form.inline { display:inline; }
.row { display:flex; flex-wrap:wrap; gap:12px; align-items:flex-end; }
.row > label { flex:1 1 180px; }
label { display:block; font-size:13px; color:var(--muted); }
input[type=text], input[type=password], input[type=search] { display:block; width:100%; margin-top:4px; padding:8px 10px; border:1px solid var(--line); border-radius:6px; background:var(--bg); color:var(--text); font:inherit; }
label.check { display:flex; align-items:center; gap:6px; color:var(--text); font-size:14px; flex:0 0 auto; padding-bottom:8px; }
button, .button { display:inline-block; padding:8px 14px; border-radius:6px; border:1px solid var(--accent); background:var(--accent); color:#fff; font:inherit; cursor:pointer; text-decoration:none; }
button.secondary { background:transparent; color:var(--accent); }
button.danger { background:transparent; border-color:var(--danger); color:var(--danger); }
button.link { background:none; border:none; color:var(--muted); padding:0; }
.login { max-width:380px; margin:48px auto; }
.login form + form, .login .or { margin-top:16px; }
.full { width:100%; text-align:center; }
dl { display:grid; grid-template-columns:max-content 1fr; gap:8px 16px; margin:0; }
dt { color:var(--muted); }
dd { margin:0; }
.actions { display:flex; flex-wrap:wrap; gap:12px; }
@media (max-width:640px) { .hide-sm { display:none; } }
</style>
</head>
<body>
<header><div class="wrap">
  <a href="/admin">DDNS admin <span class="muted">· {{.Zone}}</span></a>
  {{if .CSRF}}<form class="inline" method="post" action="/admin/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="link">Sign out</button></form>{{end}}
</div></header>
<main><div class="wrap">
{{if .Message}}<div class="notice">{{.Message}}</div>{{end}}
{{end}}

{{define "foot"}}</div></main></body></html>{{end}}

{{define "login.html"}}{{template "head" .}}
<div class="card login">
  <h1>Sign in</h1>
  {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
  {{if .PasswordEnabled}}
  <form method="post" action="/admin/login">
    <label>Admin password<input type="password" name="password" autocomplete="current-password" autofocus required></label>
    <p><button class="full">Sign in</button></p>
  </form>
  {{end}}
  {{if .GoogleEnabled}}
  {{if .PasswordEnabled}}<p class="or muted">or</p>{{end}}
  <p><a class="button full" href="/admin/login/google">Sign in with Google</a></p>
  {{end}}
  {{if not (or .PasswordEnabled .GoogleEnabled)}}
  <p>No sign in method is set up yet. Set the admin password from the server:</p>
  <p><code>docker compose --project-name ddns exec ddns ddns set-admin-password</code></p>
  {{end}}
</div>
{{template "foot"}}{{end}}

{{define "hosts.html"}}{{template "head" .}}
<div class="card">
  <h2>New host</h2>
  {{if .FormError}}<div class="error">{{.FormError}}</div>{{end}}
  <form method="post" action="/admin/hosts">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <div class="row">
      <label>Hostname<input type="text" name="hostname" value="{{.Form.Hostname}}" placeholder="myhost" required></label>
      <label>IP address<input type="text" name="ip" value="{{.Form.IP}}" placeholder="203.0.113.10 or 2001:db8::1" required></label>
      <label class="check"><input type="checkbox" name="persistent" {{if .Form.Persistent}}checked{{end}}> Never expires</label>
      <button>Create</button>
    </div>
  </form>
</div>

<div class="card">
  <div class="row" style="justify-content:space-between; align-items:center; margin-bottom:8px">
    <h2 style="margin:0">Hosts <span class="muted">({{.Total}})</span></h2>
    <form method="get" action="/admin"><input type="search" name="q" value="{{.Filter}}" placeholder="Filter by name or IP" style="margin:0; min-width:220px"></form>
  </div>
  {{if .Hosts}}
  <table>
    <thead><tr><th>Name</th><th class="hide-sm">Type</th><th>Address</th><th>Last updated</th><th class="hide-sm">Expires</th></tr></thead>
    <tbody>
    {{range .Hosts}}
      <tr>
        <td><a href="/admin/hosts/{{.Hostname}}">{{.Hostname}}</a><div class="muted mono">{{.FQDN}}</div></td>
        <td class="hide-sm">{{.Type}}</td>
        <td class="mono">{{.Ip}}</td>
        <td{{if .UpdatedTime}} title="{{.UpdatedTime}}"{{end}}>{{.Updated}}</td>
        <td class="hide-sm">{{.Expires}}</td>
      </tr>
    {{end}}
    </tbody>
  </table>
  {{else}}
  <p class="muted">{{if .Filter}}No host matches “{{.Filter}}”.{{else}}No hosts yet.{{end}}</p>
  {{end}}
</div>

{{if .DNSSEC}}
<div class="card">
  <h2>DNSSEC</h2>
  {{if .DSRecords}}
  <p class="muted">The zone is signed. Publish this DS record in the parent zone to complete the chain of trust:</p>
  {{range .DSRecords}}<p><code>{{.}}</code></p>{{end}}
  {{else}}
  <p class="muted">The zone has no active signing key.</p>
  {{end}}
</div>
{{end}}
{{template "foot"}}{{end}}

{{define "host.html"}}{{template "head" .}}
<p><a href="/admin" class="muted">← All hosts</a></p>
<div class="card">
  <h1>{{.Host.FQDN}}</h1>
  <dl>
    <dt>Record</dt><dd class="mono">{{.Host.Type}} {{.Host.Ip}}</dd>
    <dt>Last updated</dt><dd>{{.Host.Updated}}{{if .Host.UpdatedTime}} <span class="muted">({{.Host.UpdatedTime}})</span>{{end}}</dd>
    <dt>Expires</dt><dd>{{.Host.Expires}}</dd>
    <dt>Token</dt><dd class="mono">{{.Host.Token}}</dd>
    <dt>Update</dt><dd class="mono">{{.UpdateCommand}}</dd>
    <dt>Update URL</dt><dd class="mono">{{.UpdateURL}}</dd>
  </dl>
</div>

<div class="card">
  <h2>Edit</h2>
  {{if .FormError}}<div class="error">{{.FormError}}</div>{{end}}
  <form method="post" action="/admin/hosts/{{.Host.Hostname}}">
    <input type="hidden" name="csrf" value="{{.CSRF}}">
    <div class="row">
      <label>IP address<input type="text" name="ip" value="{{.Host.Ip}}" required></label>
      <label class="check"><input type="checkbox" name="persistent" {{if .Host.Persistent}}checked{{end}}> Never expires</label>
      <button>Save</button>
    </div>
  </form>
</div>

<div class="card">
  <h2>Danger zone</h2>
  <div class="actions">
    <form method="post" action="/admin/hosts/{{.Host.Hostname}}/token">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <button class="secondary">Generate new token</button>
    </form>
    <form method="post" action="/admin/hosts/{{.Host.Hostname}}/delete">
      <input type="hidden" name="csrf" value="{{.CSRF}}">
      <label class="check"><input type="checkbox" name="confirm" required> Yes, delete {{.Host.Hostname}}</label>
      <button class="danger">Delete host</button>
    </form>
  </div>
</div>
{{template "foot"}}{{end}}
`))
