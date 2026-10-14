package httpxotel

import (
	"net/http"
	"testing"
)

func TestTransport_Defaults(t *testing.T) {
	tr := Transport()
	if tr == nil {
		t.Fatal("Transport() returned nil")
	}
	if tr.MaxIdleConns != 100 {
		t.Errorf("MaxIdleConns = %d, want 100", tr.MaxIdleConns)
	}
	if tr.IdleConnTimeout.Seconds() != 90 {
		t.Errorf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}
}

func TestWrapTransport_WrapsRoundTripper(t *testing.T) {
	base := http.DefaultTransport
	wrapped := WrapTransport(base)
	if wrapped == nil {
		t.Fatal("WrapTransport returned nil")
	}
	// 包装后应不再是原 RoundTripper
	if wrapped == base {
		t.Error("WrapTransport returned the same RoundTripper (not wrapped)")
	}
}
