package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

// vulnAPI is the free WPVulnerability database (no key needed).
var vulnAPI = "https://www.wpvulnerability.net"

// Vuln is a known vulnerability in something a site runs.
type Vuln struct {
	Kind      string   `json:"kind"` // core, plugin, theme
	Slug      string   `json:"slug"`
	Name      string   `json:"name"`
	Version   string   `json:"version"` // installed
	Title     string   `json:"title"`
	Severity  string   `json:"severity"` // critical, high, medium, low, ""
	Score     float64  `json:"score"`
	FixedIn   string   `json:"fixed_in"`
	Unfixed   bool     `json:"unfixed"`
	Links     []string `json:"links"`
	IDs       []string `json:"ids"`
	Update    string   `json:"update"` // version WordPress offers, if any
	Activated bool     `json:"active"`
}

// VulnReport is the last scan of a site.
type VulnReport struct {
	Checked time.Time `json:"checked"`
	Vulns   []Vuln    `json:"vulns"`
	Error   string    `json:"error,omitempty"`
	Items   int       `json:"items"` // things checked
}

type vulnCacheEntry struct {
	at    time.Time
	vulns []apiVuln
}

type vulnState struct {
	mu    sync.Mutex
	cache map[string]vulnCacheEntry
}

// flex accepts a JSON string, number, bool or null as text.
type flex string

func (f *flex) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err == nil {
		*f = flex(s)
		return nil
	}
	if string(b) == "null" {
		*f = ""
		return nil
	}
	*f = flex(strings.Trim(string(b), `"`))
	return nil
}

type apiVuln struct {
	Name     flex `json:"name"`
	Operator obj[struct {
		MinVersion  flex `json:"min_version"`
		MinOperator flex `json:"min_operator"`
		MaxVersion  flex `json:"max_version"`
		MaxOperator flex `json:"max_operator"`
		Unfixed     flex `json:"unfixed"`
	}] `json:"operator"`
	Source list[struct {
		ID   flex `json:"id"`
		Name flex `json:"name"`
		Link flex `json:"link"`
	}] `json:"source"`
	Impact obj[struct {
		CVSS obj[struct {
			Score    flex `json:"score"`
			Severity flex `json:"severity"`
		}] `json:"cvss"`
	}] `json:"impact"`
}

// obj reads a JSON object. The database is PHP, which sends an empty
// array for an empty object and sometimes wraps an object in an array, so
// an array yields its first object, and anything unreadable becomes the
// zero value instead of failing the whole response.
type obj[T any] struct{ V T }

func (o *obj[T]) UnmarshalJSON(b []byte) error {
	var zero T
	o.V = zero
	t := strings.TrimSpace(string(b))
	switch {
	case strings.HasPrefix(t, "{"):
		var v T
		if json.Unmarshal(b, &v) == nil {
			o.V = v
		}
	case strings.HasPrefix(t, "["):
		var raw []json.RawMessage
		_ = json.Unmarshal(b, &raw)
		for _, r := range raw {
			var v T
			if strings.HasPrefix(strings.TrimSpace(string(r)), "{") && json.Unmarshal(r, &v) == nil {
				o.V = v
				break
			}
		}
	}
	return nil
}

// list reads a JSON array of objects, skipping anything else.
type list[T any] []T

func (l *list[T]) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		*l = nil
		return nil
	}
	out := make([]T, 0, len(raw))
	for _, r := range raw {
		var v T
		if json.Unmarshal(r, &v) == nil {
			out = append(out, v)
		}
	}
	*l = out
	return nil
}

type apiResponse struct {
	Error flex `json:"error"`
	Data  *struct {
		Vulnerability []apiVuln `json:"vulnerability"`
	} `json:"data"`
}

