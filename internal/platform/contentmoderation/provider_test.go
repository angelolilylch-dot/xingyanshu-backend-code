package contentmoderation

import (
	"context"
	"testing"

	"xingyanshu-backend/internal/config"
)

func TestMockProviderModeratesProductImage(t *testing.T) {
	provider := NewProvider(config.ContentSafetyConfig{Provider: "mock"})
	imageProvider, ok := provider.(ImageProvider)
	if !ok {
		t.Fatal("mock provider must implement image moderation")
	}
	result, err := imageProvider.ModerateImage(context.Background(), ImageRequest{URL: "https://example.invalid/product.png", DataID: "product-image-1"})
	if err != nil || result.Provider != "MOCK" || result.Decision != DecisionPass || result.ProviderRequestNo == "" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}
