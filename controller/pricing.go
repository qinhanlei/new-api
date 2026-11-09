package controller

import (
	"maps"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"

	"github.com/gin-gonic/gin"
)

func filterPricingByUsableGroups(pricing []model.Pricing, usableGroup map[string]string) []model.Pricing {
	if len(pricing) == 0 {
		return pricing
	}
	if len(usableGroup) == 0 {
		return []model.Pricing{}
	}

	filtered := make([]model.Pricing, 0, len(pricing))
	for _, item := range pricing {
		if common.StringsContains(item.EnableGroup, "all") {
			filtered = append(filtered, item)
			continue
		}
		for _, group := range item.EnableGroup {
			if _, ok := usableGroup[group]; ok {
				filtered = append(filtered, item)
				break
			}
		}
	}
	return filtered
}

func GetPricing(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	pricing := model.GetPricing()
	userId, exists := c.Get("id")
	currentUserID := 0
	usableGroup := map[string]string{}
	groupRatio := map[string]float64{}
	maps.Copy(groupRatio, ratio_setting.GetGroupRatioCopy())
	var group string
	if exists {
		currentUserID = userId.(int)
		user, err := model.GetUserCache(currentUserID)
		if err != nil {
			common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Unable to load billing rates"))
			return
		}
		group = user.Group
		for g := range groupRatio {
			ratio, ok := ratio_setting.GetGroupGroupRatio(group, g)
			if ok {
				groupRatio[g] = ratio
			}
		}
	}

	usableGroup = service.GetUserUsableGroups(group)
	pricing = filterPricingByUsableGroups(pricing, usableGroup)
	// check groupRatio contains usableGroup
	for group := range ratio_setting.GetGroupRatioCopy() {
		if _, ok := usableGroup[group]; !ok {
			delete(groupRatio, group)
		}
	}
	modelNames := make([]string, 0, len(pricing))
	for _, item := range pricing {
		modelNames = append(modelNames, model.ResolveBillingModelName(item.ModelName))
	}
	rates, err := model.LoadBillingRateRules(currentUserID, modelNames)
	if err != nil {
		common.ApiErrorStatus(c, http.StatusInternalServerError, common.NewMessage("Unable to load billing rates"))
		return
	}
	// The model pricing cache is shared across users. Project each response into
	// fresh values instead of attaching private rates to cached model entries.
	type pricingWithBillingRates struct {
		model.Pricing
		BillingModelName string                                `json:"billing_model_name"`
		EffectiveRates   map[string]*types.BillingRateSnapshot `json:"effective_rates"`
	}
	personalPricing := make([]pricingWithBillingRates, 0, len(pricing))
	for index, item := range pricing {
		entry := pricingWithBillingRates{Pricing: item, BillingModelName: modelNames[index], EffectiveRates: make(map[string]*types.BillingRateSnapshot, len(groupRatio))}
		for name, ratio := range groupRatio {
			groupInfo := types.GroupRatioInfo{GroupRatio: ratio}
			if special, ok := ratio_setting.GetGroupGroupRatio(group, name); exists && ok {
				groupInfo.HasSpecialRatio = true
				groupInfo.GroupSpecialRatio = special
			}
			entry.EffectiveRates[name] = rates.Resolve(entry.BillingModelName, groupInfo)
		}
		personalPricing = append(personalPricing, entry)
	}

	c.JSON(200, gin.H{
		"success":            true,
		"data":               personalPricing,
		"vendors":            model.GetVendors(),
		"group_ratio":        groupRatio,
		"usable_group":       usableGroup,
		"supported_endpoint": model.GetSupportedEndpointMap(),
		"auto_groups":        service.GetUserAutoGroup(group),
		"pricing_version":    "a42d372ccf0b5dd13ecf71203521f9d2",
	})
}

func ResetModelRatio(c *gin.Context) {
	defaultStr := ratio_setting.DefaultModelRatio2JSONString()
	err := model.UpdateOption("ModelRatio", defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	err = ratio_setting.UpdateModelRatioByJSONString(defaultStr)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccessT(c, "Model ratios reset", nil)
}
