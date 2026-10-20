package products

import "testing"

func TestGeneratedSKUCodeIncludesIPWorkProductAndSerial(t *testing.T) {
	got := generatedSKUCode("IP-202609-00018", "WORK-202609-00007", "PRODUCT-123456789", 2)
	want := "SKU-IP-202609-00018-WORK-202609-00007-456789-02"
	if got != want {
		t.Fatalf("sku=%q want=%q", got, want)
	}
}

func TestInputValidationRequiresRegionForSpecifiedAndAllowedModes(t *testing.T) {
	base := Input{SKUs: []SKU{{SKUName: "规格", SalePrice: "1"}}, SaleRegionMode: "NATIONAL", FulfillmentMode: "CREATOR_SHIP", FreightMode: "FREE"}
	if !valid(base) {
		t.Fatal("expected national input valid")
	}
	base.SaleRegionMode = "SPECIFIED"
	if valid(base) {
		t.Fatal("specified region must require codes")
	}
	base.SaleRegionCodes = []string{"350500"}
	if !valid(base) {
		t.Fatal("specified region with code should be valid")
	}
}
