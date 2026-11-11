package billingexpr

import (
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// quotaConversion keeps the existing quota scale in the snapshot's native
// currency. Version 2 only declares currency; it does not change arithmetic.
func quotaConversion(exprOutput float64, snap *BillingSnapshot) float64 {
	if snap.TaskUsageBilling {
		return exprOutput * snap.QuotaPerUnit
	}
	switch snap.ExprVersion {
	default: // v1 and v2: coefficients are native currency per 1M tokens.
		return exprOutput / 1_000_000 * snap.QuotaPerUnit
	}
}

// ComputeTieredQuota runs the Expr from a frozen BillingSnapshot against
// actual token counts and returns the settlement result.
func ComputeTieredQuota(snap *BillingSnapshot, params TokenParams) (TieredResult, error) {
	return ComputeTieredQuotaWithRequest(snap, params, RequestInput{})
}

func ComputeTieredQuotaWithRequest(snap *BillingSnapshot, params TokenParams, request RequestInput) (TieredResult, error) {
	currency, err := ExprCurrency(snap.ExprString)
	if err != nil {
		return TieredResult{}, err
	}
	if currency != snap.BillingCurrency() {
		return TieredResult{}, fmt.Errorf("billing snapshot currency %q does not match expression currency %q", snap.BillingCurrency(), currency)
	}
	if snap.ExprVersion != 0 && snap.ExprVersion != ExprVersion(snap.ExprString) {
		return TieredResult{}, fmt.Errorf("billing snapshot version %d does not match expression version %d", snap.ExprVersion, ExprVersion(snap.ExprString))
	}
	if snap.TaskUsageBilling && UsesFixedPricingByHash(snap.ExprString, snap.ExprHash) {
		return TieredResult{}, fmt.Errorf("fixed pricing is not supported for task usage expressions")
	}
	cost, trace, err := RunExprByHashWithRequest(snap.ExprString, snap.ExprHash, params, request)
	if err != nil {
		return TieredResult{}, err
	}

	quotaBeforeGroup := quotaConversion(cost, snap)
	afterGroup, clamp := common.QuotaRoundChecked(quotaBeforeGroup * snap.EffectiveBillingRate())
	crossed := trace.MatchedTier != snap.EstimatedTier

	result := TieredResult{
		ImageCount:             trace.ImageCount,
		BillingUnit:            trace.BillingUnit,
		FixedPrice:             trace.FixedPrice,
		ActualQuotaBeforeGroup: quotaBeforeGroup,
		ActualQuotaAfterGroup:  afterGroup,
		MatchedTier:            trace.MatchedTier,
		RequestRules:           trace.RequestRules,
		CrossedTier:            crossed,
		Clamp:                  clamp,
	}
	if trace.BillingUnit == BillingUnitToken && UsedVarsByHash(snap.ExprString, snap.ExprHash)["img_cr"] {
		result.BillingTokens = &params
	}
	return result, nil
}
