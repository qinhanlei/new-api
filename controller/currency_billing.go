package controller

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func requireCNYCurrency(c *gin.Context) bool {
	if c.Param("currency") != "CNY" {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Unsupported billing currency"))
		return false
	}
	return true
}

func currencyResourceID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		common.ApiErrorStatus(c, http.StatusBadRequest, common.NewMessage("Invalid parameters"))
		return 0, false
	}
	return id, true
}

func respondCurrencyBillingError(c *gin.Context, err error) {
	status, message := http.StatusInternalServerError, "Failed to process CNY billing"
	switch {
	case errors.Is(err, model.ErrCNYInvalid), errors.Is(err, model.ErrWalletQuotaLimitExceeded):
		status, message = http.StatusBadRequest, "Invalid CNY billing parameters"
	case errors.Is(err, model.ErrCNYConflict):
		status, message = http.StatusConflict, "CNY billing operation conflicts with its existing record"
	case errors.Is(err, model.ErrCNYVersion):
		status, message = http.StatusConflict, "CNY token budget has changed; reload before saving"
	case errors.Is(err, model.ErrCNYQuota):
		status, message = http.StatusBadRequest, "Insufficient CNY wallet quota"
	case errors.Is(err, model.ErrCNYTokenQuota):
		status, message = http.StatusBadRequest, "CNY token budget is disabled or insufficient"
	case errors.Is(err, model.ErrCNYState):
		status, message = http.StatusConflict, "CNY billing record is already final"
	case errors.Is(err, model.ErrUserQuotaPermission):
		status, message = http.StatusForbidden, "Permission denied"
	case errors.Is(err, gorm.ErrRecordNotFound):
		status, message = http.StatusNotFound, "Record not found"
	}
	common.ApiErrorStatus(c, status, common.NewMessage(message))
}

func GetSelfCurrencyWallet(c *gin.Context) {
	if !requireCNYCurrency(c) {
		return
	}
	wallet, err := model.GetCNYWallet(c.GetInt("id"))
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	common.ApiSuccess(c, wallet)
}

func GetUserCurrencyWallet(c *gin.Context) {
	if !requireCNYCurrency(c) {
		return
	}
	id, ok := currencyResourceID(c)
	if !ok {
		return
	}
	user, err := model.GetUserById(id, false)
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	role := c.GetInt("role")
	if role < common.RoleAdminUser || !canManageTargetRole(role, user.Role) {
		respondCurrencyBillingError(c, model.ErrUserQuotaPermission)
		return
	}
	wallet, err := model.GetCNYWallet(id)
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	common.ApiSuccess(c, wallet)
}

func AdjustUserCurrencyWallet(c *gin.Context) {
	if !requireCNYCurrency(c) {
		return
	}
	id, ok := currencyResourceID(c)
	if !ok {
		return
	}
	var request struct {
		DeltaQuota  *int    `json:"delta_quota"`
		Mode        *string `json:"mode"`
		Value       *int    `json:"value"`
		OperationID string  `json:"operation_id"`
		Reason      string  `json:"reason"`
	}
	params := model.AuditFields{"target_user_id": id, "currency": "CNY", "quota_per_unit": model.CNYQuotaPerUnit}
	success := false
	defer func() {
		model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "CNY wallet adjustment", c.ClientIP(), "user.cny_wallet_adjust", params,
			auditOperatorInfo(c), &model.AuditRequestInfo{Method: c.Request.Method, Route: c.FullPath(), Status: c.Writer.Status(), Success: success}, c)
		markAuditLogged(c)
	}()
	if err := c.ShouldBindJSON(&request); err != nil {
		respondCurrencyBillingError(c, model.ErrCNYInvalid)
		return
	}
	var adjustment *model.CNYBillingRecord
	var err error
	if request.DeltaQuota != nil {
		if request.Mode != nil || request.Value != nil {
			respondCurrencyBillingError(c, model.ErrCNYInvalid)
			return
		}
		params["delta_quota"] = *request.DeltaQuota
		adjustment, err = model.AdjustCNYWallet(id, c.GetInt("id"), c.GetInt("role"), *request.DeltaQuota, request.OperationID, request.Reason)
	} else {
		if request.Mode == nil || request.Value == nil {
			respondCurrencyBillingError(c, model.ErrCNYInvalid)
			return
		}
		params["mode"], params["requested_quota"] = *request.Mode, *request.Value
		adjustment, err = model.AdjustCNYWalletByMode(id, c.GetInt("id"), c.GetInt("role"), *request.Mode, *request.Value, request.OperationID, request.Reason)
	}
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	params["operation_id"], params["target_username"] = adjustment.RequestID, adjustment.Username
	params["delta_quota"] = adjustment.DeltaQuota
	params["from"], params["to"] = adjustment.BeforeQuota, adjustment.AfterQuota
	success = true
	wallet, err := model.GetCNYWallet(id)
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	common.ApiSuccess(c, wallet)
}

func GetTokenCurrencyBudget(c *gin.Context) {
	if !requireCNYCurrency(c) {
		return
	}
	id, ok := currencyResourceID(c)
	if !ok {
		return
	}
	budget, err := model.GetCNYTokenBudget(id, c.GetInt("id"))
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	common.ApiSuccess(c, budget)
}

func UpdateTokenCurrencyBudget(c *gin.Context) {
	if !requireCNYCurrency(c) {
		return
	}
	id, ok := currencyResourceID(c)
	if !ok {
		return
	}
	var request struct {
		Enabled         *bool  `json:"enabled"`
		RemainQuota     *int   `json:"remain_quota"`
		UnlimitedQuota  *bool  `json:"unlimited_quota"`
		ExpectedVersion *int64 `json:"expected_version"`
	}
	params := model.AuditFields{"id": id, "currency": "CNY", "quota_per_unit": model.CNYQuotaPerUnit}
	success := false
	defer func() {
		model.RecordOperationAuditLog(c.GetInt("id"), c.GetInt("role"), "API token configuration update", c.ClientIP(), "token.cny_budget_update", params, nil,
			&model.AuditRequestInfo{Method: c.Request.Method, Route: c.FullPath(), Status: c.Writer.Status(), Success: success}, c)
		markAuditLogged(c)
	}()
	if err := c.ShouldBindJSON(&request); err != nil || request.Enabled == nil || request.RemainQuota == nil || request.UnlimitedQuota == nil || request.ExpectedVersion == nil {
		respondCurrencyBillingError(c, model.ErrCNYInvalid)
		return
	}
	budget, err := model.UpdateCNYTokenBudget(id, c.GetInt("id"), model.CNYTokenBudgetChange{Enabled: *request.Enabled, RemainQuota: *request.RemainQuota,
		UnlimitedQuota: *request.UnlimitedQuota, ExpectedVersion: *request.ExpectedVersion})
	if err != nil {
		respondCurrencyBillingError(c, err)
		return
	}
	params["enabled"], params["remain_quota"], params["unlimited_quota"], params["version"] = budget.Enabled, budget.RemainQuota, budget.UnlimitedQuota, budget.Version
	success = true
	common.ApiSuccess(c, budget)
}
