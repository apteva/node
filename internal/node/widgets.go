package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/apteva/node/internal/link"
	"github.com/apteva/node/internal/proto"
)

// Widgets are app-defined native surfaces (apteva-native-surface/v1,
// presentation "widget") placed on this device by the server. The daemon
// keeps the documents, relays their data requests over the link, and caches
// the last answer to each read so tiles still show something offline.

const (
	surfaceMaxBytes = 256 << 10 // same bound as app-sdk's NativeSurfaceMaxBytes
	appRequestWait  = 15 * time.Second
	readCacheLimit  = 64
)

// WidgetView is what the UI renders: the placement plus a revision that bumps
// whenever one of its refresh topics fires, so the UI knows to refetch.
type WidgetView struct {
	proto.Widget
	Rev int `json:"rev"`
}

type widgetStore struct {
	mu      sync.Mutex
	widgets []WidgetView
	cache   map[string]cachedRead
	order   []string // cache keys, oldest first

	pendMu  sync.Mutex
	pending map[string]chan proto.AppResponse
	seq     int
}

type cachedRead struct {
	resp proto.AppResponse
	at   time.Time
}

func newWidgetStore() *widgetStore {
	return &widgetStore{cache: map[string]cachedRead{}, pending: map[string]chan proto.AppResponse{}}
}

// checkWidget rejects documents this Node can't render. Full validation is
// the server's job (app-sdk ValidateNativeSurface); this is the fail-closed
// floor so a bad document becomes an "update required" tile, not a crash.
func checkWidget(w proto.Widget) error {
	if w.ID == "" || w.App == "" {
		return errors.New("widget needs id and app")
	}
	if len(w.Surface) > surfaceMaxBytes {
		return fmt.Errorf("surface exceeds %d bytes", surfaceMaxBytes)
	}
	var head struct {
		Schema       string `json:"schema"`
		Presentation string `json:"presentation"`
	}
	if err := json.Unmarshal(w.Surface, &head); err != nil {
		return fmt.Errorf("surface: %w", err)
	}
	if head.Schema != proto.NativeSurfaceSchema {
		return fmt.Errorf("surface schema %q unsupported", head.Schema)
	}
	if head.Presentation != "widget" {
		return fmt.Errorf("surface presentation %q is not a widget", head.Presentation)
	}
	return nil
}

func (n *Node) setWidgets(ws []proto.Widget) {
	views := make([]WidgetView, 0, len(ws))
	for _, w := range ws {
		if err := checkWidget(w); err != nil {
			n.log.Warn("skipping widget", "id", w.ID, "app", w.App, "err", err)
			continue
		}
		if w.Size != "full" {
			w.Size = "half"
		}
		views = append(views, WidgetView{Widget: w})
	}
	n.widgets.mu.Lock()
	old := n.widgets.widgets
	// Keep revisions for placements that survive, so tiles don't refetch for nothing.
	for i := range views {
		for _, o := range old {
			if o.ID == views[i].ID && string(o.Surface) == string(views[i].Surface) {
				views[i].Rev = o.Rev
			}
		}
	}
	n.widgets.widgets = views
	n.widgets.mu.Unlock()
	n.changed()
}

func (n *Node) onAppEvent(ev proto.AppEvent) {
	n.widgets.mu.Lock()
	hit := false
	for i := range n.widgets.widgets {
		w := &n.widgets.widgets[i]
		if w.App == ev.App && slices.Contains(w.RefreshTopics, ev.Topic) {
			w.Rev++
			hit = true
		}
	}
	n.widgets.mu.Unlock()
	if hit {
		n.changed()
	}
}

func (n *Node) widgetViews() []WidgetView {
	n.widgets.mu.Lock()
	defer n.widgets.mu.Unlock()
	return append([]WidgetView{}, n.widgets.widgets...)
}

func (n *Node) onAppResponse(id string, r proto.AppResponse) {
	n.widgets.pendMu.Lock()
	ch := n.widgets.pending[id]
	delete(n.widgets.pending, id)
	n.widgets.pendMu.Unlock()
	if ch != nil {
		ch <- r
	}
}

