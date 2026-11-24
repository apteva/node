package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// The device-code pairing flow, as the real server will implement it: the
// device asks for a code, a person approves it on /pair, the device's next
// poll collects a token. Issued tokens are kept in a file so restarting the
// sim doesn't unpair the device.

type pairReq struct {
	ID       string
	Code     string
	Name     string
	Version  string
	Hardware []struct{ ID, Kind, Description string }
	Created  time.Time
	Status   string // pending | approved | denied | collected
	Token    string
	Agent    string
}

type pairings struct {
	mu     sync.Mutex
	reqs   map[string]*pairReq // by pairing id
	tokens map[string]string   // token -> agent ("" = the default agent)
	dev    string
	file   string
	agents []string
}

func newPairings(file, devToken string, agents []string) *pairings {
	p := &pairings{reqs: map[string]*pairReq{}, tokens: map[string]string{}, dev: devToken, file: file, agents: agents}
	if b, err := os.ReadFile(file); err == nil {
		json.Unmarshal(b, &p.tokens)
	}
	p.tokens[devToken] = ""
	return p
}

func (p *pairings) save() {
	b, _ := json.MarshalIndent(p.tokens, "", "  ")
	os.WriteFile(p.file, b, 0o600)
}

func (p *pairings) valid(auth string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	_, ok := p.tokens[strings.TrimPrefix(auth, "Bearer ")]
	return ok && strings.HasPrefix(auth, "Bearer ")
}

func (p *pairings) agentFor(auth, fallback string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	if a := p.tokens[strings.TrimPrefix(auth, "Bearer ")]; a != "" {
		return a
	}
	return fallback
}

// revokeAll forgets every paired device except the built-in dev token.
func (p *pairings) revokeAll() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for t := range p.tokens {
		if t != p.dev {
			delete(p.tokens, t)
		}
	}
	p.save()
}

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// Codes avoid look-alike characters: no 0/O, 1/I/L.
func userCode() string {
	const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	rand.Read(b)
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b[:3]) + "-" + string(b[3:])
}

const pairTTL = 10 * time.Minute

func (p *pairings) routes(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok","service":"node-sim"}`))
	})
	mux.HandleFunc("POST /api/devices/pair/start", func(w http.ResponseWriter, r *http.Request) {
		var req pairReq
		json.NewDecoder(r.Body).Decode(&req)
		req.ID, req.Code, req.Created, req.Status = randHex(16), userCode(), time.Now(), "pending"
		p.mu.Lock()
		p.reqs[req.ID] = &req
		p.mu.Unlock()
		verify := fmt.Sprintf("http://%s/pair?code=%s", r.Host, req.Code)
		log.Printf("pairing: %q asks to join — code %s, approve at %s", req.Name, req.Code, verify)
		json.NewEncoder(w).Encode(map[string]any{
			"pairing_id": req.ID, "user_code": req.Code, "verify_url": verify,
			"expires_in": int(pairTTL.Seconds()), "interval": 2,
		})
	})
	mux.HandleFunc("POST /api/devices/pair/poll", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			PairingID string `json:"pairing_id"`
		}
		json.NewDecoder(r.Body).Decode(&body)
		p.mu.Lock()
		defer p.mu.Unlock()
		req := p.reqs[body.PairingID]
		w.Header().Set("Content-Type", "application/json")
		switch {
		case req == nil || req.Status == "collected":
			http.Error(w, `{"error":"unknown pairing"}`, http.StatusNotFound)
		case time.Since(req.Created) > pairTTL:
			w.WriteHeader(http.StatusGone)
		case req.Status == "approved":
			req.Status = "collected"
			p.tokens[req.Token] = req.Agent
			p.save()
			json.NewEncoder(w).Encode(map[string]string{"status": "approved", "token": req.Token, "agent": req.Agent})
		default:
			json.NewEncoder(w).Encode(map[string]string{"status": req.Status})
		}
	})
	mux.HandleFunc("GET /pair", func(w http.ResponseWriter, r *http.Request) {
		code := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("code")))
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		pairPage.Execute(w, map[string]any{"Code": code, "Req": p.byCode(code), "Pending": p.pending(), "Agents": p.agents})
	})
	mux.HandleFunc("POST /pair", func(w http.ResponseWriter, r *http.Request) {
		r.ParseForm()
		code, agent := r.FormValue("code"), r.FormValue("agent")
		p.decide(code, r.FormValue("decision") == "approve", agent)
		http.Redirect(w, r, "/pair?code="+code, http.StatusSeeOther)
	})
}

