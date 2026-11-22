package plugin

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/joshuadouch/berth/internal/store"
)

type fake struct{ tested Config }

func (f *fake) Info() Info {
	return Info{ID: "fake", Name: "Fake", Fields: []Field{
		{Key: "url", Type: Text, Required: true, Label: "URL"},
		{Key: "key", Type: Secret, Label: "Key"},
		{Key: "on", Type: Bool, Default: true},
		{Key: "n", Type: Number, Default: 5},
		{Key: "mode", Type: Select, Default: "a", Options: []Option{{Value: "a"}, {Value: "b"}}},
	}}
}
func (f *fake) Init(*Host) http.Handler { return http.NotFoundHandler() }
func (f *fake) Test(_ context.Context, c Config) (string, error) {
	f.tested = c
	return "ok", nil
}

func TestRegistry(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	f := &fake{}
	r := NewRegistry(st, nil, f)
	ctx := context.Background()

	v, err := r.View(ctx, "fake")
	if err != nil || v.Enabled || v.Config.Int("n") != 5 || !v.Config.Bool("on") || !v.CanTest {
		t.Fatal(v, err)
	}
	if _, err := r.Save(ctx, "fake", true, Config{}); err == nil {
		t.Fatal("required field missing should fail")
	}
	if _, err := r.Save(ctx, "fake", false, Config{"mode": "zzz"}); err == nil {
		t.Fatal("bad select accepted")
	}
	v, err = r.Save(ctx, "fake", true, Config{"url": " https://x ", "key": "s3cret", "n": "7", "on": false, "junk": 1})
	if err != nil {
		t.Fatal(err)
	}
	if _, leaked := v.Config["key"]; leaked || !v.SecretsSet["key"] {
		t.Fatal("secret must be hidden but marked set", v)
	}
	if _, ok := v.Config["junk"]; ok || v.Config.Str("url") != "https://x" || v.Config.Int("n") != 7 || v.Config.Bool("on") {
		t.Fatal(v.Config)
	}
	// Blank secret keeps the saved one; Test sees it.
	if _, err := r.Test(ctx, "fake", Config{"key": ""}); err != nil || f.tested.Str("key") != "s3cret" {
		t.Fatal(f.tested, err)
	}
	if _, err := r.Save(ctx, "fake", true, Config{"key": "-"}); err != nil {
		t.Fatal(err)
	}
	if v, _ = r.View(ctx, "fake"); v.SecretsSet["key"] || v.Config.Str("url") != "https://x" {
		t.Fatal("'-' should clear the secret and keep the rest", v)
	}
	if !r.Enabled(ctx, "fake") || r.Enabled(ctx, "nope") {
		t.Fatal("Enabled")
	}
	if _, err := r.View(ctx, "nope"); !errors.Is(err, ErrUnknown) {
		t.Fatal(err)
	}
}

type perAccount struct{ fake }

func (*perAccount) Info() Info { return Info{ID: "pa", Name: "PA", PerAccount: true} }

func TestActiveFor(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	r := NewRegistry(st, nil, &perAccount{}, &fake{})
	if r.ActiveFor(ctx, "pa", "a1") {
		t.Fatal("off plugin active")
	}
	_, _ = r.Save(ctx, "pa", true, nil)
	if r.ActiveFor(ctx, "pa", "a1") {
		t.Fatal("active without accounts")
	}
	if err := r.SetAccounts(ctx, "pa", []string{"a1", "a1", ""}); err != nil {
		t.Fatal(err)
	}
	if !r.ActiveFor(ctx, "pa", "a1") || r.ActiveFor(ctx, "pa", "a2") {
		t.Fatal("accounts not applied")
	}
	// Saving settings keeps the accounts.
	_, _ = r.Save(ctx, "pa", true, Config{})
	if v, _ := r.View(ctx, "pa"); len(v.Accounts) != 1 {
		t.Fatalf("accounts %v", v.Accounts)
	}
	if err := r.SetAccounts(ctx, "fake", []string{"a1"}); err == nil {
		t.Fatal("server-wide plugin took accounts")
	}
}