// fetchVulns asks the database about one core version, plugin or theme.
func (e *Engine) fetchVulns(ctx context.Context, kind, slug string) ([]apiVuln, error) {
	key := kind + "/" + slug
	e.vulns.mu.Lock()
	if e.vulns.cache == nil {
		e.vulns.cache = map[string]vulnCacheEntry{}
	}
	if c, ok := e.vulns.cache[key]; ok && time.Since(c.at) < 12*time.Hour {
		e.vulns.mu.Unlock()
		return c.vulns, nil
	}
	e.vulns.mu.Unlock()
	req, err := http.NewRequestWithContext(ctx, "GET", vulnAPI+"/"+kind+"/"+url.PathEscape(slug)+"/", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Berth/"+e.Version)
	res, err := e.httpClient(30 * time.Second).Do(req)
	if err != nil {
		return nil, fmt.Errorf("vulnerability database: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 {
		return nil, nil
	}
	if res.StatusCode != 200 {
		return nil, fmt.Errorf("vulnerability database answered %s", res.Status)
	}
	var r apiResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&r); err != nil {
		return nil, fmt.Errorf("vulnerability database: %w", err)
	}
	var out []apiVuln
	if r.Data != nil {
		out = r.Data.Vulnerability
	}
	e.vulns.mu.Lock()
	e.vulns.cache[key] = vulnCacheEntry{at: time.Now(), vulns: out}
	e.vulns.mu.Unlock()
	return out, nil
}

// compareVersions compares dotted versions numerically (1.10 > 1.9).
// Missing parts count as 0; letters after a number sort before it (1.0-beta < 1.0).
func compareVersions(a, b string) int {
	split := func(v string) []string {
		return strings.FieldsFunc(strings.ToLower(v), func(r rune) bool { return r == '.' || r == '-' || r == '_' || r == '+' })
	}
	pa, pb := split(a), split(b)
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case x == y:
			continue
		case ex == nil && ey == nil:
			if nx != ny {
				if nx < ny {
					return -1
				}
				return 1
			}
		case x == "":
			if ey != nil {
				return 1 // 1.0 > 1.0-beta
			}
			if ny == 0 {
				continue
			}
			return -1
		case y == "":
			if ex != nil {
				return -1
			}
			if nx == 0 {
				continue
			}
			return 1
		case ex == nil:
			return 1 // number beats a pre-release tag
		case ey == nil:
			return -1
		default:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func versionMatches(v, op, bound string) bool {
	if bound == "" || op == "" {
		return true
	}
	c := compareVersions(v, bound)
	switch op {
	case "lt":
		return c < 0
	case "le":
		return c <= 0
	case "gt":
		return c > 0
	case "ge":
		return c >= 0
	case "eq":
		return c == 0
	}
	return true
}

var severityNames = map[string]string{"c": "critical", "h": "high", "m": "medium", "l": "low", "n": ""}

func toVuln(a apiVuln, kind, slug, name, version string) (Vuln, bool) {
	o := a.Operator.V
	if version != "" && (!versionMatches(version, string(o.MinOperator), string(o.MinVersion)) || !versionMatches(version, string(o.MaxOperator), string(o.MaxVersion))) {
		return Vuln{}, false
	}
	v := Vuln{Kind: kind, Slug: slug, Name: name, Version: version, Title: string(a.Name)}
	v.Unfixed = o.Unfixed == "1" || o.Unfixed == "true"
	if string(o.MaxOperator) == "lt" && !v.Unfixed {
		v.FixedIn = string(o.MaxVersion)
	}
	v.Score, _ = strconv.ParseFloat(string(a.Impact.V.CVSS.V.Score), 64)
	sev := strings.ToLower(string(a.Impact.V.CVSS.V.Severity))
	if n, ok := severityNames[sev]; ok {
		sev = n
	}
	if sev == "" && v.Score > 0 {
		switch {
		case v.Score >= 9:
			sev = "critical"
		case v.Score >= 7:
			sev = "high"
		case v.Score >= 4:
			sev = "medium"
		default:
			sev = "low"
		}
	}
	v.Severity = sev
	seen := map[string]bool{}
	for _, s := range a.Source {
		if l := string(s.Link); l != "" && !seen[l] && (strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://")) {
			seen[l] = true
			v.Links = append(v.Links, l)
		}
		if id := string(s.ID); id != "" && !seen["id:"+id] {
			seen["id:"+id] = true
			v.IDs = append(v.IDs, id)
		}
	}
	return v, true
}

var severityRank = map[string]int{"critical": 4, "high": 3, "medium": 2, "low": 1}

// ScanVulns checks a WordPress site's core, plugins and themes.
func (e *Engine) ScanVulns(ctx context.Context, s model.Site) VulnReport {
	rep := VulnReport{Checked: time.Now().UTC(), Vulns: []Vuln{}}
	info := e.WordPressInfo(ctx, s)
	if info.Error != "" {
		rep.Error = info.Error
		return rep
	}
	type item struct{ kind, slug, name, version, update, status string }
	items := []item{{"core", info.Version, "WordPress", info.Version, info.CoreUpdate, "active"}}
	for _, p := range info.Plugins {
		if p.Status == "must-use" || p.Status == "dropin" {
			continue
		}
		items = append(items, item{"plugin", p.Name, p.Title, p.Version, p.Update, p.Status})
	}
	for _, t := range info.Themes {
		items = append(items, item{"theme", t.Name, t.Title, t.Version, t.Update, t.Status})
	}
	var errs []string
	for _, it := range items {
		if it.slug == "" {
			continue
		}
		rep.Items++
		list, err := e.fetchVulns(ctx, it.kind, it.slug)
		if err != nil {
			errs = append(errs, err.Error())
			if len(errs) > 2 {
				break
			}
			continue
		}
		for _, a := range list {
			version := it.version
			if it.kind == "core" {
				version = "" // the core endpoint already answers for this version
			}
			v, ok := toVuln(a, it.kind, it.slug, it.name, version)
			if !ok {
				continue
			}
			v.Version, v.Update, v.Activated = it.version, it.update, it.status == "active" || it.status == "parent"
			if v.Name == "" {
				v.Name = it.slug
			}
			rep.Vulns = append(rep.Vulns, v)
		}
	}
	if len(errs) > 0 {
		rep.Error = errs[0]
	}
	sort.SliceStable(rep.Vulns, func(i, j int) bool {
		return severityRank[rep.Vulns[i].Severity] > severityRank[rep.Vulns[j].Severity]
	})
	return rep
}

// VulnReportFor returns the last stored scan of a site.
func (e *Engine) VulnReportFor(ctx context.Context, site string) (VulnReport, bool) {
	var r VulnReport
	_ = e.Store.GetKV(ctx, "vulns_"+site, &r)
	return r, !r.Checked.IsZero()
}

// RunVulnScan scans a site, stores the result and logs new findings.
func (e *Engine) RunVulnScan(ctx context.Context, s model.Site) VulnReport {
	prev, _ := e.VulnReportFor(ctx, s.Name)
	rep := e.ScanVulns(ctx, s)
	if rep.Error != "" && len(rep.Vulns) == 0 && rep.Items <= 1 {
		// Keep the previous findings when the check could not run.
		prev.Error = rep.Error
		_ = e.Store.SetKV(ctx, "vulns_"+s.Name, prev)
		return prev
	}
	_ = e.Store.SetKV(ctx, "vulns_"+s.Name, rep)
	known := map[string]bool{}
	for _, v := range prev.Vulns {
		known[v.Kind+v.Slug+v.Title] = true
	}
	var fresh []string
	for _, v := range rep.Vulns {
		if !known[v.Kind+v.Slug+v.Title] {
			fresh = append(fresh, v.Name+" "+v.Version)
		}
	}
	if len(fresh) > 0 {
		e.Store.AddEvent(ctx, "warn", "security", s.Name, fmt.Sprintf("%d new known vulnerabilit%s on %s: %s", len(fresh),
			map[bool]string{true: "y", false: "ies"}[len(fresh) == 1], s.Domain, strings.Join(uniq(fresh), ", ")))
	}
	return rep
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// scanAllVulns checks every active WordPress site.
func (e *Engine) scanAllVulns(ctx context.Context) {
	st, _ := e.Store.Settings(ctx)
	if st.NoVulnScan {
		return
	}
	sites, err := e.Store.Sites(ctx)
	if err != nil {
		return
	}
	for _, s := range sites {
		if s.Type == model.TypeWordPress && s.Status == model.StatusActive {
			e.RunVulnScan(ctx, s)
		}
	}
}

// VulnCounts returns the number of findings per site from the last scans.
func (e *Engine) VulnCounts(ctx context.Context, sites []model.Site) map[string]int {
	out := map[string]int{}
	for _, s := range sites {
		if r, ok := e.VulnReportFor(ctx, s.Name); ok && len(r.Vulns) > 0 {
			out[s.Name] = len(r.Vulns)
		}
	}
	return out
}