// AppResult is what the UI gets back.
type AppResult struct {
	proto.AppResponse
	Stale    bool      `json:"stale,omitempty"`
	CachedAt time.Time `json:"cached_at,omitzero"`
}

// AppRequest relays one widget request to its app through the server. Only
// apps that currently have a widget on this device can be reached. Reads
// fall back to the last good answer when the link is down.
func (n *Node) AppRequest(ctx context.Context, req proto.AppRequest) (AppResult, error) {
	if req.ProjectID == "" {
		n.mu.Lock()
		req.ProjectID = n.agent.ProjectID
		n.mu.Unlock()
	}
	req.Method = strings.ToUpper(req.Method)
	if req.Method == "" {
		req.Method = http.MethodGet
	}
	switch req.Method {
	case http.MethodGet, http.MethodPost, http.MethodPatch, http.MethodDelete:
	default:
		return AppResult{}, fmt.Errorf("method %s not allowed", req.Method)
	}
	if !strings.HasPrefix(req.Path, "/") || strings.Contains(req.Path, "..") || strings.ContainsAny(req.Path, "?#") || strings.Contains(req.Path, "://") {
		return AppResult{}, fmt.Errorf("path %q must be app-relative", req.Path)
	}
	if !n.hasWidgetFor(req.App) {
		return AppResult{}, fmt.Errorf("no widget for app %q on this device", req.App)
	}

	key := ""
	if req.Method == http.MethodGet {
		key = cacheKey(req)
	}
	cached := func(cause error) (AppResult, error) {
		n.widgets.mu.Lock()
		c, ok := n.widgets.cache[key]
		n.widgets.mu.Unlock()
		if key == "" || !ok {
			return AppResult{}, cause
		}
		return AppResult{AppResponse: c.resp, Stale: true, CachedAt: c.at}, nil
	}

	if n.linkStatus().State != link.StateConnected {
		return cached(errors.New("offline"))
	}

	n.widgets.pendMu.Lock()
	n.widgets.seq++
	id := fmt.Sprintf("a%d", n.widgets.seq)
	ch := make(chan proto.AppResponse, 1)
	n.widgets.pending[id] = ch
	n.widgets.pendMu.Unlock()
	defer func() {
		n.widgets.pendMu.Lock()
		delete(n.widgets.pending, id)
		n.widgets.pendMu.Unlock()
	}()

	n.sendFrame(proto.TypeAppRequest, id, req)

	ctx, cancel := context.WithTimeout(ctx, appRequestWait)
	defer cancel()
	select {
	case r := <-ch:
		if key != "" && r.Status >= 200 && r.Status < 300 {
			n.remember(key, r)
		}
		return AppResult{AppResponse: r}, nil
	case <-ctx.Done():
		return cached(errors.New("app did not answer in time"))
	}
}

func (n *Node) hasWidgetFor(app string) bool {
	n.widgets.mu.Lock()
	defer n.widgets.mu.Unlock()
	for _, w := range n.widgets.widgets {
		if w.App == app {
			return true
		}
	}
	return false
}

func (n *Node) remember(key string, r proto.AppResponse) {
	n.widgets.mu.Lock()
	defer n.widgets.mu.Unlock()
	if _, ok := n.widgets.cache[key]; !ok {
		n.widgets.order = append(n.widgets.order, key)
	}
	n.widgets.cache[key] = cachedRead{resp: r, at: time.Now()}
	for len(n.widgets.order) > readCacheLimit {
		delete(n.widgets.cache, n.widgets.order[0])
		n.widgets.order = n.widgets.order[1:]
	}
}

func cacheKey(r proto.AppRequest) string {
	keys := make([]string, 0, len(r.Query))
	for k := range r.Query {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(r.App + " " + r.Path)
	for _, k := range keys {
		v, _ := json.Marshal(r.Query[k])
		b.WriteString(" " + k + "=" + string(v))
	}
	return b.String()
}
