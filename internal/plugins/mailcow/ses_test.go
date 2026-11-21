package mailcow

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
	"time"
)

// AWS's published SigV4 example (GET iam ListUsers).
func TestSignKnownAnswer(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://iam.amazonaws.com/?Action=ListUsers&Version=2010-05-08", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	ts := time.Date(2015, 8, 30, 12, 36, 0, 0, time.UTC)
	sign(req, nil, "AKIDEXAMPLE", "wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1", "iam", ts)
	want := "AWS4-HMAC-SHA256 Credential=AKIDEXAMPLE/20150830/us-east-1/iam/aws4_request, SignedHeaders=content-type;host;x-amz-date, Signature=5d672d79c15b13162d9279b0855cfba6789a8edb4c82c400e06b5924a6f2b5d7"
	if got := req.Header.Get("Authorization"); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

func TestSigningKey(t *testing.T) {
	k := signingKey("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "20150830", "us-east-1", "iam")
	if got := sha256HexKey(k); got != "c4afb1cc5771d871763a393e44b703571b55cc28424d1a5e86da6ed3c154a4b9" {
		t.Fatal(got)
	}
}

func sha256HexKey(b []byte) string {
	const hx = "0123456789abcdef"
	var sb strings.Builder
	for _, c := range b {
		sb.WriteByte(hx[c>>4])
		sb.WriteByte(hx[c&15])
	}
	return sb.String()
}

func TestSMTPPassword(t *testing.T) {
	p := SMTPPassword("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "eu-west-2")
	b, err := base64.StdEncoding.DecodeString(p)
	if err != nil || len(b) != 33 || b[0] != 4 {
		t.Fatalf("bad SMTP password %q", p)
	}
	if p == SMTPPassword("wJalrXUtnFEMI/K7MDENG+bPxRfiCYEXAMPLEKEY", "us-east-1") {
		t.Fatal("password must depend on the region")
	}
	if SMTPHost("eu-west-2") != "email-smtp.eu-west-2.amazonaws.com:587" {
		t.Fatal(SMTPHost("eu-west-2"))
	}
}
