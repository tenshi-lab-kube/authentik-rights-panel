package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

type Config struct {
	AuthentikBaseURL  string          `json:"authentik_base_url"`
	ListenAddr        string          `json:"listen_addr"`
	ProjectGroupPref  string          `json:"project_group_prefix"`
	ExcludeGroupNames []string        `json:"exclude_group_names"`
	Projects          []ProjectConfig `json:"projects"`
}

type ProjectConfig struct {
	Name        string `json:"name"`
	Group       string `json:"group"`
	Description string `json:"description"`
}

type Project struct {
	Name, GroupName, GroupPK, Description string
}

type User struct {
	PK       int      `json:"pk"`
	Username string   `json:"username"`
	Name     string   `json:"name"`
	Email    string   `json:"email"`
	IsActive bool     `json:"is_active"`
	Groups   []string `json:"groups"`
}

type Group struct {
	PK          string `json:"pk"`
	Name        string `json:"name"`
	IsSuperuser bool   `json:"is_superuser"`
}

type listResponse[T any] struct{ Results []T `json:"results"` }

type app struct {
	cfg  Config
	api  *authentikClient
	tmpl *template.Template
}

type pageData struct {
	Users        []User
	Projects     []Project
	Query        string
	Status       string
	Error        string
	MissingSetup bool
	BaseURL      string
}

type authentikClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}

	client := &authentikClient{
		baseURL: strings.TrimRight(cfg.AuthentikBaseURL, "/"),
		token:   strings.TrimSpace(os.Getenv("AUTHENTIK_TOKEN")),
		httpClient: &http.Client{Timeout: 20 * time.Second},
	}

	a := &app{cfg: cfg, api: client, tmpl: template.Must(template.New("page").Funcs(template.FuncMap{"hasGroup": hasGroup}).Parse(pageTemplate))}
	mux := http.NewServeMux()
	mux.HandleFunc("/", a.index)
	mux.HandleFunc("POST /toggle", a.toggle)

	log.Printf("listening on http://%s", cfg.ListenAddr)
	log.Fatal(http.ListenAndServe(cfg.ListenAddr, securityHeaders(mux)))
}

func loadConfig() (Config, error) {
	cfg := Config{ListenAddr: "127.0.0.1:8080"}
	path := strings.TrimSpace(os.Getenv("CONFIG_PATH"))
	if path == "" {
		path = "config.json"
	}
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("read %s: %w", path, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return cfg, err
	}
	if v := strings.TrimSpace(os.Getenv("AUTHENTIK_BASE_URL")); v != "" {
		cfg.AuthentikBaseURL = v
	}
	if v := strings.TrimSpace(os.Getenv("LISTEN_ADDR")); v != "" {
		cfg.ListenAddr = v
	}
	cfg.AuthentikBaseURL = strings.TrimRight(cfg.AuthentikBaseURL, "/")
	return cfg, nil
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	data := pageData{
		Query:        strings.TrimSpace(r.URL.Query().Get("q")),
		Status:       strings.TrimSpace(r.URL.Query().Get("status")),
		BaseURL:      a.cfg.AuthentikBaseURL,
		MissingSetup: a.cfg.AuthentikBaseURL == "" || a.api.token == "",
	}
	if !data.MissingSetup {
		users, projects, err := a.directory(r.Context(), data.Query)
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Users, data.Projects = users, projects
		}
	}
	a.render(w, data)
}

func (a *app) toggle(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		redirect(w, r, "", "Formulaire illisible")
		return
	}
	query := r.FormValue("q")
	userID, groupPK := r.FormValue("user_id"), r.FormValue("group_pk")
	enabled := r.FormValue("enabled") == "true"

	user, err := a.api.getUser(r.Context(), userID)
	if err != nil {
		redirect(w, r, query, "Lecture impossible: "+err.Error())
		return
	}

	next := append([]string(nil), user.Groups...)
	has := hasGroup(next, groupPK)
	if enabled && !has {
		next = append(next, groupPK)
	}
	if !enabled && has {
		out := next[:0]
		for _, g := range next {
			if g != groupPK {
				out = append(out, g)
			}
		}
		next = out
	}
	if err := a.api.patchUserGroups(r.Context(), userID, next); err != nil {
		redirect(w, r, query, "Modification refusee: "+err.Error())
		return
	}
	redirect(w, r, query, "Droits mis a jour")
}

