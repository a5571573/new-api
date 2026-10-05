package controller

import (
	"errors"
	"fmt"
	"maps"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// The workbook is a thin view over the model pricing API: the editable sheet
// carries each model's configured values (blank = inherit the engine default,
// 0 = free), and imports are saved through model.UpdateModelPricing so they get
// the same validation, version check, and transaction as the settings page.
const (
	pricingSheetEditable  = "Model Pricing"
	pricingSheetEffective = "Effective Pricing"
	pricingSheetGuide     = "Instructions"

	pricingHeaderModel   = "Model Name"
	pricingHeaderVersion = "Version (do not edit)"

	pricingImportMaxBytes = 10 << 20
	pricingImportMaxRows  = 20000
	pricingImportMaxError = 50
)

type pricingExcelColumn struct {
	Header string
	Key    string
	Text   bool
}

var pricingExcelColumns = []pricingExcelColumn{
	{Header: "Billing Mode", Key: "billing_setting.billing_mode", Text: true},
	{Header: "Billing Expression", Key: "billing_setting.billing_expr", Text: true},
	{Header: "Fixed Price (USD/request)", Key: "ModelPrice"},
	{Header: "Model Ratio", Key: "ModelRatio"},
	{Header: "Completion Ratio", Key: "CompletionRatio"},
	{Header: "Cache Ratio", Key: "CacheRatio"},
	{Header: "Create Cache Ratio", Key: "CreateCacheRatio"},
	{Header: "Image Ratio", Key: "ImageRatio"},
	{Header: "Audio Ratio", Key: "AudioRatio"},
	{Header: "Audio Completion Ratio", Key: "AudioCompletionRatio"},
}

var pricingExcelGuide = []string{
	"模型定價批次編輯 / Model pricing bulk edit",
	"",
	"1. 只修改「Model Pricing」工作表；「Effective Pricing」是目前實際生效的價格，僅供參考，匯入時會忽略。",
	"   Edit only the \"Model Pricing\" sheet. \"Effective Pricing\" shows the prices in effect and is ignored on import.",
	"2. 空白 = 不設定，沿用系統預設值；填 0 = 明確設為 0（免費）。",
	"   Blank = not set (the system default applies). 0 = explicitly zero (free).",
	"3. Billing Mode 只能填 ratio、tiered_expr，或留空。tiered_expr 使用 Billing Expression 計費。",
	"   Billing Mode accepts ratio, tiered_expr, or blank. tiered_expr bills with the Billing Expression.",
	"4. Fixed Price 有值時按次計費，優先於倍率。倍率模式下，Model Ratio 1 = 每百萬輸入 token 2 美元。",
	"   A Fixed Price bills per request and overrides ratios. In ratio mode, Model Ratio 1 = USD 2 per 1M input tokens.",
	"5. 可新增列來設定新模型。刪除列不會刪除該模型的價格；不想修改的欄位也可以整欄刪除。",
	"   Add rows to price new models. Deleting a row does not remove its price; you may delete whole columns you do not change.",
	"6. 不要修改「Version」欄。若匯出後有人在後台改過同一個模型，匯入會被拒絕，請重新匯出。",
	"   Do not edit the Version column. If someone changes the same model after export, the import is rejected; export again.",
	"7. 匯入前會先列出所有變更供確認；任何一列有錯誤時，整批都不會寫入。",
	"   Changes are listed for confirmation before saving. If any row has an error, nothing is saved.",
	"8. 任務插件的專屬計費表達式不在此檔案中，匯入時會保持不變。",
	"   Task plugin billing expressions are not included and stay unchanged on import.",
}

type pricingFieldChange struct {
	Field  string `json:"field"`
	Before string `json:"before"`
	After  string `json:"after"`
}

type pricingModelChange struct {
	ModelName string               `json:"model_name"`
	Fields    []pricingFieldChange `json:"fields"`
}

// ExportModelPricingExcel exports every priced model as an editable workbook.
func ExportModelPricingExcel(c *gin.Context) {
	snapshot, err := model.GetModelPricingSnapshot(nil)
	if err != nil {
		common.ApiError(c, err)
		return
	}

	f := excelize.NewFile()
	defer f.Close()
	f.SetSheetName("Sheet1", pricingSheetEditable)
	if _, err := f.NewSheet(pricingSheetEffective); err != nil {
		common.ApiError(c, err)
		return
	}
	if _, err := f.NewSheet(pricingSheetGuide); err != nil {
		common.ApiError(c, err)
		return
	}

	editableHeaders := []any{pricingHeaderModel}
	effectiveHeaders := []any{pricingHeaderModel}
	for _, column := range pricingExcelColumns {
		editableHeaders = append(editableHeaders, column.Header)
		effectiveHeaders = append(effectiveHeaders, column.Header)
	}
	editableHeaders = append(editableHeaders, pricingHeaderVersion)
	effectiveHeaders = append(effectiveHeaders, "Input (USD/1M tokens)", "Output (USD/1M tokens)")
	_ = f.SetSheetRow(pricingSheetEditable, "A1", &editableHeaders)
	_ = f.SetSheetRow(pricingSheetEffective, "A1", &effectiveHeaders)

	for i, entry := range snapshot.Entries {
		editable := []any{entry.ModelName}
		effective := []any{entry.ModelName}
		for _, column := range pricingExcelColumns {
			editable = append(editable, entry.Configured[column.Key])
			effective = append(effective, entry.Effective[column.Key])
		}
		editable = append(editable, entry.Version)
		effective = append(effective, ratioTokenPrices(entry.Effective)...)

		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		_ = f.SetSheetRow(pricingSheetEditable, cell, &editable)
		_ = f.SetSheetRow(pricingSheetEffective, cell, &effective)
	}

	for i, line := range pricingExcelGuide {
		_ = f.SetCellValue(pricingSheetGuide, fmt.Sprintf("A%d", i+1), line)
	}

	lastColumn, _ := excelize.ColumnNumberToName(len(editableHeaders))
	_ = f.SetColWidth(pricingSheetEditable, "A", "A", 36)
	_ = f.SetColWidth(pricingSheetEditable, "B", lastColumn, 18)
	_ = f.SetColWidth(pricingSheetEditable, "C", "C", 60)
	_ = f.SetColWidth(pricingSheetEffective, "A", "A", 36)
	_ = f.SetColWidth(pricingSheetEffective, "B", "N", 18)
	_ = f.SetColWidth(pricingSheetEffective, "C", "C", 60)
	_ = f.SetColWidth(pricingSheetGuide, "A", "A", 120)
	frozen := &excelize.Panes{Freeze: true, XSplit: 1, YSplit: 1, TopLeftCell: "B2", ActivePane: "bottomRight"}
	_ = f.SetPanes(pricingSheetEditable, frozen)
	_ = f.SetPanes(pricingSheetEffective, frozen)

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=model_pricing_%s.xlsx", time.Now().Format("20060102_150405")))
	if err := f.Write(c.Writer); err != nil {
		common.SysError("failed to write model pricing workbook: " + err.Error())
	}
}

// ratioTokenPrices converts effective ratio pricing into USD per 1M tokens for
// the read-only sheet. Fixed-price and expression models have no such price.
func ratioTokenPrices(effective model.PricingValues) []any {
	if effective["billing_setting.billing_mode"] == "tiered_expr" {
		return []any{nil, nil}
	}
	if _, fixed := effective["ModelPrice"]; fixed {
		return []any{nil, nil}
	}
	ratio, ok := effective["ModelRatio"].(float64)
	if !ok {
		return []any{nil, nil}
	}
	input := ratio * 1000000 / common.QuotaPerUnit
	completion, _ := effective["CompletionRatio"].(float64)
	return []any{input, input * completion}
}

// ImportModelPricingExcel applies an edited workbook. With dry_run=true it only
// reports the changes, so the administrator can confirm before saving.
func ImportModelPricingExcel(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, pricingImportMaxBytes)
	file, _, err := c.Request.FormFile("file")
	if err != nil {
		common.ApiErrorMsg(c, "invalid upload: "+err.Error())
		return
	}
	defer file.Close()

	workbook, err := excelize.OpenReader(file)
	if err != nil {
		common.ApiErrorMsg(c, "cannot read the Excel file: "+err.Error())
		return
	}
	defer workbook.Close()

	rows, err := workbook.GetRows(pricingSheetEditable, excelize.Options{RawCellValue: true})
	if err != nil {
		common.ApiErrorMsg(c, fmt.Sprintf("sheet %q not found; export a new template first", pricingSheetEditable))
		return
	}
	if len(rows) > pricingImportMaxRows {
		common.ApiErrorMsg(c, fmt.Sprintf("too many rows (max %d)", pricingImportMaxRows))
		return
	}
	if len(rows) == 0 {
		common.ApiErrorMsg(c, "the sheet is empty")
		return
	}

	columnIndex := make(map[string]int)
	for i, header := range rows[0] {
		columnIndex[strings.ToLower(strings.TrimSpace(header))] = i
	}
	modelColumn, ok := columnIndex[strings.ToLower(pricingHeaderModel)]
	if !ok {
		common.ApiErrorMsg(c, fmt.Sprintf("column %q not found; export a new template first", pricingHeaderModel))
		return
	}
	versionColumn, hasVersion := columnIndex[strings.ToLower(pricingHeaderVersion)]

	cell := func(row []string, index int) string {
		if index < len(row) {
			return strings.TrimSpace(row[index])
		}
		return ""
	}

	type importRow struct {
		line    int
		name    string
		version string
		values  map[string]string
	}
	var parsed []importRow
	var names []string
	var rowErrors []string
	seen := make(map[string]int)
	for i, row := range rows[1:] {
		line := i + 2
		name := cell(row, modelColumn)
		if name == "" {
			continue
		}
		if previous, duplicate := seen[name]; duplicate {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): duplicate of row %d", line, name, previous))
			continue
		}
		seen[name] = line
		values := make(map[string]string)
		for _, column := range pricingExcelColumns {
			if index, present := columnIndex[strings.ToLower(column.Header)]; present {
				values[column.Key] = cell(row, index)
			}
		}
		version := ""
		if hasVersion {
			version = cell(row, versionColumn)
		}
		parsed = append(parsed, importRow{line: line, name: name, version: version, values: values})
		names = append(names, name)
	}
	if len(names) == 0 && len(rowErrors) == 0 {
		common.ApiErrorMsg(c, "no model rows found")
		return
	}

	snapshot, err := model.GetModelPricingSnapshot(names)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	current := make(map[string]model.ModelPricingEntry, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		current[entry.ModelName] = entry
	}

	var changes []model.ModelPricingChange
	var summary []pricingModelChange
	unchanged := 0
	for _, row := range parsed {
		entry, exists := current[row.name]
		before := entry.Configured
		currentVersion := entry.Version
		if !exists {
			before = model.PricingValues{}
			currentVersion = snapshot.EmptyVersion
		}

		draft := maps.Clone(before)
		if draft == nil {
			draft = model.PricingValues{}
		}
		var fields []pricingFieldChange
		var parseErrors []string
		for _, column := range pricingExcelColumns {
			raw, present := row.values[column.Key]
			if !present {
				continue
			}
			var value any
			switch {
			case raw == "":
				value = nil
			case column.Key == "billing_setting.billing_mode":
				mode := strings.ToLower(raw)
				if mode != "ratio" && mode != "tiered_expr" {
					parseErrors = append(parseErrors, fmt.Sprintf("%s must be ratio, tiered_expr, or blank (got %q)", column.Header, raw))
					continue
				}
				value = mode
			case column.Text:
				value = raw
			default:
				number, err := strconv.ParseFloat(raw, 64)
				if err != nil {
					parseErrors = append(parseErrors, fmt.Sprintf("%s is not a number (got %q)", column.Header, raw))
					continue
				}
				value = number
			}
			oldText := pricingCellText(before[column.Key])
			newText := pricingCellText(value)
			if oldText == newText || samePricingNumber(before[column.Key], value) {
				continue
			}
			if value == nil {
				delete(draft, column.Key)
			} else {
				draft[column.Key] = value
			}
			fields = append(fields, pricingFieldChange{Field: column.Header, Before: oldText, After: newText})
		}
		if len(parseErrors) > 0 {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): %s", row.line, row.name, strings.Join(parseErrors, "; ")))
			continue
		}
		if len(fields) == 0 {
			unchanged++
			continue
		}
		if row.version != "" && row.version != currentVersion {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): changed by someone else after export; export again", row.line, row.name))
			continue
		}
		if err := model.ValidateModelPricing(row.name, draft); err != nil {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): %s", row.line, row.name, err.Error()))
			continue
		}
		changes = append(changes, model.ModelPricingChange{ModelName: row.name, ExpectedVersion: currentVersion, Pricing: draft})
		summary = append(summary, pricingModelChange{ModelName: row.name, Fields: fields})
	}

	if len(rowErrors) > 0 {
		total := len(rowErrors)
		if total > pricingImportMaxError {
			rowErrors = rowErrors[:pricingImportMaxError]
		}
		c.JSON(http.StatusOK, gin.H{
			"success": false,
			"message": fmt.Sprintf("%d row(s) have errors; nothing was saved", total),
			"data":    gin.H{"errors": rowErrors},
		})
		return
	}

	dryRun := c.Query("dry_run") == "true"
	if !dryRun && len(changes) > 0 {
		if err := model.UpdateModelPricing(changes); err != nil {
			message := err.Error()
			if errors.Is(err, model.ErrModelPricingConflict) {
				message = "model pricing changed while importing; export again and retry (" + message + ")"
			}
			common.ApiErrorMsg(c, message)
			return
		}
		changedNames := make([]string, 0, len(changes))
		for _, change := range changes {
			changedNames = append(changedNames, change.ModelName)
		}
		recordManageAudit(c, "model.pricing.update", map[string]any{"models": changedNames, "source": "excel"})
	}

	common.ApiSuccess(c, gin.H{
		"applied":   !dryRun && len(changes) > 0,
		"changes":   summary,
		"unchanged": unchanged,
	})
}

// samePricingNumber ignores the last-digit noise Excel introduces when it
// re-saves a value with 15 significant digits.
func samePricingNumber(before, after any) bool {
	a, ok := before.(float64)
	if !ok {
		return false
	}
	b, ok := after.(float64)
	if !ok {
		return false
	}
	return math.Abs(a-b) <= 1e-12*max(math.Abs(a), math.Abs(b))
}

// pricingCellText renders a configured value the way it appears in a cell, so
// blank means unset and numbers compare by value rather than by type.
func pricingCellText(value any) string {
	switch v := value.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case string:
		return strings.TrimSpace(v)
	default:
		return fmt.Sprint(v)
	}
}
