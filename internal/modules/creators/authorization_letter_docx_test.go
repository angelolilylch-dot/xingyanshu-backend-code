package creators

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
	"time"
)

func TestBuildAuthorizationLetterDOCXIncludesVerifiedEnterpriseFields(t *testing.T) {
	content, err := buildAuthorizationLetterDOCX(authorizationLetterDraftContent{LegalName: "星衍树文化创意有限公司", CredentialNo: "91350100TEST000001", SubjectType: "ENTERPRISE", GeneratedDate: time.Date(2026, 9, 20, 0, 0, 0, 0, time.Local)})
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(content), int64(len(content)))
	if err != nil {
		t.Fatalf("generated file is not a docx zip: %v", err)
	}
	parts := map[string]string{}
	for _, file := range reader.File {
		stream, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		value, err := io.ReadAll(stream)
		stream.Close()
		if err != nil {
			t.Fatal(err)
		}
		parts[file.Name] = string(value)
	}
	for _, required := range []string{"[Content_Types].xml", "_rels/.rels", "word/document.xml"} {
		if parts[required] == "" {
			t.Fatalf("missing docx part %s", required)
		}
	}
	for _, expected := range []string{"星衍树文化创意有限公司", "91350100TEST000001", "企业", "2026年09月20日"} {
		if !strings.Contains(parts["word/document.xml"], expected) {
			t.Fatalf("document.xml missing %q", expected)
		}
	}
}
