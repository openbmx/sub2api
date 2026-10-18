//go:build unit

package provider

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/smartwalle/alipay/v3"
)

// A refund whose reply was lost is retried from refund_failed. Alipay only
// treats the retry as the same refund when out_request_no matches, so the number
// must be a pure function of the order and amount.
func TestAlipayRefundRequestNoIsDeterministic(t *testing.T) {
	first := alipayRefundRequestNo("20260925AbCd1234", "12.50")
	if first != "20260925AbCd1234-refund-1250" {
		t.Fatalf("unexpected out_request_no %q", first)
	}
	if again := alipayRefundRequestNo("20260925AbCd1234", "12.50"); again != first {
		t.Fatalf("retry produced %q, want %q", again, first)
	}
	if other := alipayRefundRequestNo("20260925AbCd1234", "5.00"); other == first {
		t.Fatal("a different refund amount must not reuse the request number")
	}
	if len(first) > 64 {
		t.Fatalf("out_request_no is limited to 64 characters, got %d", len(first))
	}
	if !strings.HasPrefix(first, "20260925AbCd1234"+alipayRefundRequestSep) {
		t.Fatalf("QueryRefund only accepts numbers prefixed with the order and %q, got %q", alipayRefundRequestSep, first)
	}
}

// The helper test above cannot see the call site: upstream v0.2.17 inlined a
// time.Now-based number back into Refund and it stayed green. This drives Refund
// itself — a retry after a lost reply must replay the same out_request_no,
// record it as the RefundID, and that RefundID must be what QueryRefund sends.
func TestAlipayRefundRetryReplaysRequestNoAndStaysQueryable(t *testing.T) {
	origRefund, origQuery := alipayTradeRefund, alipayTradeFastPayRefundQuery
	t.Cleanup(func() { alipayTradeRefund, alipayTradeFastPayRefundQuery = origRefund, origQuery })

	var sent []string
	alipayTradeRefund = func(_ context.Context, _ *alipay.Client, param alipay.TradeRefund) (*alipay.TradeRefundRsp, error) {
		sent = append(sent, param.OutRequestNo)
		if len(sent) == 1 {
			// The refund went through upstream; only the reply was lost.
			return nil, errors.New("read tcp: connection reset by peer")
		}
		return &alipay.TradeRefundRsp{FundChange: "N"}, nil
	}
	provider := &Alipay{client: &alipay.Client{}}
	req := payment.RefundRequest{OrderID: "sub2_7", Amount: "12.50", Reason: "r"}

	if _, err := provider.Refund(context.Background(), req); err == nil {
		t.Fatal("precondition: the first attempt must fail like a lost reply")
	}
	resp, err := provider.Refund(context.Background(), req)
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if len(sent) != 2 || sent[0] != sent[1] {
		t.Fatalf("the retry must replay the same out_request_no, sent %q", sent)
	}
	if sent[0] != alipayRefundRequestNo(req.OrderID, req.Amount) || resp.RefundID != sent[0] {
		t.Fatalf("RefundID = %q, sent %q", resp.RefundID, sent)
	}

	var queried string
	alipayTradeFastPayRefundQuery = func(_ context.Context, _ *alipay.Client, param alipay.TradeFastPayRefundQuery) (*alipay.TradeFastPayRefundQueryRsp, error) {
		queried = param.OutRequestNo
		return &alipay.TradeFastPayRefundQueryRsp{
			Error:        alipay.Error{Code: alipay.CodeSuccess, Msg: "Success"},
			OutRequestNo: param.OutRequestNo,
			RefundAmount: req.Amount,
			RefundStatus: alipayRefundStatusSucc,
		}, nil
	}
	got, err := provider.QueryRefund(context.Background(), payment.RefundQueryRequest{OrderID: req.OrderID, RefundID: resp.RefundID})
	if err != nil {
		t.Fatalf("QueryRefund rejected the recorded RefundID %q: %v", resp.RefundID, err)
	}
	if queried != resp.RefundID || got.Status != payment.ProviderStatusSuccess {
		t.Fatalf("queried %q, status %q", queried, got.Status)
	}
}
