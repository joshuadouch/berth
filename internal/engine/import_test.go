package engine

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeTarGz(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.WriteHeader(&tar.Header{Name: "home/user/public_html/evil", Linkname: "/etc/passwd", Typeflag: tar.TypeSymlink})
	_ = tw.Close()
	_ = gz.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestExtractAndFindRoot(t *testing.T) {
	dir := t.TempDir()
	// A cPanel-style account backup: the site is in public_html, the dump beside it.
	arc := filepath.Join(dir, "site.tar.gz")
	writeTarGz(t, arc, map[string]string{
		"home/user/public_html/index.php":   "<?php",
		"home/user/public_html/css/app.css": "body{}",
		"home/user/db.sql":                  "CREATE TABLE x (id int);",
		"home/user/../../escape.txt":        "no",
	})
	stage := filepath.Join(dir, "stage")
	_ = os.MkdirAll(stage, 0o755)
	n, err := extractArchive(arc, stage)
	if err != nil || n != 3 {
		t.Fatalf("extract %d %v", n, err)
	}
	if _, err := os.Lstat(filepath.Join(stage, "home/user/public_html/evil")); err == nil {
		t.Fatal("symlink extracted")
	}
	if _, err := os.Stat(filepath.Join(dir, "escape.txt")); err == nil {
		t.Fatal("escaped the stage")
	}
	root, err := findWebRoot(stage, false)
	if err != nil || !strings.HasSuffix(root, "public_html") {
		t.Fatalf("root %s %v", root, err)
	}
	if d := findDump(stage, root); !strings.HasSuffix(d, "db.sql") {
		t.Fatalf("dump %q", d)
	}

	// A zip of a WordPress folder.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range []string{"backup/wordpress/wp-load.php", "backup/wordpress/wp-config.php", "backup/readme.txt"} {
		w, _ := zw.Create(n)
		_, _ = w.Write([]byte("x"))
	}
	_ = zw.Close()
	zp := filepath.Join(dir, "wp.zip")
	_ = os.WriteFile(zp, buf.Bytes(), 0o600)
	stage2 := filepath.Join(dir, "stage2")
	_ = os.MkdirAll(stage2, 0o755)
	if _, err := extractArchive(zp, stage2); err != nil {
		t.Fatal(err)
	}
	root, err = findWebRoot(stage2, true)
	if err != nil || !strings.HasSuffix(root, "backup/wordpress") {
		t.Fatalf("wp root %s %v", root, err)
	}
	if _, err := findWebRoot(stage, true); err == nil {
		t.Fatal("found WordPress where there is none")
	}
}

func TestFilterDump(t *testing.T) {
	in := "CREATE DATABASE /*!32312 IF NOT EXISTS*/ `old` /*!40100 DEFAULT CHARACTER SET utf8mb4 */;\n" +
		"USE `old`;\n" +
		"CREATE TABLE `wp_x` (`a` text) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;\n" +
		"/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER t BEFORE INSERT ON x FOR EACH ROW SET @a=1 */;;\n" +
		"/*!50013 DEFINER=`admin`@`%` SQL SECURITY DEFINER */\n" +
		"INSERT INTO `wp_x` VALUES ('USE `old`; DEFINER=`root`@`localhost`');\n"
	var out bytes.Buffer
	if err := filterDump(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, bad := range []string{"\nUSE `old`;", "\nCREATE DATABASE", "utf8mb4_0900", "DEFINER=`root`@`localhost`*/ /*!50003", "SQL SECURITY DEFINER"} {
		if strings.Contains(got, bad) {
			t.Errorf("still has %q:\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "utf8mb4_unicode_ci") || !strings.Contains(got, "TRIGGER t BEFORE") || !strings.Contains(got, "DEFINER=`root`@`localhost`');") {
		t.Errorf("lost content:\n%s", got)
	}
}
