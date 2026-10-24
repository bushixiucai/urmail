package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/bushixiucai/urmail/internal/store"
)

type platformCouponStoreStub struct {
	result string
	err    error
	args   store.PlatformRedeemCouponParams
}

func (s *platformCouponStoreStub) PlatformOverview(context.Context) (store.PlatformOverviewRow, error) {
	return store.PlatformOverviewRow{}, nil
}
func (s *platformCouponStoreStub) PlatformListTenants(context.Context) ([]store.PlatformListTenantsRow, error) {
	return nil, nil
}
func (s *platformCouponStoreStub) PlatformUpdateTenantStatus(context.Context, store.PlatformUpdateTenantStatusParams) (store.PlatformUpdateTenantStatusRow, error) {
	return store.PlatformUpdateTenantStatusRow{}, nil
}
func (s *platformCouponStoreStub) PlatformListBrands(context.Context) ([]store.PlatformListBrandsRow, error) {
	return nil, nil
}
func (s *platformCouponStoreStub) PlatformListPlans(context.Context) ([]store.Plans, error) {
	return nil, nil
}
func (s *platformCouponStoreStub) PlatformListCoupons(context.Context) ([]store.Coupons, error) {
	return nil, nil
}
func (s *platformCouponStoreStub) PlatformRedeemCoupon(_ context.Context, arg store.PlatformRedeemCouponParams) (string, error) {
	s.args = arg
	return s.result, s.err
}
func (s *platformCouponStoreStub) PlatformUpsertPlan(context.Context, store.PlatformUpsertPlanParams) (store.Plans, error) {
	return store.Plans{}, nil
}
func (s *platformCouponStoreStub) PlatformUpsertCoupon(context.Context, store.PlatformUpsertCouponParams) (store.Coupons, error) {
	return store.Coupons{}, nil
}
func (s *platformCouponStoreStub) PlatformUpsertBrand(context.Context, store.PlatformUpsertBrandParams) (store.PlatformUpsertBrandRow, error) {
	return store.PlatformUpsertBrandRow{}, nil
}
func (s *platformCouponStoreStub) PlatformListChannels(context.Context) ([]store.Channels, error) {
	return nil, nil
}
func (s *platformCouponStoreStub) PlatformUpsertChannel(context.Context, store.PlatformUpsertChannelParams) (store.Channels, error) {
	return store.Channels{}, nil
}
func (s *platformCouponStoreStub) PlatformListAuditLogs(context.Context, int32) ([]store.AuditLogs, error) {
	return nil, nil
}

func TestPlatformRedeemCouponMapsSafeOutcomes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, result string
		status       int
		body         string
	}{{"redeemed", "redeemed", 200, "redeemed"}, {"same retry", "already_redeemed", 200, "already_redeemed"}, {"exhausted", "exhausted", 409, "COUPON_EXHAUSTED"}, {"invalid", "invalid", 404, "COUPON_NOT_FOUND"}} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &platformCouponStoreStub{result: tc.result}
			router := gin.New()
			router.POST("/coupons/:code/redeem", NewPlatformHandler(stub).RedeemCoupon)
			req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/coupons/SAVE/redeem", strings.NewReader(`{"tenant_id":42,"order_id":9}`))
			req.Header.Set("Content-Type", "application/json")
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || !strings.Contains(w.Body.String(), tc.body) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
			}
			if stub.args.Code != "SAVE" || stub.args.TenantID != 42 || !stub.args.OrderID.Valid || stub.args.OrderID.Int64 != 9 {
				t.Fatalf("wrong args: %+v", stub.args)
			}
		})
	}
}

func TestPlatformRedeemCouponRejectsInvalidRequestBeforeStore(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &platformCouponStoreStub{}
	router := gin.New()
	router.POST("/coupons/:code/redeem", NewPlatformHandler(stub).RedeemCoupon)
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/coupons/SAVE/redeem", strings.NewReader(`{"tenant_id":42,"order_id":0}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if stub.args.TenantID != 0 || stub.args.OrderID != (pgtype.Int8{}) {
		t.Fatalf("store called unexpectedly: %+v", stub.args)
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
}
