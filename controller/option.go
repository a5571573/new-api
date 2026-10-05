package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/console_setting"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/setting/system_setting"

	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

var completionRatioMetaOptionKeys = []string{
	"ModelPrice",
	"ModelRatio",
	"CompletionRatio",
	"CacheRatio",
	"CreateCacheRatio",
	"ImageRatio",
	"AudioRatio",
	"AudioCompletionRatio",
}

func isPaymentComplianceOptionKey(key string) bool {
	return strings.HasPrefix(key, "payment_setting.compliance_")
}

func isPositiveOptionValue(value string) bool {
	intValue, err := strconv.Atoi(strings.TrimSpace(value))
	if err == nil {
		return intValue > 0
	}
	floatValue, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return err == nil && floatValue > 0
}

func collectModelNamesFromOptionValue(raw string, modelNames map[string]struct{}) {
	if strings.TrimSpace(raw) == "" {
		return
	}

	var parsed map[string]any
	if err := common.UnmarshalJsonStr(raw, &parsed); err != nil {
		return
	}

	for modelName := range parsed {
		modelNames[modelName] = struct{}{}
	}
}

func buildCompletionRatioMetaValue(optionValues map[string]string) string {
	modelNames := make(map[string]struct{})
	for _, key := range completionRatioMetaOptionKeys {
		collectModelNamesFromOptionValue(optionValues[key], modelNames)
	}

	meta := make(map[string]ratio_setting.CompletionRatioInfo, len(modelNames))
	for modelName := range modelNames {
		meta[modelName] = ratio_setting.GetCompletionRatioInfo(modelName)
	}

	jsonBytes, err := common.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(jsonBytes)
}

func GetOptions(c *gin.Context) {
	var options []*model.Option
	optionValues := make(map[string]string)
	common.OptionMapRWMutex.Lock()
	for k, v := range common.OptionMap {
		if k == "theme.frontend" || k == "billing_setting.billing_mode" || k == "billing_setting.billing_expr" {
			continue
		}
		value := common.Interface2String(v)
		isSensitiveKey := strings.HasSuffix(k, "Token") ||
			strings.HasSuffix(k, "Secret") ||
			strings.HasSuffix(k, "Key") ||
			strings.HasSuffix(k, "secret") ||
			strings.HasSuffix(k, "api_key")
		if isSensitiveKey {
			continue
		}
		options = append(options, &model.Option{
			Key:   k,
			Value: value,
		})
		if slices.Contains(completionRatioMetaOptionKeys, k) {
			optionValues[k] = value
		}
	}
	common.OptionMapRWMutex.Unlock()
	// Display the same effective expressions used by pricing and settlement,
	// including built-in defaults absent from persisted administrator options.
	for key, values := range map[string]map[string]string{
		"billing_setting.billing_mode": billing_setting.GetBillingModeCopy(),
		"billing_setting.billing_expr": billing_setting.GetBillingExprCopy(),
	} {
		encoded, err := common.Marshal(values)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": err.Error()})
			return
		}
		options = append(options, &model.Option{Key: key, Value: string(encoded)})
	}
	options = append(options, &model.Option{
		Key:   "CompletionRatioMeta",
		Value: buildCompletionRatioMetaValue(optionValues),
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data":    options,
	})
}

type OptionUpdateRequest struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
}

