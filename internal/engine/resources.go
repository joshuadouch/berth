package engine

import (
	"bufio"
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/joshuadouch/berth/internal/store"
)

// clockTicks is USER_HZ, 100 on every Linux Berth runs on.
const clockTicks = 100

type procSample struct {
	ticks uint64
	site  string
}

type resourceState struct {
	mu       sync.Mutex
	prev     map[int]procSample
	prevAt   time.Time
	prevCPU  [2]uint64 // busy, total jiffies from /proc/stat
	uids     map[uint32]string
	uidsAt   time.Time
	cpuSum   map[string]float64
	memSum   map[string]float64
	samples  int
	lastSave time.Time
}

// siteUIDs maps each site user's uid to the site name.
func (e *Engine) siteUIDs(ctx context.Context) map[uint32]string {
	r := &e.res
	if r.uids != nil && time.Since(r.uidsAt) < 5*time.Minute {
		return r.uids
	}
	out := map[uint32]string{}
	sites, _ := e.Store.Sites(ctx)
	for _, s := range sites {
		if u, err := user.Lookup(s.User()); err == nil {
			if id, err := strconv.ParseUint(u.Uid, 10, 32); err == nil {
				out[uint32(id)] = s.Name
			}
		}
	}
	r.uids, r.uidsAt = out, time.Now()
	return out
}

// readProcStat returns utime+stime and resident memory of a process.
func readProcStat(pid int) (uint64, int64, bool) {
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, 0, false
	}
	s := string(b)
	// The command name is in parentheses and may contain spaces.
	i := strings.LastIndexByte(s, ')')
	if i < 0 {
		return 0, 0, false
	}
	f := strings.Fields(s[i+1:])
	// Fields after ")": state is f[0]; utime is field 14 overall -> f[11].
	if len(f) < 22 {
		return 0, 0, false
	}
	ut, _ := strconv.ParseUint(f[11], 10, 64)
	st, _ := strconv.ParseUint(f[12], 10, 64)
	rss, _ := strconv.ParseInt(f[21], 10, 64)
	return ut + st, rss * int64(os.Getpagesize()), true
}

func readCPUTotals() (busy, total uint64) {
	f, err := os.Open("/proc/stat")
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		return 0, 0
	}
	fields := strings.Fields(sc.Text())
	if len(fields) < 5 || fields[0] != "cpu" {
		return 0, 0
	}
	for i, v := range fields[1:] {
		n, _ := strconv.ParseUint(v, 10, 64)
		total += n
		if i != 3 && i != 4 { // idle and iowait
			busy += n
		}
	}
	return busy, total
}

func memUsed() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	var total, avail int64
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		n, _ := strconv.ParseInt(f[1], 10, 64)
		switch f[0] {
		case "MemTotal:":
			total = n * 1024
		case "MemAvailable:":
			avail = n * 1024
		}
	}
	return total - avail
}

// sampleResources measures CPU and memory per site user. It runs every 15
// seconds and saves five-minute averages.
func (e *Engine) sampleResources(ctx context.Context, now time.Time) {
	r := &e.res
	r.mu.Lock()
	defer r.mu.Unlock()
	uids := e.siteUIDs(ctx)
	cur := map[int]procSample{}
	mem := map[string]float64{}
	ents, _ := os.ReadDir("/proc")
	for _, en := range ents {
		pid, err := strconv.Atoi(en.Name())
		if err != nil {
			continue
		}
		fi, err := os.Stat(filepath.Join("/proc", en.Name()))
		if err != nil {
			continue
		}
		st, ok := fi.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		site, ok := uids[st.Uid]
		if !ok {
			continue
		}
		ticks, rss, ok := readProcStat(pid)
		if !ok {
			continue
		}
		cur[pid] = procSample{ticks: ticks, site: site}
		mem[site] += float64(rss)
	}
	busy, total := readCPUTotals()
	if r.prev != nil && !r.prevAt.IsZero() {
		secs := now.Sub(r.prevAt).Seconds()
		if secs > 0 {
			if r.cpuSum == nil {
				r.cpuSum, r.memSum = map[string]float64{}, map[string]float64{}
			}
			cpu := map[string]float64{}
			for pid, p := range cur {
				prev, seen := r.prev[pid]
				d := p.ticks
				if seen && prev.ticks <= p.ticks {
					d = p.ticks - prev.ticks
				} else if seen {
					d = 0
				}
				cpu[p.site] += float64(d) / clockTicks / secs * 100
			}
			for site := range uids {
				name := uids[site]
				r.cpuSum[name] += cpu[name]
				r.memSum[name] += mem[name]
			}
			if total > r.prevCPU[1] {
				r.cpuSum[""] += float64(busy-r.prevCPU[0]) / float64(total-r.prevCPU[1]) * 100
			}
			r.memSum[""] += float64(memUsed())
			r.samples++
		}
	}
	r.prev, r.prevAt, r.prevCPU = cur, now, [2]uint64{busy, total}
	if r.samples > 0 && now.Sub(r.lastSave) >= 5*time.Minute {
		usage := e.SiteUsage()
		out := map[string]store.Metric{}
		var disk, db int64
		for name, c := range r.cpuSum {
			m := store.Metric{CPU: c / float64(r.samples), Mem: int64(r.memSum[name] / float64(r.samples))}
			if name != "" {
				m.Disk, m.DB = usage[name].Files, usage[name].DB
				disk += m.Disk
				db += m.DB
			}
			out[name] = m
		}
		if m, ok := out[""]; ok {
			m.Disk, m.DB = disk, db
			out[""] = m
		}
		at := now.Unix() / 300 * 300
		if err := e.Store.AddMetrics(ctx, at, out); err != nil {
			e.Log.Warn("metrics", "err", err)
		}
		r.cpuSum, r.memSum, r.samples, r.lastSave = nil, nil, 0, now
	}
}

// ResourceHistory returns averaged samples over a period ("" site = the server).
func (e *Engine) ResourceHistory(ctx context.Context, site string, since time.Duration) ([]store.Metric, error) {
	step := int64(300)
	switch {
	case since > 7*24*time.Hour:
		step = 6 * 3600
	case since > 2*24*time.Hour:
		step = 3600
	case since > 12*time.Hour:
		step = 900
	}
	return e.Store.Metrics(ctx, site, time.Now().Add(-since).Unix(), step)
}
