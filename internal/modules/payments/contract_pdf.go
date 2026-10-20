package payments

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// buildContractPDF 生成不含敏感原文的静态 PDF。采用 PDF 内建的 Adobe GB1 CJK 字体，
// 使部署端无需依赖本机中文字体文件。
func buildContractPDF(lines []string) []byte {
	var pages [][]string
	for len(lines) > 0 {
		n := 34
		if len(lines) < n {
			n = len(lines)
		}
		pages, lines = append(pages, lines[:n]), lines[n:]
	}
	if len(pages) == 0 {
		pages = [][]string{{"授权合同"}}
	}
	objects := []string{"<< /Type /Catalog /Pages 2 0 R >>", "", "<< /Type /Font /Subtype /Type0 /BaseFont /STSong-Light /Encoding /UniGB-UCS2-H /DescendantFonts [4 0 R] >>", "<< /Type /Font /Subtype /CIDFontType0 /BaseFont /STSong-Light /CIDSystemInfo << /Registry (Adobe) /Ordering (GB1) /Supplement 2 >> >>"}
	pageIDs, contentIDs := make([]int, len(pages)), make([]int, len(pages))
	for i, page := range pages {
		content := pdfContent(page)
		contentIDs[i] = len(objects) + 1
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content))
		pageIDs[i] = len(objects) + 1
		objects = append(objects, fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 595 842] /Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentIDs[i]))
	}
	kids := make([]string, len(pageIDs))
	for i, id := range pageIDs {
		kids[i] = fmt.Sprintf("%d 0 R", id)
	}
	objects[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(kids))
	var out bytes.Buffer
	out.WriteString("%PDF-1.4\n%\xE2\xE3\xCF\xD3\n")
	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = out.Len()
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}
	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n0000000000 65535 f \n", len(objects)+1)
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&out, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return out.Bytes()
}

func pdfContent(lines []string) string {
	var b strings.Builder
	b.WriteString("BT\n/F1 15 Tf\n50 800 Td\n")
	for i, line := range lines {
		if i > 0 {
			b.WriteString("0 -21 Td\n/F1 10 Tf\n")
		}
		b.WriteString("<" + utf16Hex(line) + "> Tj\n")
	}
	b.WriteString("ET")
	return b.String()
}
func utf16Hex(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r > 0xFFFF {
			r = 0xFFFD
		}
		b.WriteString(fmt.Sprintf("%04X", r))
	}
	return b.String()
}
func contractLines(snapshot map[string]any, contractNo, authNo, orderNo, paymentNo, paidAt string) []string {
	buyer, _ := snapshot["buyer"].(map[string]any)
	ip, _ := snapshot["ip"].(map[string]any)
	scope, _ := snapshot["authorization"].(map[string]any)
	order, _ := snapshot["order"].(map[string]any)
	platform, _ := snapshot["platform"].(map[string]any)
	distribution, _ := snapshot["distribution"].(map[string]any)
	return []string{
		"星衍树 IP 授权合同",
		"合同编号：" + contractNo,
		"授权编号：" + authNo,
		"授权订单号：" + orderNo,
		"支付流水号：" + paymentNo,
		"支付时间：" + paidAt,
		"平台运营主体：" + text(platform["orgName"]) + "（" + text(platform["orgCode"]) + "）",
		"购买方显示名称：" + text(buyer["displayName"]),
		"购买方实名主体：" + text(buyer["legalName"]),
		"主体类型：" + text(buyer["subjectType"]),
		"认证证件：" + text(buyer["credentialType"]) + " " + text(buyer["credentialNoMasked"]),
		"实名核验编号：" + text(buyer["verificationNo"]),
		"IP：" + text(ip["ipName"]) + "（" + text(ip["ipNo"]) + "）",
		"IP所属城市：" + text(ip["cityOrgName"]),
		"权利人：" + text(ip["rightHolderNameMasked"]) + "；取得方式：" + text(ip["acquisitionMethod"]),
		"授权方案：" + text(scope["planName"]),
		"授权方式：" + text(scope["authorizationMode"]),
		"授权期限：" + termText(scope),
		"授权地区：" + text(scope["regionMode"]) + " " + listText(scope["regionCodes"]),
		"使用场景：" + listText(scope["scenes"]),
		"内容类型：" + text(scope["contentType"]),
		"商业化/二创/N创：" + boolText(scope["commercialAllowed"]) + "/" + boolText(scope["derivativeAllowed"]) + "/" + boolText(scope["nCreationAllowed"]),
		"分配规则：" + text(distribution["name"]) + "（" + text(distribution["type"]) + "）",
		"合同金额：" + text(order["amount"]) + " " + text(order["currency"]),
		"权利说明：" + text(ip["rightsNotice"]),
		"购买须知：" + text(ip["purchaseNotice"]),
		"合同自支付成功时生效，授权范围以本合同所载订单内容为准。",
		"未经许可不得超出授权地区、场景、期限或内容类型使用。",
		"违约、侵权及争议处理按照购买时同意的平台合同条款执行。",
		"本 PDF 根据不可修改的授权订单内容生成；文件哈希及存证回执以平台购买记录为准。",
	}
}

func certificateLines(snapshot map[string]any, certificateNo, authNo, orderNo, paidAt string) []string {
	buyer, _ := snapshot["buyer"].(map[string]any)
	ip, _ := snapshot["ip"].(map[string]any)
	scope, _ := snapshot["authorization"].(map[string]any)
	return []string{"星衍树 IP 授权凭证", "凭证编号：" + certificateNo, "授权编号：" + authNo, "授权订单号：" + orderNo, "授权生效时间：" + paidAt, "被授权主体：" + text(buyer["legalName"]), "主体类型：" + text(buyer["subjectType"]), "IP：" + text(ip["ipName"]) + "（" + text(ip["ipNo"]) + "）", "授权方式：" + text(scope["authorizationMode"]), "授权期限：" + termText(scope), "授权地区：" + text(scope["regionMode"]) + " " + listText(scope["regionCodes"]), "使用场景：" + listText(scope["scenes"]), "内容类型：" + text(scope["contentType"]), "本凭证须与正式授权合同及平台购买记录一并核验。"}
}

func termText(scope map[string]any) string {
	if text(scope["termType"]) == "DATE_RANGE" {
		return text(scope["validFrom"]) + " 至 " + text(scope["validTo"])
	}
	return text(scope["termType"]) + " " + text(scope["termValue"])
}

func listText(value any) string {
	items, ok := value.([]any)
	if !ok {
		return text(value)
	}
	values := make([]string, 0, len(items))
	for _, item := range items {
		values = append(values, text(item))
	}
	return strings.Join(values, "、")
}

func boolText(value any) string {
	if allowed, ok := value.(bool); ok && allowed {
		return "允许"
	}
	return "不允许"
}
func text(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		return fmt.Sprint(x)
	}
}