func UpdatePasskeyDomains(c *gin.Context) {
	var request struct {
		RPID                *string `json:"rp_id"`
		LegacyRPIDs         *string `json:"legacy_rp_ids"`
		Origins             *string `json:"origins"`
		Preview             bool    `json:"preview"`
		RemovalConfirmation string  `json:"removal_confirmation"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || request.RPID == nil || request.LegacyRPIDs == nil || request.Origins == nil {
		common.ApiErrorI18n(c, i18n.MsgInvalidParams)
		return
	}
	change, err := model.UpdatePasskeyDomainOptions(map[string]string{
		"passkey.rp_id": *request.RPID, "passkey.legacy_rp_ids": *request.LegacyRPIDs, "passkey.origins": *request.Origins,
	}, request.Preview, request.RemovalConfirmation)
	if err != nil {
		writePasskeyDomainSettingsError(c, err)
		if !request.Preview {
			recordPasskeyDomainAudit(c, change, request.RemovalConfirmation != "", err)
		}
		return
	}
	if !request.Preview {
		recordPasskeyDomainAudit(c, change, request.RemovalConfirmation != "", nil)
	}
	common.ApiSuccess(c, change)
}

func writePasskeyDomainSettingsError(c *gin.Context, err error) {
	var removal *model.PasskeyDomainRemovalError
	if errors.As(err, &removal) {
		c.JSON(http.StatusConflict, gin.H{
			"success": false, "code": "PASSKEY_RP_ID_REMOVAL_CONFIRMATION_REQUIRED",
			"message": i18n.T(c, i18n.MsgPasskeyRPIDRemovalConfirmation), "data": removal.Change,
		})
		return
	}
	if errors.Is(err, system_setting.ErrPasskeyRPIDInvalid) {
		writeSecurityOperationError(c, err)
		return
	}
	common.ApiError(c, err)
}

func UpdateOption(c *gin.Context) {
	var option OptionUpdateRequest
	err := common.DecodeJson(c.Request.Body, &option)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": "无效的参数",
		})
		return
	}
	switch option.Value.(type) {
	case bool:
		option.Value = common.Interface2String(option.Value.(bool))
	case float64:
		option.Value = common.Interface2String(option.Value.(float64))
	case int:
		option.Value = common.Interface2String(option.Value.(int))
	default:
		option.Value = fmt.Sprintf("%v", option.Value)
	}
	switch option.Key {
	case "QuotaForInviter", "QuotaForInvitee":
		if isPositiveOptionValue(option.Value.(string)) && !operation_setting.IsPaymentComplianceConfirmed() {
			common.ApiErrorI18n(c, i18n.MsgPaymentComplianceRequired)
			return
		}
	default:
		if isPaymentComplianceOptionKey(option.Key) {
			common.ApiErrorMsg(c, "合规确认字段不允许通过通用设置接口修改")
			return
		}
	}
	if option.Key == "TaskPublicAddress" && option.Value.(string) != "" {
		if err := service.ValidateTaskArtifactBaseURL(option.Value.(string)); err != nil {
			common.ApiErrorMsg(c, err.Error())
			return
		}
	}
	switch option.Key {
	case "GitHubOAuthEnabled":
		if option.Value == "true" && common.GitHubClientId == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用 GitHub OAuth，请先填入 GitHub Client Id 以及 GitHub Client Secret！",
			})
			return
		}
	case "discord.enabled":
		if option.Value == "true" && system_setting.GetDiscordSettings().ClientId == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用 Discord OAuth，请先填入 Discord Client Id 以及 Discord Client Secret！",
			})
			return
		}
	case "oidc.enabled":
		if option.Value == "true" && system_setting.GetOIDCSettings().ClientId == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用 OIDC 登录，请先填入 OIDC Client Id 以及 OIDC Client Secret！",
			})
			return
		}
	case "LinuxDOOAuthEnabled":
		if option.Value == "true" && common.LinuxDOClientId == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用 LinuxDO OAuth，请先填入 LinuxDO Client Id 以及 LinuxDO Client Secret！",
			})
			return
		}
	case "EmailDomainRestrictionEnabled":
		if option.Value == "true" && len(common.EmailDomainWhitelist) == 0 {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用邮箱域名限制，请先填入限制的邮箱域名！",
			})
			return
		}
	case "WeChatAuthEnabled":
		if option.Value == "true" && common.WeChatServerAddress == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用微信登录，请先填入微信登录相关配置信息！",
			})
			return
		}
	case "TurnstileCheckEnabled":
		if option.Value == "true" && common.TurnstileSiteKey == "" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "无法启用 Turnstile 校验，请先填入 Turnstile 校验相关配置信息！",
			})

			return
		}
	case "TelegramOAuthEnabled":
		if option.Value == "true" && !system_setting.GetTelegramSettings().IsConfigured() {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"code":    "TELEGRAM_OAUTH_NOT_CONFIGURED",
				"message": "Telegram OAuth is not configured or enabled. Please contact your administrator.",
			})
			return
		}
	case "theme.frontend":
		if option.Value != "default" {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "Classic 前端已移除，主题只能设置为 default",
			})
			return
		}
	case "GroupRatio":
		err = ratio_setting.CheckGroupRatio(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "gemini.safety_settings":
		err = model_setting.ValidateGeminiSafetySettings(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "claude.default_max_tokens":
		err = model_setting.ValidateClaudeDefaultMaxTokens(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case operation_setting.ToolPriceOptionKey:
		err = operation_setting.ValidateToolPricesJSON(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "ImageRatio":
		err = ratio_setting.UpdateImageRatioByJSONString(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "图片倍率设置失败: " + err.Error(),
			})
			return
		}
	case "AudioRatio":
		err = ratio_setting.UpdateAudioRatioByJSONString(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "音频倍率设置失败: " + err.Error(),
			})
			return
		}
	case "AudioCompletionRatio":
		err = ratio_setting.UpdateAudioCompletionRatioByJSONString(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "音频补全倍率设置失败: " + err.Error(),
			})
			return
		}
	case "CreateCacheRatio":
		err = ratio_setting.UpdateCreateCacheRatioByJSONString(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": "缓存创建倍率设置失败: " + err.Error(),
			})
			return
		}
	case "ModelRequestRateLimitGroup":
		err = setting.CheckModelRequestRateLimitGroup(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "AutomaticDisableStatusCodes":
		_, err = operation_setting.ParseHTTPStatusCodeRanges(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "AutomaticRetryStatusCodes":
		_, err = operation_setting.ParseHTTPStatusCodeRanges(option.Value.(string))
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "billing_setting.billing_expr":
		expressions := make(map[string]string)
		if err = common.UnmarshalJsonStr(option.Value.(string), &expressions); err != nil {
			common.ApiErrorMsg(c, "计费表达式配置必须是模型到表达式的 JSON 对象: "+err.Error())
			return
		}
		models := make([]string, 0, len(expressions))
		for modelName := range expressions {
			models = append(models, modelName)
		}
		sort.Strings(models)
		storedVariants := billing_setting.GetPluginBillingExprCopy()
		for _, modelName := range models {
			variants := make(map[string]any)
			for key, expression := range storedVariants {
				if plugin, name, ok := billing_setting.SplitPluginBillingExprKey(key); ok && name == modelName {
					variants[plugin] = expression
				}
			}
			err = model.ValidateModelPricing(modelName, model.PricingValues{
				"billing_setting.billing_expr":          expressions[modelName],
				billing_setting.PluginBillingExprOption: variants,
			})
			if err != nil {
				common.ApiErrorMsg(c, fmt.Sprintf("模型 %s 的计费表达式无效: %v", modelName, err))
				return
			}
		}
	case billing_setting.PluginBillingExprOption:
		var expressions map[string]string
		if err = common.UnmarshalJsonStr(option.Value.(string), &expressions); err != nil || expressions == nil {
			common.ApiErrorMsg(c, "plugin billing expressions must be a JSON object")
			return
		}
		for key, expression := range expressions {
			plugin, name, valid := billing_setting.SplitPluginBillingExprKey(key)
			if !valid {
				common.ApiErrorMsg(c, "invalid plugin billing expression key: "+key)
				return
			}
			if err = model.ValidateModelPricing(name, model.PricingValues{
				billing_setting.PluginBillingExprOption: map[string]any{plugin: expression},
			}); err != nil {
				common.ApiErrorMsg(c, err.Error())
				return
			}
		}
	case "console_setting.api_info":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "ApiInfo")
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "console_setting.announcements":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "Announcements")
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "console_setting.faq":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "FAQ")
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	case "console_setting.uptime_kuma_groups":
		err = console_setting.ValidateConsoleSettings(option.Value.(string), "UptimeKumaGroups")
		if err != nil {
			c.JSON(http.StatusOK, gin.H{
				"success": false,
				"message": err.Error(),
			})
			return
		}
	}
	if model.IsPasskeyDomainOption(option.Key) {
		change, updateErr := model.UpdatePasskeyDomainOptions(map[string]string{option.Key: option.Value.(string)}, false, "")
		if updateErr != nil {
			writePasskeyDomainSettingsError(c, updateErr)
			recordPasskeyDomainAudit(c, change, false, updateErr)
			return
		}
		recordPasskeyDomainAudit(c, change, false, nil)
		common.ApiSuccess(c, change)
		return
	}
	err = model.UpdateOption(option.Key, option.Value.(string))
	if err != nil {
		if errors.Is(err, system_setting.ErrPasskeyRPIDInvalid) {
			writeSecurityOperationError(c, err)
		} else {
			common.ApiError(c, err)
		}
		return
	}
	// 出于安全考虑只记录被修改的配置项名称，不记录配置值（可能含密钥等敏感信息）。
	recordManageAudit(c, "option.update", map[string]any{
		"key": option.Key,
	})
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
	})
}

type RequestRule struct {
	FieldType string  `json:"field_type"` // request_body 或 header
	ParamKey  string  `json:"param_key"`  // 例如 service_tier
	Operator  string  `json:"operator"`   // ==, !=, >= 等
	Value     string  `json:"value"`      // 匹配值
	Ratio     float64 `json:"ratio"`      // 命中時的倍率
}

type BillingBranch struct {
	Name            string  `json:"name"`
	InputPrice      float64 `json:"input_price"`
	OutputPrice     float64 `json:"output_price"`
	CacheReadPrice  float64 `json:"cache_read_price"`
	CacheWritePrice float64 `json:"cache_write_price"`
}

type BillingTier struct {
	Name      string          `json:"name"`
	Condition string          `json:"condition"` // CEL 條件字串
	Branches  []BillingBranch `json:"branches"`
}

type ModelCELConfig struct {
	Tiers []BillingTier `json:"tiers"`
	Rules []RequestRule `json:"rules"`
}

// safeGetCol 安全取得陣列欄位 (避免列超出索引報錯)
func safeGetCol(row []string, idx int) string {
	if idx < len(row) {
		return row[idx]
	}
	return "0"
}

// ExportModelRatios 匯出完整模型計費配置成 Excel 檔案 (支援多 Sheet)
func ExportModelRatios(c *gin.Context) {
	f := excelize.NewFile()
	defer func() {
		if err := f.Close(); err != nil {
			common.SysError("failed to close excel file: " + err.Error())
		}
	}()

	sBase := "Model_Base"
	sTiers := "Model_Tiers"
	sRules := "Request_Rules"

	f.NewSheet(sBase)
	f.NewSheet(sTiers)
	f.NewSheet(sRules)
	f.DeleteSheet("Sheet1") // 刪除預設工作表

	// 1. 表頭設定
	baseHeaders := []string{`Model Name`, `Billing Mode`, `Billing Expr`, `Fixed Price ($)`, `Model Ratio`, `Completion Ratio`}
	tierHeaders := []string{
		"Model Name",
		"Tier Name",
		"Condition (CEL)",
		"Branch Name",
		"Input Price ($/1M)",
		"Output Price ($/1M)",
		"Cache Read ($/1M)",
		"Cache Write ($/1M)",
	}
	ruleHeaders := []string{`Model Name`, `Field Type`, `Parameter Key`, `Operator`, `Match Value`, `Ratio Multiplier`}

	for i, h := range baseHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sBase, cell, h)
	}
	for i, h := range tierHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sTiers, cell, h)
	}
	for i, h := range ruleHeaders {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sRules, cell, h)
	}

	// 2. 獲取資料庫最新狀態
	billingModeMap := billing_setting.GetBillingModeCopy()
	billingExprMap := billing_setting.GetBillingExprCopy()
	modelPriceMap := ratio_setting.GetModelPriceCopy()
	modelRatioMap := ratio_setting.GetModelRatioCopy()
	completionRatioMap := ratio_setting.GetCompletionRatioCopy()

	modelSet := make(map[string]bool)
	for k := range billingExprMap {
		modelSet[k] = true
	}
	for k := range modelRatioMap {
		modelSet[k] = true
	}
	for k := range modelPriceMap {
		modelSet[k] = true
	}

	var modelList []string
	for k := range modelSet {
		modelList = append(modelList, k)
	}
	sort.Strings(modelList)

	rBase, rTiers, rRules := 2, 2, 2

	for _, modelName := range modelList {
		mode := billingModeMap[modelName]
		if mode == "" {
			mode = "expr"
		}
		exprStr := billingExprMap[modelName]

		// 寫入 Sheet 1: Model_Base
		f.SetCellValue(sBase, fmt.Sprintf("A%d", rBase), modelName)
		f.SetCellValue(sBase, fmt.Sprintf("B%d", rBase), mode)
		f.SetCellValue(sBase, fmt.Sprintf("C%d", rBase), exprStr)
		f.SetCellValue(sBase, fmt.Sprintf("D%d", rBase), modelPriceMap[modelName])
		f.SetCellValue(sBase, fmt.Sprintf("E%d", rBase), modelRatioMap[modelName])
		f.SetCellValue(sBase, fmt.Sprintf("F%d", rBase), completionRatioMap[modelName])
		rBase++

		// 嘗試解析 CEL 高級配置 (Tiers & Rules)
		if strings.HasPrefix(strings.TrimSpace(exprStr), "{") {
			var cfg ModelCELConfig
			if err := json.Unmarshal([]byte(exprStr), &cfg); err == nil {
				// 寫入 Sheet 2: Model_Tiers
				for _, tier := range cfg.Tiers {
					for _, b := range tier.Branches {
						f.SetCellValue(sTiers, fmt.Sprintf("A%d", rTiers), modelName)
						f.SetCellValue(sTiers, fmt.Sprintf("B%d", rTiers), tier.Name)
						f.SetCellValue(sTiers, fmt.Sprintf("C%d", rTiers), tier.Condition)
						f.SetCellValue(sTiers, fmt.Sprintf("D%d", rTiers), b.Name)
						f.SetCellValue(sTiers, fmt.Sprintf("E%d", rTiers), b.InputPrice)
						f.SetCellValue(sTiers, fmt.Sprintf("F%d", rTiers), b.OutputPrice)
						f.SetCellValue(sTiers, fmt.Sprintf("G%d", rTiers), b.CacheReadPrice)
						f.SetCellValue(sTiers, fmt.Sprintf("H%d", rTiers), b.CacheWritePrice)
						rTiers++
					}
				}
				// 寫入 Sheet 3: Request_Rules
				for _, rule := range cfg.Rules {
					f.SetCellValue(sRules, fmt.Sprintf("A%d", rRules), modelName)
					f.SetCellValue(sRules, fmt.Sprintf("B%d", rRules), rule.FieldType)
					f.SetCellValue(sRules, fmt.Sprintf("C%d", rRules), rule.ParamKey)
					f.SetCellValue(sRules, fmt.Sprintf("D%d", rRules), rule.Operator)
					f.SetCellValue(sRules, fmt.Sprintf("E%d", rRules), rule.Value)
					f.SetCellValue(sRules, fmt.Sprintf("F%d", rRules), rule.Ratio)
					rRules++
				}
			}
		}
	}

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=model_pricing_%d.xlsx", time.Now().Unix()))
	_ = f.Write(c.Writer)
}

// ImportModelRatios 批量匯入並更新模型計費 (支援多 Sheet 解析組合)
func ImportModelRatios(c *gin.Context) {
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "無效的檔案上傳: " + err.Error()})
		return
	}
	defer file.Close()

	f, err := excelize.OpenReader(file)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"success": false, "message": "Excel 解析失敗: " + err.Error()})
		return
	}
	defer f.Close()

	billingModeMap := billing_setting.GetBillingModeCopy()
	billingExprMap := billing_setting.GetBillingExprCopy()
	modelPriceMap := ratio_setting.GetModelPriceCopy()
	modelRatioMap := ratio_setting.GetModelRatioCopy()
	completionRatioMap := ratio_setting.GetCompletionRatioCopy()

	// 臨時儲存 CEL 高級配置的 Map
	celConfigs := make(map[string]*ModelCELConfig)

	// 1. 解析 Sheet 1: Model_Base
	if rows, err := f.GetRows("Model_Base"); err == nil {
		for i, row := range rows {
			if i == 0 || len(row) == 0 {
				continue
			}
			mName := strings.TrimSpace(row[0])
			if mName == "" {
				continue
			}

			if len(row) > 1 && row[1] != "" {
				billingModeMap[mName] = strings.TrimSpace(row[1])
			}
			if len(row) > 2 && row[2] != "" {
				billingExprMap[mName] = strings.TrimSpace(row[2])
			}
			if len(row) > 3 && row[3] != "" {
				if val, err := strconv.ParseFloat(strings.TrimSpace(row[3]), 64); err == nil {
					if val > 0 {
						modelPriceMap[mName] = val
					} else {
						delete(modelPriceMap, mName)
					}
				}
			}
			if len(row) > 4 && row[4] != "" {
				if val, err := strconv.ParseFloat(strings.TrimSpace(row[4]), 64); err == nil {
					modelRatioMap[mName] = val
				}
			}
			if len(row) > 5 && row[5] != "" {
				if val, err := strconv.ParseFloat(strings.TrimSpace(row[5]), 64); err == nil {
					completionRatioMap[mName] = val
				}
			}
		}
	}

	// 2. 解析 Sheet 2: Model_Tiers
	if rows, err := f.GetRows("Model_Tiers"); err == nil {
		for i, row := range rows {
			if i == 0 || len(row) < 4 {
				continue
			}
			mName := strings.TrimSpace(row[0])
			if mName == "" {
				continue
			}

			if _, exists := celConfigs[mName]; !exists {
				celConfigs[mName] = &ModelCELConfig{Tiers: []BillingTier{}, Rules: []RequestRule{}}
			}

			tName := strings.TrimSpace(row[1])
			tCond := strings.TrimSpace(row[2])
			bName := strings.TrimSpace(row[3])

			inP, _ := strconv.ParseFloat(strings.TrimSpace(safeGetCol(row, 4)), 64)
			outP, _ := strconv.ParseFloat(strings.TrimSpace(safeGetCol(row, 5)), 64)
			cRead, _ := strconv.ParseFloat(strings.TrimSpace(safeGetCol(row, 6)), 64)
			cWrite, _ := strconv.ParseFloat(strings.TrimSpace(safeGetCol(row, 7)), 64)

			branch := BillingBranch{Name: bName, InputPrice: inP, OutputPrice: outP, CacheReadPrice: cRead, CacheWritePrice: cWrite}

			// 尋找是否已存在該 Tier
			foundTier := false
			for tIdx, t := range celConfigs[mName].Tiers {
				if t.Name == tName {
					celConfigs[mName].Tiers[tIdx].Branches = append(celConfigs[mName].Tiers[tIdx].Branches, branch)
					foundTier = true
					break
				}
			}
			if !foundTier {
				celConfigs[mName].Tiers = append(celConfigs[mName].Tiers, BillingTier{
					Name:      tName,
					Condition: tCond,
					Branches:  []BillingBranch{branch},
				})
			}
		}
	}

	// 3. 解析 Sheet 3: Request_Rules
	if rows, err := f.GetRows("Request_Rules"); err == nil {
		for i, row := range rows {
			if i == 0 || len(row) < 6 {
				continue
			}
			mName := strings.TrimSpace(row[0])
			if mName == "" {
				continue
			}

			if _, exists := celConfigs[mName]; !exists {
				celConfigs[mName] = &ModelCELConfig{Tiers: []BillingTier{}, Rules: []RequestRule{}}
			}

			ratio, _ := strconv.ParseFloat(strings.TrimSpace(safeGetCol(row, 5)), 64)
			rule := RequestRule{
				FieldType: strings.TrimSpace(row[1]),
				ParamKey:  strings.TrimSpace(row[2]),
				Operator:  strings.TrimSpace(row[3]),
				Value:     strings.TrimSpace(row[4]),
				Ratio:     ratio,
			}
			celConfigs[mName].Rules = append(celConfigs[mName].Rules, rule)
		}
	}

	// 4. 將解析出的 CEL 結構體序列化回 JSON 並覆蓋原 Expr
	for mName, cfg := range celConfigs {
		if len(cfg.Tiers) > 0 || len(cfg.Rules) > 0 {
			bytes, err := json.Marshal(cfg)
			if err == nil {
				billingExprMap[mName] = string(bytes)
				billingModeMap[mName] = "expr"
			}
		}
	}

	// 5. 寫回 Option 資料庫
	saveMapOption("billing_setting.billing_mode", billingModeMap)
	saveMapOption("billing_setting.billing_expr", billingExprMap)
	saveMapOption("ModelPrice", modelPriceMap)
	saveMapOption("ModelRatio", modelRatioMap)
	saveMapOption("CompletionRatio", completionRatioMap)

	c.JSON(http.StatusOK, gin.H{"success": true, "message": "批量匯入成功！已同步更新多工作表基礎倍率與 CEL 階梯規則。"})
}

func saveMapOption(key string, data interface{}) {
	bytes, _ := json.Marshal(data)
	_ = model.UpdateOption(key, string(bytes))
}
