package controller

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/xuri/excelize/v2"

	"github.com/gin-gonic/gin"
)

// 將 log.Type 整數轉為中文類型名稱
func getLogTypeName(logType int) string {
	switch logType {
	case 1:
		return "儲值" // LogTypeTopup
	case 2:
		return "消耗" // LogTypeConsume
	case 3:
		return "管理" // LogTypeManage
	case 4:
		return "系統" // LogTypeSystem
	case 5:
		return "錯誤" // LogTypeError
	case 6:
		return "退款" // LogTypeRefund
	case 7:
		return "登入" // LogTypeLogin
	default:
		return "未知" // LogTypeUnknown (0) 或其他未知類型
	}
}

func GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), channel, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if c.GetInt("role") < common.RoleRootUser {
		model.FormatAdminLogs(logs)
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		c.JSON(200, gin.H{
			"success": false,
			"message": "无效的令牌",
		})
		return
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func GetLogsStat(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
	return
}

func GetLogsSelfStat(c *gin.Context) {
	username := c.GetString("username")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	quotaNum, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, tokenName)
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": quotaNum.Quota,
			"rpm":   quotaNum.Rpm,
			"tpm":   quotaNum.Tpm,
			//"token": tokenNum,
		},
	})
	return
}

func ExportLogs(c *gin.Context) {
	// 1. 解析前端傳入的篩選參數
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	modelName := c.Query("model_name")
	group := c.Query("group")
	username := c.Query("username")
	tokenName := c.Query("token_name")

	// 2. 建立 Excel 檔案
	f := excelize.NewFile()
	defer f.Close()

	// ---------------------------------------------------------
	// Sheet 1: Usage Logs (標準 10 大英文表頭)
	// ---------------------------------------------------------
	sheet1 := "Usage Logs"
	f.SetSheetName("Sheet1", sheet1)

	// 表頭完全對齊前端列表：Time, Channel, User, Token, Model, Stream, Tokens, Cost, Timing, Details
	headers1 := []string{
		"Time",
		"Channel",
		"User",
		"Token",
		"Model",
		"Stream",
		"Tokens",
		"Cost",
		"Timing",
		"Details",
	}
	for i, h := range headers1 {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet1, cell, h)
	}

	// 查詢日誌明細數據
	var logs []*model.Log
	query := model.LOG_DB
	if startTimestamp > 0 {
		query = query.Where("created_at >= ?", startTimestamp)
	}
	if endTimestamp > 0 {
		query = query.Where("created_at <= ?", endTimestamp)
	}
	if modelName != "" {
		query = query.Where("model_name = ?", modelName)
	}
	if username != "" {
		query = query.Where("username = ?", username)
	}
	if tokenName != "" {
		query = query.Where("token_name = ?", tokenName)
	}
	if group != "" {
		query = query.Where("group_name = ?", group)
	}
	if logType != 0 {
		query = query.Where("type = ?", logType)
	}
	query.Order("id desc").Limit(5000).Find(&logs)

	for idx, l := range logs {
		row := idx + 2

		// 是否為流式請求 (Stream)
		isStream := "No"
		if l.IsStream { // 若 Log struct 無此欄位可直接設為 "No" 或判斷 l.Type
			isStream = "Yes"
		}

		// 總 Tokens 數 (Prompt + Completion)
		totalTokens := l.PromptTokens + l.CompletionTokens

		// 耗時 (ms)
		timingStr := fmt.Sprintf("%dms", l.UseTime)

		// 詳情 (若 Content 為空，寫入日誌類型名稱)
		details := l.Content
		if details == "" {
			details = getLogTypeName(l.Type)
		}

		_ = f.SetCellValue(sheet1, fmt.Sprintf("A%d", row), time.Unix(l.CreatedAt, 0).Format("2006-01-02 15:04:05")) // Time
		_ = f.SetCellValue(sheet1, fmt.Sprintf("B%d", row), l.ChannelId)                                             // Channel
		_ = f.SetCellValue(sheet1, fmt.Sprintf("C%d", row), l.Username)                                              // User
		_ = f.SetCellValue(sheet1, fmt.Sprintf("D%d", row), l.TokenName)                                             // Token
		_ = f.SetCellValue(sheet1, fmt.Sprintf("E%d", row), l.ModelName)                                             // Model
		_ = f.SetCellValue(sheet1, fmt.Sprintf("F%d", row), isStream)                                                // Stream
		_ = f.SetCellValue(sheet1, fmt.Sprintf("G%d", row), totalTokens)                                             // Tokens
		_ = f.SetCellValue(sheet1, fmt.Sprintf("H%d", row), float64(l.Quota)/500000.0)                               // Cost ($)
		_ = f.SetCellValue(sheet1, fmt.Sprintf("I%d", row), timingStr)                                               // Timing
		_ = f.SetCellValue(sheet1, fmt.Sprintf("J%d", row), details)                                                 // Details
	}

	// ---------------------------------------------------------
	// Sheet 2: User Monthly Summary (用戶月度對帳全英文頁籤)
	// ---------------------------------------------------------
	sheet2 := "User Monthly Summary"
	f.NewSheet(sheet2)

	headers2 := []string{
		"User",
		"Month",
		"Current Month Cost ($)",
		"Previous Month Cost ($)",
		"MoM Growth Rate",
	}
	for i, h := range headers2 {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		_ = f.SetCellValue(sheet2, cell, h)
	}

	type MonthlyRecon struct {
		Username   string  `json:"username"`
		Month      string  `json:"month"`
		TotalQuota float64 `json:"total_quota"`
	}

	var reconList []MonthlyRecon
	model.LOG_DB.Raw(`
		SELECT username, 
		       strftime('%Y-%m', datetime(created_at, 'unixepoch')) as month, 
		       SUM(quota) / 500000.0 as total_quota 
		FROM logs 
		GROUP BY username, month 
		ORDER BY month DESC, total_quota DESC
	`).Scan(&reconList)

	for idx, r := range reconList {
		row := idx + 2
		_ = f.SetCellValue(sheet2, fmt.Sprintf("A%d", row), r.Username)
		_ = f.SetCellValue(sheet2, fmt.Sprintf("B%d", row), r.Month)
		_ = f.SetCellValue(sheet2, fmt.Sprintf("C%d", row), r.TotalQuota)
		_ = f.SetCellValue(sheet2, fmt.Sprintf("D%d", row), 0.00)
		_ = f.SetCellFormula(sheet2, fmt.Sprintf("E%d", row), fmt.Sprintf("IF(D%d=0, 0, (C%d-D%d)/D%d)", row, row, row, row))
	}

	// 3. 設定 HTTP Response Header 並輸出帶時間戳記的 Excel 檔案
	timestamp := time.Now().Format("20060102_1504")
	filename := fmt.Sprintf("usage_logs_%s.xlsx", timestamp)

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	c.Header("Pragma", "no-cache")
	c.Header("Cache-Control", "no-cache")

	_ = f.Write(c.Writer)
}
