package mcpauth

import (
	"html/template"
	"net/http"
)

// The one page a person sees: sign in to let Claude use the portfolio.
// Plain HTML, no scripts, nothing loaded from elsewhere.

type loginPage struct {
	Client string // the client's own name for itself, e.g. "Claude"
	Form   string // the signed request
	Email  string
	Error  string
}

var loginTmpl = template.Must(template.New("login").Parse(`<!DOCTYPE html>
<html lang="uz"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Portfolio - kirish</title>
<style>
body{font:16px/1.5 system-ui,sans-serif;background:#f6f5f2;color:#222;margin:0;display:flex;min-height:100vh;align-items:center;justify-content:center}
main{background:#fff;border:1px solid #e3e1db;border-radius:12px;padding:28px;width:min(360px,90vw)}
h1{font-size:20px;font-weight:600;margin:0 0 6px}p{margin:0 0 18px;color:#555;font-size:14px}
label{display:block;font-size:14px;margin:12px 0 4px}input{width:100%;box-sizing:border-box;padding:10px;border:1px solid #ccc;border-radius:8px;font:inherit}
.err{background:#fdecea;color:#8a1c12;padding:10px;border-radius:8px;font-size:14px;margin:0 0 12px}
.row{display:flex;gap:8px;margin-top:20px}button{flex:1;padding:10px;border-radius:8px;border:1px solid #222;background:#222;color:#fff;font:inherit;cursor:pointer}
button.ghost{background:#fff;color:#222;border-color:#ccc}
</style></head><body><main>
<h1>Portfolio</h1>
<p>{{if .Client}}<b>{{.Client}}</b>{{else}}Ilova{{end}} portfolio tool'laridan foydalanmoqchi: postlar, loyihalar, xabarlar, tizim holati, moliya va o'sish. Ruxsat berish uchun admin login va parolingiz bilan kiring.</p>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<form method="post" action="/authorize">
<input type="hidden" name="form" value="{{.Form}}">
<label for="email">Email</label><input id="email" name="email" type="email" autocomplete="username" value="{{.Email}}" required>
<label for="password">Parol</label><input id="password" name="password" type="password" autocomplete="current-password">
<div class="row"><button class="ghost" name="action" value="deny" type="submit" formnovalidate>Rad etish</button><button name="action" value="allow" type="submit">Ruxsat berish</button></div>
</form></main></body></html>`))

var errorTmpl = template.Must(template.New("error").Parse(`<!DOCTYPE html>
<html lang="uz"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>Portfolio</title></head>
<body style="font:16px/1.5 system-ui,sans-serif;max-width:420px;margin:15vh auto;padding:0 20px">
<h1 style="font-size:20px">Kirib bo'lmadi</h1><p>{{.}}</p></body></html>`))

func pageHeaders(w http.ResponseWriter) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "no-referrer")
	// No form-action: the answer to the form is a redirect to Claude, and
	// browsers apply form-action to that redirect too.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; frame-ancestors 'none'")
}

func renderLogin(w http.ResponseWriter, status int, p loginPage) {
	pageHeaders(w)
	w.WriteHeader(status)
	loginTmpl.Execute(w, p)
}

func renderError(w http.ResponseWriter, status int, msg string) {
	pageHeaders(w)
	w.WriteHeader(status)
	errorTmpl.Execute(w, msg)
}
