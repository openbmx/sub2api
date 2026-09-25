//go:build unit

package provider

import "testing"

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
}
