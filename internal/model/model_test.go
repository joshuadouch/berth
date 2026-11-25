package model

import "testing"

func TestNameFromDomain(t *testing.T) {
	for in, want := range map[string]string{
		"www.example.com":           "examplecom",
		"blog.example.co.uk":        "blogexamplecouk",
		"123.example.com":           "examplecom",
		"a.io":                      "aio",
		"averyveryverylongname.com": "averyveryverylongnamecom"[:24],
	} {
		if got := NameFromDomain(in); got != want || !ValidName(got) {
			t.Errorf("NameFromDomain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSiteNormalize(t *testing.T) {
	ok := Site{Name: "blog", Type: TypeWordPress, Domain: "Blog.Example.com.", Aliases: []string{"www.blog.example.com", "blog.example.com", ""}, PHPVersion: "8.3"}
	if err := ok.Normalize(); err != nil {
		t.Fatal(err)
	}
	if ok.Domain != "blog.example.com" || len(ok.Aliases) != 1 || ok.UploadMB != 64 || ok.MemoryMB != 256 || ok.MaxChildren != 5 {
		t.Fatalf("defaults not applied: %+v", ok)
	}
	bad := []Site{
		{Name: "1abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3"},
		{Name: "ab-c", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3"},
		{Name: "abc", Type: "node", Domain: "a.com", PHPVersion: "8.3"},
		{Name: "abc", Type: TypePHP, Domain: "a .com", PHPVersion: "8.3"},
		{Name: "abc", Type: TypePHP, Domain: "*.a.com", PHPVersion: "8.3"},
		{Name: "abc", Type: TypePHP, Domain: "10.0.0.1", PHPVersion: "8.3"},
		{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "5.6"},
		{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3", DocRoot: "../etc"},
		{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3", DocRoot: "web/../../x"},
		{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3", DocRoot: "a b"},
		{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3", SFTP: SFTP{Keys: []string{"not a key"}}},
		{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3", SFTP: SFTP{Keys: []string{"ssh-ed25519 AAAA\nssh-rsa AAAA"}}},
	}
	for _, s := range bad {
		if err := s.Normalize(); err == nil {
			t.Errorf("expected an error for %+v", s)
		}
	}
	key := Site{Name: "abc", Type: TypePHP, Domain: "a.com", PHPVersion: "8.3", DocRoot: "/public/web/", SFTP: SFTP{Keys: []string{" ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMq josh@laptop "}}}
	if err := key.Normalize(); err != nil {
		t.Fatal(err)
	}
	if key.DocRoot != "public/web" || key.SFTP.Keys[0] != "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMq josh@laptop" {
		t.Fatalf("got %+v", key)
	}
}

func TestSettingsNormalize(t *testing.T) {
	st := Settings{TrustedProxies: []string{" 10.0.30.11 ", "10.0.40.0/24", ""}}
	if err := st.Normalize(); err != nil {
		t.Fatal(err)
	}
	if len(st.TrustedProxies) != 2 || st.BackupTime != "02:30" || st.BackupKeep != 7 || st.DefaultPHP == "" {
		t.Fatalf("defaults: %+v", st)
	}
	for _, bad := range []Settings{
		{TrustedProxies: []string{"proxy.lan"}},
		{ProxyOnly: true},
		{BackupTime: "25:00"},
		{BackupDir: "backups"},
	} {
		if err := bad.Normalize(); err == nil {
			t.Errorf("expected an error for %+v", bad)
		}
	}
}