func (p *pairings) byCode(code string) *pairReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, r := range p.reqs {
		if r.Code == code && time.Since(r.Created) < pairTTL {
			c := *r
			return &c
		}
	}
	return nil
}

func (p *pairings) pending() []pairReq {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []pairReq
	for _, r := range p.reqs {
		if r.Status == "pending" && time.Since(r.Created) < pairTTL {
			out = append(out, *r)
		}
	}
	return out
}

// decide approves or denies the request with this code ("" = newest pending).
func (p *pairings) decide(code string, approve bool, agent string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	var req *pairReq
	for _, r := range p.reqs {
		if r.Status == "pending" && (r.Code == code || code == "" && (req == nil || r.Created.After(req.Created))) {
			req = r
		}
	}
	if req == nil {
		return false
	}
	if !approve {
		req.Status = "denied"
		log.Printf("pairing: denied %s", req.Code)
		return true
	}
	if agent == "" {
		agent = p.agents[0]
	}
	req.Status, req.Token, req.Agent = "approved", "dev_"+randHex(24), agent
	log.Printf("pairing: approved %s (%q → %s)", req.Code, req.Name, agent)
	return true
}

var pairPage = template.Must(template.New("pair").Parse(`<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>Add a device · node-sim</title>
<style>
 body{font:15px/1.5 system-ui,sans-serif;background:#f5f5f4;color:#1c1917;margin:0;padding:32px 16px}
 main{max-width:440px;margin:0 auto;background:#fff;border:1px solid #e7e5e4;border-radius:14px;padding:24px}
 h1{font-size:19px;margin:0 0 4px} .muted{color:#78716c;font-size:13px}
 .code{font:600 26px ui-monospace,monospace;letter-spacing:.08em;margin:14px 0}
 ul{padding-left:18px;margin:8px 0 16px} select,input{font:inherit;padding:8px;border:1px solid #d6d3d1;border-radius:8px;width:100%;box-sizing:border-box}
 .row{display:flex;gap:8px;margin-top:16px} button{flex:1;font:inherit;font-weight:600;padding:10px;border-radius:8px;border:1px solid #d6d3d1;background:#fff;cursor:pointer}
 button.ok{background:#1c1917;color:#fff;border-color:#1c1917} .badge{display:inline-block;padding:2px 8px;border-radius:99px;background:#ecfdf5;color:#047857;font-size:13px}
 a{color:inherit}
</style></head><body><main>
<p class="muted">node-sim · stand-in for the Apteva dashboard</p>
{{with .Req}}
 <h1>Add “{{.Name}}” to this project?</h1>
 <div class="code">{{.Code}}</div>
 <p class="muted">Check this matches the code on the device's screen.</p>
 {{if eq .Status "pending"}}
 <p>It will let the agent use:</p>
 <ul>{{range .Hardware}}<li><b>{{.ID}}</b> — {{.Kind}}{{if .Description}}, {{.Description}}{{end}}</li>{{end}}</ul>
 <form method="post"><input type="hidden" name="code" value="{{.Code}}">
  <label class="muted">Agent</label>
  <select name="agent">{{range $.Agents}}<option>{{.}}</option>{{end}}</select>
  <div class="row"><button name="decision" value="deny">Deny</button><button class="ok" name="decision" value="approve">Approve</button></div>
 </form>
 {{else if or (eq .Status "approved") (eq .Status "collected")}}<p><span class="badge">Approved</span> The device connects to {{.Agent}} in a moment.</p>
 {{else}}<p>Denied. The device can ask again.</p>{{end}}
{{else}}
 <h1>Add a device</h1>
 {{if .Code}}<p>No pending request with code <b>{{.Code}}</b> — it may have expired.</p>{{end}}
 <form method="get"><label class="muted">Code shown on the device</label><input name="code" placeholder="ABC-123" autofocus></form>
 {{if .Pending}}<p class="muted" style="margin-top:20px">Waiting to be approved:</p><ul>{{range .Pending}}<li><a href="/pair?code={{.Code}}">{{.Code}}</a> — {{.Name}}</li>{{end}}</ul>{{end}}
{{end}}
</main></body></html>`))
