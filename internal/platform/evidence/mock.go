package evidence

import (
	"context"
	"fmt"
)

// MockProvider 仅用于测试环境，生成可验证的测试回执以完整演练存证成功流转。
type MockProvider struct{}

func (MockProvider) Submit(_ context.Context, request SubmitRequest) (SubmitResult, error) {
	return SubmitResult{Provider: "MOCK_EVIDENCE", RequestNo: request.RequestNo, Status: "SUCCESS", ReceiptNo: fmt.Sprintf("MOCK-RCPT-%s", request.RequestNo)}, nil
}

func (MockProvider) Query(_ context.Context, requestNo string) (SubmitResult, error) {
	return SubmitResult{Provider: "MOCK_EVIDENCE", RequestNo: requestNo, Status: "PENDING_CHAIN"}, nil
}
