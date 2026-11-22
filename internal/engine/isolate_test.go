package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joshuadouch/berth/internal/model"
)

func TestIsolatedAccounts(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()
	var cmds []string
	e.Run.Record = func(c string) { cmds = append(cmds, c) }
	_ = e.Store.SaveAccount(ctx, model.Account{ID: "abc123", Name: "Acme", CreatedAt: time.Now()})
	_ = e.Store.SaveAccount(ctx, model.Account{ID: "own", Name: "Me", CreatedAt: time.Now()})
	for _, s := range []model.Site{
		{ID: "shop", Name: "shop", Type: model.TypePHP, Domain: "shop.test", PHPVersion: "8.3", Enabled: true, AccountID: "abc123", Status: model.StatusActive},
		{ID: "blog", Name: "blog", Type: model.TypePHP, Domain: "blog.test", PHPVersion: "8.2", Enabled: true, AccountID: "abc123", Status: model.StatusActive},
		{ID: "mine", Name: "mine", Type: model.TypePHP, Domain: "mine.test", PHPVersion: "8.3", Enabled: true, AccountID: "own", Status: model.StatusActive},
	} {
		_ = s.Normalize()
		_ = e.Store.SaveSite(ctx, s)
	}
	if err := e.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	shared := e.Paths().PoolFile("8.3", "shop")
	if _, err := os.Stat(shared); err != nil {
		t.Fatal("shared pool missing before isolation")
	}

	// A plan with caps moves the account's pools into masters of its own.
	_ = e.Store.SavePlan(ctx, model.Plan{ID: "p", Name: "Capped", CPUPercent: 150, RAMMB: 1024, MaxProcesses: 200})
	a, _ := e.Store.Account(ctx, "abc123")
	a.PlanID = "p"
	_ = e.Store.SaveAccount(ctx, a)
	cmds = nil
	if err := e.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(shared); err == nil {
		t.Error("isolated site still in the shared master")
	}
	if _, err := os.Stat(e.Paths().PoolFile("8.3", "mine")); err != nil {
		t.Error("other account's site left the shared master")
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	inst := filepath.Join(e.Cfg.PHP.IsolatedDir, "abc123-8.3")
	if !strings.Contains(read(filepath.Join(inst, "berth-shop.conf")), "[berth-shop]") ||
		!strings.Contains(read(filepath.Join(inst, "php-fpm.conf")), "include = "+inst+"/berth-*.conf") ||
		!strings.Contains(read(filepath.Join(inst, "env")), "php-fpm8.3") {
		t.Fatal("isolated master files")
	}
	slice := read(filepath.Join(e.Cfg.SystemdDir, "berth-acct-abc123.slice"))
	for _, want := range []string{"CPUQuota=150%", "MemoryMax=1024M", "TasksMax=200"} {
		if !strings.Contains(slice, want) {
			t.Errorf("slice lacks %s:\n%s", want, slice)
		}
	}
	if !strings.Contains(read(filepath.Join(e.Cfg.SystemdDir, "berth-fpm@abc123-8.2.service.d", "berth.conf")), "Slice=berth-acct-abc123.slice") {
		t.Error("drop-in")
	}
	if !strings.Contains(read(filepath.Join(e.Cfg.SystemdDir, "berth-fpm.target")), "berth-fpm@abc123-8.2.service berth-fpm@abc123-8.3.service") {
		t.Error("target")
	}
	tmpl := read(filepath.Join(e.Cfg.SystemdDir, "berth-fpm@.service"))
	if !strings.Contains(tmpl, "EnvironmentFile="+e.Cfg.PHP.IsolatedDir+"/%i/env") || strings.Contains(tmpl, "%!") {
		t.Errorf("template:\n%s", tmpl)
	}
	order := strings.Join(cmds, "\n")
	reload := strings.Index(order, "reload-or-restart php8.3-fpm")
	start := strings.Index(order, "systemctl start berth-fpm@abc123-8.3.service")
	if reload < 0 || start < 0 || reload > start || !strings.Contains(order, "systemctl daemon-reload") || !strings.Contains(order, "-t -y "+inst+"/php-fpm.conf") {
		t.Fatalf("commands:\n%s", order)
	}

	// Nothing changes: nothing runs.
	cmds = nil
	if err := e.Apply(ctx); err != nil || len(cmds) != 0 {
		t.Fatalf("idle apply ran %v (%v)", cmds, err)
	}

	// Dropping the plan stops the masters before the shared ones take the pools back.
	a.PlanID = ""
	_ = e.Store.SaveAccount(ctx, a)
	cmds = nil
	if err := e.Apply(ctx); err != nil {
		t.Fatal(err)
	}
	order = strings.Join(cmds, "\n")
	stop := strings.Index(order, "systemctl stop berth-fpm@abc123-8.3.service")
	reload = strings.Index(order, "reload-or-restart php8.3-fpm")
	if stop < 0 || reload < 0 || stop > reload {
		t.Fatalf("commands:\n%s", order)
	}
	if _, err := os.Stat(shared); err != nil {
		t.Error("pool not back in the shared master")
	}
	if left, _ := filepath.Glob(filepath.Join(e.Cfg.SystemdDir, "berth-*")); len(left) != 0 {
		t.Errorf("units left: %v", left)
	}
	if _, err := os.Stat(inst); err == nil {
		t.Error("instance dir left")
	}
}
