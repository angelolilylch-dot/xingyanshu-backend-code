package ips

import (
	"encoding/json"
	"testing"
)

func TestIPInputAcceptsStringDetailFileIDs(t *testing.T) {
	var input IPInput
	raw := []byte(`{
		"ipName":"Test",
		"ipType":"CHARACTER",
		"sourceType":"CITY_OPERATION_SOLICITATION",
		"mainCityOrgId":"1789351382102810300",
		"shortIntro":"简介",
		"detailContent":"详情",
		"cultureStory":"故事",
		"ipFeature":"特色",
		"creativeDirections":["方向"],
		"applicableProductTypes":["CULTURAL_CREATIVE"],
		"applicableActivityTypes":["EXHIBITION"],
		"publicCopyrightNotice":"版权说明",
		"purchaseNotice":"购买须知",
		"suggestedTransactionPriceAmount":"15555",
		"coverFileId":"1789459883988179200",
		"detailFileIds":["1789459886398697500"],
		"rightHolder":{"holderType":"PERSONAL","holderName":"权利人","acquisitionMethod":"TRANSFER","rightsDescription":"权属说明"},
		"rightDocuments":[{"documentType":"COPYRIGHT_REGISTRATION","documentNo":"125125125","validFrom":"2026-09-03","validTo":"2026-09-08","fileId":"1789459894089989100"}],
		"dciCertificateNo":"14551",
		"dciCertificateFileId":"1789459919044671200",
		"authPlanVersionId":"1789437325179021300",
		"distributionTemplateId":"1789377321358682600",
		"distributionItems":[{"participantType":"RIGHT_HOLDER","ratio":"0.99"}]
	}`)
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("unmarshal IP input: %v", err)
	}
	if len(input.DetailFileIDs) != 1 || input.DetailFileIDs[0] != 1789459886398697500 {
		t.Fatalf("unexpected detailFileIds: %#v", input.DetailFileIDs)
	}
}

func TestIPInputAcceptsDCIAssistanceWithCreateRequest(t *testing.T) {
	var input IPInput
	raw := []byte(`{
		"dciAssistanceRequested":true,
		"dciAssistanceRequest":{
			"contactName":"王小明",
			"contactMobile":"13800138000",
			"preferredContactTime":"工作日下午",
			"requestNote":"请协助办理 DCI 证书"
		}
	}`)
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatalf("unmarshal DCI assistance with IP input: %v", err)
	}
	if !input.DCIAssistanceRequested || input.DCIAssistanceRequest == nil || input.DCIAssistanceRequest.ContactMobile != "13800138000" {
		t.Fatalf("unexpected DCI assistance input: %#v", input)
	}
}
