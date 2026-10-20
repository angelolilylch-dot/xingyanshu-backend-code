package creators

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
	"time"
)

const authorizationLetterDOCXContentType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"

type authorizationLetterDraftContent struct {
	LegalName     string
	CredentialNo  string
	SubjectType   string
	GeneratedDate time.Time
}

// buildAuthorizationLetterDOCX 生成最小、标准的 Office Open XML 文档包。
// 该文件是待签署草稿：企业名称、登记证号、主体类型和生成日期由服务端填充，签署信息留给用户补充。
func buildAuthorizationLetterDOCX(content authorizationLetterDraftContent) ([]byte, error) {
	var output bytes.Buffer
	writer := zip.NewWriter(&output)
	parts := map[string]string{
		"[Content_Types].xml": `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`,
		"_rels/.rels":         `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`,
		"word/document.xml":   authorizationLetterDocumentXML(content),
	}
	for name, value := range parts {
		entry, err := writer.Create(name)
		if err != nil {
			return nil, err
		}
		if _, err := entry.Write([]byte(value)); err != nil {
			return nil, err
		}
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func authorizationLetterDocumentXML(content authorizationLetterDraftContent) string {
	subject := map[string]string{"ENTERPRISE": "企业", "STUDIO": "工作室"}[content.SubjectType]
	if subject == "" {
		subject = content.SubjectType
	}
	lines := []string{
		"创作者主体授权委托书（草稿）",
		"",
		"委托方（主体名称）：" + content.LegalName,
		"统一社会信用代码/登记证号：" + content.CredentialNo,
		"主体类型：" + subject,
		"",
		"受托人姓名：________________________",
		"受托人证件类型及号码：________________________",
		"受托人联系电话：________________________",
		"",
		"委托事项：委托受托人代表委托方在星衍树文化版权运营平台办理创作者入驻、IP 授权购买及相关资料提交事项。",
		"授权期限：自____年__月__日起至____年__月__日止。",
		"",
		"委托方（盖章）：________________________",
		"法定代表人/负责人（签字）：________________________",
		"签署日期：____年__月__日",
		"",
		"本草稿生成日期：" + content.GeneratedDate.Format("2006年01月02日"),
	}
	paragraphs := make([]string, 0, len(lines))
	for index, line := range lines {
		alignment := "left"
		size := "24"
		bold := ""
		if index == 0 {
			alignment, size, bold = "center", "32", "<w:b/>"
		}
		paragraphs = append(paragraphs, fmt.Sprintf(`<w:p><w:pPr><w:jc w:val="%s"/><w:spacing w:after="160"/></w:pPr><w:r><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="SimSun"/><w:sz w:val="%s"/>%s</w:rPr><w:t xml:space="preserve">%s</w:t></w:r></w:p>`, alignment, size, bold, escapeWordXML(line)))
	}
	return `<?xml version="1.0" encoding="UTF-8" standalone="yes"?><w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + strings.Join(paragraphs, "") + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="720" w:footer="720" w:gutter="0"/></w:sectPr></w:body></w:document>`
}

func escapeWordXML(value string) string {
	var output bytes.Buffer
	_ = xml.EscapeText(&output, []byte(value))
	return output.String()
}
