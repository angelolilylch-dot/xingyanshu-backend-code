package payments

import (
	"bytes"
	"os"
	"testing"
	"time"
)

func TestBuildContractPDFStructure(t *testing.T) {
	snapshot := map[string]any{
		"platform":      map[string]any{"orgName": "星衍树测试平台", "orgCode": "PLATFORM"},
		"buyer":         map[string]any{"displayName": "测试创作者", "legalName": "测试主体", "subjectType": "PERSONAL", "credentialType": "ID_CARD", "credentialNoMasked": "**************1234", "verificationNo": "IDV-1"},
		"ip":            map[string]any{"ipName": "洛阳桥文化记忆", "ipNo": "IP-001", "cityOrgName": "泉州", "rightHolderNameMasked": "泉***", "acquisitionMethod": "ORIGINAL", "rightsNotice": "权利说明", "purchaseNotice": "购买须知"},
		"authorization": map[string]any{"planName": "普通授权", "authorizationMode": "NORMAL_SHARE", "termType": "MONTH", "termValue": float64(12), "regionMode": "NATIONWIDE", "regionCodes": []any{}, "scenes": []any{"PRODUCT_SALE"}, "contentType": "PRODUCT_ONLY", "commercialAllowed": true, "derivativeAllowed": true, "nCreationAllowed": false},
		"distribution":  map[string]any{"name": "普通分账", "type": "NORMAL_SHARE"},
		"order":         map[string]any{"amount": "199.00", "currency": "CNY"},
	}
	pdf := buildContractPDF(contractLines(snapshot, "CONTRACT-1", "AUTHZ-1", "AUTH-1", "MOCKPAY-1", time.Now().Format(time.RFC3339)))
	if !bytes.HasPrefix(pdf, []byte("%PDF-1.4")) || !bytes.Contains(pdf, []byte("xref")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatalf("invalid PDF structure, bytes=%d", len(pdf))
	}
	if path := os.Getenv("XINGYANSHU_PDF_QA_PATH"); path != "" {
		if err := os.WriteFile(path, pdf, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCalculatePeriodSupportsDateRange(t *testing.T) {
	paidAt := time.Date(2026, 9, 23, 10, 0, 0, 0, time.Local)
	start, end := calculatePeriod(paidAt, map[string]any{"termType": "DATE_RANGE", "validFrom": "2026-10-01", "validTo": "2026-12-31"})
	if start.Format("2006-01-02") != "2026-10-01" || end.Format("2006-01-02") != "2026-12-31" || end.Hour() != 23 {
		t.Fatalf("unexpected period: %s - %s", start, end)
	}
}
