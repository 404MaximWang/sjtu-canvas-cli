package mlearning

import (
	"errors"
	"testing"
)

// TestValidateForscanURL pins the acceptance rules of scanned QR URLs:
// https scheme, exact mlearning host, forscan path prefix, and both
// roll-call parameters non-empty.
func TestValidateForscanURL(t *testing.T) {
	const valid = "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=tok123&signHistoryId=456"
	cases := []struct {
		name    string
		url     string
		wantErr bool
	}{
		{"valid", valid, false},
		{"trailing path segment", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/extra?rollCallToken=t&signHistoryId=1", false},
		{"extra query parameter", valid + "&from=qr", false},
		{"uppercase host", "https://MLEARNING.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", false},
		{"http scheme", "http://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", true},
		{"wrong host", "https://oc.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", true},
		{"host suffix trick", "https://mlearning.sjtu.edu.cn.evil.example/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=1", true},
		{"wrong path", "https://mlearning.sjtu.edu.cn/lms/mobile2/other/?rollCallToken=t&signHistoryId=1", true},
		{"path without trailing slash", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan?rollCallToken=t&signHistoryId=1", true},
		{"missing rollCallToken", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?signHistoryId=1", true},
		{"empty signHistoryId", "https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=t&signHistoryId=", true},
		{"not a URL", "not a url", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			token, historyID, err := ValidateForscanURL(tc.url)
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidURL) {
					t.Fatalf("want ErrInvalidURL, got %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if token == "" || historyID == "" {
				t.Fatalf("extracted empty parameters: %q %q", token, historyID)
			}
		})
	}
}

// TestValidateForscanURLExtraction pins the exact parameter values a valid
// URL yields, including percent-decoding.
func TestValidateForscanURLExtraction(t *testing.T) {
	token, historyID, err := ValidateForscanURL(
		"https://mlearning.sjtu.edu.cn/lms/mobile2/forscan/?rollCallToken=a%2Fb&signHistoryId=42")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if token != "a/b" || historyID != "42" {
		t.Fatalf("got %q %q", token, historyID)
	}
}

// TestCleanToken pins the cookie-value normalization: URL-unquote, strip
// surrounding quotes, reject empty results.
func TestCleanToken(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		wantErr bool
	}{
		{"plain JWT", "eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig", "eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig", false},
		{"quoted and escaped", "%22eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig%22", "eyJhbGciOiJ9.eyJzdWIiOiJ4In0.sig", false},
		{"bare quotes", `"tok"`, "tok", false},
		{"bad escape", "%zz", "", true},
		{"empty", `""`, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cleanToken(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}
