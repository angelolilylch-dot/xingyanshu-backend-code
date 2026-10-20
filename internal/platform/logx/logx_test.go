package logx

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInitWritesDailyTextFilesAndRedactsSecrets(t *testing.T) {
	directory := t.TempDir()
	cleanup, err := Init(Settings{Directory: directory, RetentionDays: 7, Component: "server"})
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	Errorf(`request_body={"mobile":"13800138000","password":"plain","sms_code":"123456"}`)
	cleanup()

	path := filepath.Join(directory, "server-error-"+time.Now().Format("2006-01-02")+".log")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read error log: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "13800138000") || !strings.Contains(text, `"password":"***"`) || !strings.Contains(text, `"sms_code":"***"`) {
		t.Fatalf("unexpected diagnostic log: %s", text)
	}
}

func TestRedactKeepsPIIButRemovesCredentials(t *testing.T) {
	got := Redact(`mobile=13800138000 password=plain Authorization: Bearer-token mysql://root:pw@127.0.0.1/test`)
	for _, forbidden := range []string{"plain", "Bearer-token", ":pw@"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("secret leaked: %s", got)
		}
	}
	if !strings.Contains(got, "13800138000") {
		t.Fatalf("test PII should be retained: %s", got)
	}
	escaped := fmt.Sprintf("sql=%q", RedactSQL(`INSERT INTO account(password,sms_code) VALUES ('plain','123456')`))
	for _, forbidden := range []string{"'plain'", "'123456'"} {
		if strings.Contains(escaped, forbidden) {
			t.Fatalf("SQL secret leaked: %s", escaped)
		}
	}
}