func (a *app) directory(ctx context.Context, query string) ([]User, []Project, error) {
	users, err := a.api.listUsers(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	groups, err := a.api.listGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	return users, buildProjects(a.cfg, groups), nil
}

func buildProjects(cfg Config, groups []Group) []Project {
	byName := map[string]Group{}
	for _, g := range groups {
		byName[g.Name] = g
	}
	projects := []Project{}
	for _, p := range cfg.Projects {
		if g, ok := byName[p.Group]; ok {
			name := p.Name
			if name == "" {
				name = g.Name
			}
			projects = append(projects, Project{Name: name, GroupName: g.Name, GroupPK: g.PK, Description: p.Description})
		}
	}
	if len(projects) == 0 {
		excluded := map[string]bool{}
		for _, name := range cfg.ExcludeGroupNames {
			excluded[strings.ToLower(name)] = true
		}
		for _, g := range groups {
			if g.IsSuperuser || excluded[strings.ToLower(g.Name)] || (cfg.ProjectGroupPref != "" && !strings.HasPrefix(g.Name, cfg.ProjectGroupPref)) {
				continue
			}
			projects = append(projects, Project{Name: strings.TrimPrefix(g.Name, cfg.ProjectGroupPref), GroupName: g.Name, GroupPK: g.PK})
		}
	}
	sort.Slice(projects, func(i, j int) bool { return strings.ToLower(projects[i].Name) < strings.ToLower(projects[j].Name) })
	return projects
}

func (c *authentikClient) listUsers(ctx context.Context, search string) ([]User, error) {
	users := []User{}
	for page := 1; page <= 50; page++ {
		v := url.Values{"include_groups": {"true"}, "page_size": {"200"}, "page": {fmt.Sprint(page)}, "ordering": {"username"}}
		if search != "" {
			v.Set("search", search)
		}
		var res listResponse[User]
		if err := c.do(ctx, http.MethodGet, "/api/v3/core/users/?"+v.Encode(), nil, &res); err != nil {
			return nil, err
		}
		users = append(users, res.Results...)
		if len(res.Results) < 200 {
			break
		}
	}
	return users, nil
}

func (c *authentikClient) listGroups(ctx context.Context) ([]Group, error) {
	groups := []Group{}
	for page := 1; page <= 50; page++ {
		v := url.Values{"include_users": {"false"}, "page_size": {"500"}, "page": {fmt.Sprint(page)}, "ordering": {"name"}}
		var res listResponse[Group]
		if err := c.do(ctx, http.MethodGet, "/api/v3/core/groups/?"+v.Encode(), nil, &res); err != nil {
			return nil, err
		}
		groups = append(groups, res.Results...)
		if len(res.Results) < 500 {
			break
		}
	}
	return groups, nil
}

func (c *authentikClient) getUser(ctx context.Context, id string) (User, error) {
	var user User
	return user, c.do(ctx, http.MethodGet, "/api/v3/core/users/"+url.PathEscape(id)+"/", nil, &user)
}

func (c *authentikClient) patchUserGroups(ctx context.Context, id string, groups []string) error {
	return c.do(ctx, http.MethodPatch, "/api/v3/core/users/"+url.PathEscape(id)+"/", map[string][]string{"groups": groups}, nil)
}

func (c *authentikClient) do(ctx context.Context, method, path string, payload, target any) error {
	if c.baseURL == "" || c.token == "" {
		return errors.New("AUTHENTIK_BASE_URL ou AUTHENTIK_TOKEN manquant")
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Authentik HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if target == nil {
		return nil
	}
	return json.Unmarshal(data, target)
}

func (a *app) render(w http.ResponseWriter, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := a.tmpl.Execute(w, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func redirect(w http.ResponseWriter, r *http.Request, query, status string) {
	v := url.Values{"status": {status}}
	if query != "" {
		v.Set("q", query)
	}
	http.Redirect(w, r, "/?"+v.Encode(), http.StatusSeeOther)
}

func hasGroup(groups []string, groupPK string) bool {
	for _, g := range groups {
		if g == groupPK {
			return true
		}
	}
	return false
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'unsafe-inline' 'self'; script-src 'unsafe-inline' 'self'")
		next.ServeHTTP(w, r)
	})
}

const pageTemplate = `<!doctype html>
<html lang="fr">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Gestion des droits Authentik</title>
  <style>
    :root{--ink:#17201c;--muted:#64736d;--line:#dbe3df;--paper:#f7f9f8;--panel:#fff;--accent:#197b68;--accent-dark:#0f5b4c;--warn:#a44426;--shadow:0 12px 28px rgba(30,47,42,.10)}
    *{box-sizing:border-box}body{margin:0;font-family:ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;color:var(--ink);background:var(--paper)}
    header{display:flex;align-items:center;justify-content:space-between;gap:16px;padding:20px 28px;background:#fff;border-bottom:1px solid var(--line);position:sticky;top:0;z-index:10}h1{margin:0;font-size:20px;letter-spacing:0}.base-url{color:var(--muted);font-size:13px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;max-width:42vw}
    main{padding:24px 28px 40px;max-width:1380px;margin:0 auto}.toolbar{display:grid;grid-template-columns:minmax(220px,420px) 1fr;gap:16px;align-items:center;margin-bottom:18px}.search{display:flex;background:var(--panel);border:1px solid var(--line);border-radius:8px;box-shadow:var(--shadow);overflow:hidden}.search input{width:100%;border:0;padding:13px 14px;font-size:15px;outline:none}.search button{border:0;background:var(--accent);color:#fff;padding:0 16px;font-weight:700;cursor:pointer}.search button:hover{background:var(--accent-dark)}
    .notice,.error,.empty{padding:14px 16px;border-radius:8px;border:1px solid var(--line);background:var(--panel);color:var(--muted)}.error{border-color:#e5b7a8;color:var(--warn);background:#fff8f5;margin-bottom:16px}.notice{color:var(--accent-dark);border-color:#b7d8d0;background:#f3fbf8}.setup{max-width:760px;line-height:1.6}.grid-wrap{background:var(--panel);border:1px solid var(--line);border-radius:8px;overflow:auto;box-shadow:var(--shadow)}
    table{width:100%;border-collapse:separate;border-spacing:0;min-width:780px}th,td{border-bottom:1px solid var(--line);padding:12px;text-align:left;vertical-align:middle}th{position:sticky;top:66px;background:#fbfcfb;z-index:5;font-size:12px;text-transform:uppercase;color:var(--muted);letter-spacing:.04em}th:first-child,td:first-child{position:sticky;left:0;background:inherit;z-index:4;min-width:260px}th:first-child{z-index:6;background:#fbfcfb}tbody tr{background:#fff}tbody tr:hover{background:#f6faf8}
    .person{display:flex;flex-direction:column;gap:3px}.person strong{font-size:14px}.person span,.project-head span{color:var(--muted);font-size:13px}.project-head{display:flex;flex-direction:column;gap:3px;min-width:150px}.project-head span{font-size:11px;text-transform:none;letter-spacing:0;font-weight:500}.toggle-form{display:flex;justify-content:center}.switch{position:relative;width:52px;height:30px;display:inline-block}.switch input{opacity:0;width:0;height:0}.slider{position:absolute;cursor:pointer;inset:0;background:#cbd5d0;border-radius:999px;transition:.15s ease}.slider:before{content:"";position:absolute;height:24px;width:24px;left:3px;top:3px;background:#fff;border-radius:50%;box-shadow:0 2px 5px rgba(0,0,0,.18);transition:.15s ease}input:checked+.slider{background:var(--accent)}input:checked+.slider:before{transform:translateX(22px)}.inactive{color:var(--warn);font-size:12px;font-weight:700}
    @media (max-width:760px){header{align-items:flex-start;flex-direction:column;padding:16px}.base-url{max-width:100%}main{padding:16px}.toolbar{grid-template-columns:1fr}th{top:88px}}
  </style>
</head>
<body>
  <header><h1>Gestion des droits Authentik</h1><div class="base-url">{{if .BaseURL}}{{.BaseURL}}{{else}}Authentik non configure{{end}}</div></header>
  <main>
    {{if .MissingSetup}}
      <section class="setup notice"><strong>Configuration manquante.</strong><br>Renseigne <code>config.json</code> puis lance l'application avec <code>AUTHENTIK_TOKEN</code>.</section>
    {{else}}
      <div class="toolbar"><form class="search" action="/" method="get"><input name="q" value="{{.Query}}" placeholder="Chercher une personne, un email..."><button type="submit">Chercher</button></form>{{if .Status}}<div class="notice">{{.Status}}</div>{{end}}</div>
      {{if .Error}}<div class="error">{{.Error}}</div>{{end}}
      {{if not .Projects}}<div class="empty">Aucun projet trouve. Ajoute tes groupes projet dans <code>config.json</code>, ou configure <code>project_group_prefix</code>.</div>{{else if not .Users}}<div class="empty">Aucune personne ne correspond a cette recherche.</div>{{else}}
        <div class="grid-wrap"><table><thead><tr><th>Personne</th>{{range .Projects}}<th><div class="project-head"><strong>{{.Name}}</strong><span>{{if .Description}}{{.Description}}{{else}}{{.GroupName}}{{end}}</span></div></th>{{end}}</tr></thead><tbody>
        {{range $user := .Users}}<tr><td><div class="person"><strong>{{if $user.Name}}{{$user.Name}}{{else}}{{$user.Username}}{{end}}</strong><span>{{$user.Username}}{{if $user.Email}} &middot; {{$user.Email}}{{end}}</span>{{if not $user.IsActive}}<span class="inactive">Compte desactive</span>{{end}}</div></td>{{range $project := $.Projects}}<td><form class="toggle-form" method="post" action="/toggle"><input type="hidden" name="user_id" value="{{$user.PK}}"><input type="hidden" name="group_pk" value="{{$project.GroupPK}}"><input type="hidden" name="q" value="{{$.Query}}"><input type="hidden" name="enabled" value="{{if hasGroup $user.Groups $project.GroupPK}}false{{else}}true{{end}}"><label class="switch" title="Basculer l'acces"><input type="checkbox" {{if hasGroup $user.Groups $project.GroupPK}}checked{{end}} onchange="this.form.submit()"><span class="slider"></span></label></form></td>{{end}}</tr>{{end}}
        </tbody></table></div>
      {{end}}
    {{end}}
  </main>
</body>
</html>`
