package controller

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/xuri/excelize/v2"
)

// The workbook mirrors the model pricing editor: prices are shown in USD the
// same way the settings page shows them, and an edited row is saved as the same
// draft the page would save, through model.UpdateModelPricing (validation,
// version check and transaction included). Unedited rows are never written.
const (
	pricingSheetEditable  = "Model Pricing"
	pricingSheetEffective = "Effective Pricing"
	pricingSheetGuide     = "Instructions"

	pricingImportMaxBytes = 10 << 20
	pricingImportMaxRows  = 20000
	pricingImportMaxError = 50

	pricingModeExpression = "Expression"
	pricingModePerToken   = "Per Token"
	pricingModePerRequest = "Per Request"
)

type pricingCol int

const (
	colModel pricingCol = iota
	colMode
	colExpression
	colFixedPrice
	colInput
	colCompletion
	colCacheRead
	colCacheWrite
	colImage
	colAudioInput
	colAudioOutput
	colVersion
	pricingColCount
)

// pricingHeaders are matched case-insensitively on import; the first name is
// written on export and the Chinese alias keeps earlier exports importable.
var pricingHeaders = [pricingColCount][]string{
	colModel:       {"Model Name", "模型名稱"},
	colMode:        {"Pricing Mode", "定價模式"},
	colExpression:  {"Billing Expression", "計費運算式"},
	colFixedPrice:  {"Fixed Price (USD/request)", "固定價格 (USD/次)"},
	colInput:       {"Input Price (USD/1M tokens)", "輸入價格 (USD/1M tokens)"},
	colCompletion:  {"Completion Price (USD/1M tokens)", "補全價格 (USD/1M tokens)"},
	colCacheRead:   {"Cache Read Price (USD/1M tokens)", "緩存讀取價格 (USD/1M tokens)"},
	colCacheWrite:  {"Cache Write Price (USD/1M tokens)", "緩存寫入價格 (USD/1M tokens)"},
	colImage:       {"Image Input Price (USD/1M tokens)", "圖像輸入價格 (USD/1M tokens)"},
	colAudioInput:  {"Audio Input Price (USD/1M tokens)", "音頻輸入價格 (USD/1M tokens)"},
	colAudioOutput: {"Audio Output Price (USD/1M tokens)", "音頻輸出價格 (USD/1M tokens)"},
	colVersion:     {"Version (do not edit)", "版本 (請勿修改)"},
}

// Token prices other than input are stored as ratios of a base price, exactly
// as the settings page converts them: audio output is relative to audio input,
// everything else to the input price.
var pricingLanes = []struct {
	col  pricingCol
	key  string
	base pricingCol
}{
	{colCompletion, "CompletionRatio", colInput},
	{colCacheRead, "CacheRatio", colInput},
	{colCacheWrite, "CreateCacheRatio", colInput},
	{colImage, "ImageRatio", colInput},
	{colAudioInput, "AudioRatio", colInput},
	{colAudioOutput, "AudioCompletionRatio", colAudioInput},
}

var pricingExcelGuide = []string{
	"Model pricing bulk edit",
	"",
	"1. Edit only the \"Model Pricing\" sheet. Its columns match the model pricing editor in the admin console; all prices are in USD.",
	"   \"Effective Pricing\" shows the prices currently in effect (system defaults included). It is for reference and ignored on import.",
	"2. Pricing Mode is Expression, Per Token, or Per Request, matching the three tabs in the editor.",
	"   - Expression: billed by the Billing Expression. For a simple expression such as tier(\"base\", p * 2 + c * 8),",
	"     its prices appear in the price columns; edit those and the expression is rewritten on import.",
	"     Tiered or conditional expressions leave the price columns blank; edit the Billing Expression column instead.",
	"     Do not change both the price columns and the Billing Expression in the same row.",
	"   - Per Token: billed by the input price and the other token prices (USD per 1M tokens).",
	"   - Per Request: billed by Fixed Price, once per request.",
	"   If Pricing Mode is blank: a Billing Expression means Expression; otherwise a Fixed Price means Per Request; otherwise Per Token.",
	"3. Blank = not set (prices other than the input price fall back to the system default). 0 = free.",
	"4. In Per Token mode, any other token price requires an Input Price. When Input Price is 0, other prices must be 0 or blank.",
	"   Audio Output Price requires an Audio Input Price.",
	"5. Add rows to price new models. Deleting a row does not remove its price, and rows you did not change are not saved.",
	"6. Do not edit the Version column. If someone changes the same model after your export, the import is rejected; export again.",
	"7. All changes are listed for confirmation before saving. If any row has an error, nothing is saved.",
	"8. Task plugin billing expressions are not included in this file and stay unchanged on import.",
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

func pricingNumber(values model.PricingValues, key string) (float64, bool) {
	number, ok := values[key].(float64)
	return number, ok
}

// Simple single-tier expressions, such as tier("base", p * 2.5 + c * 10), are
// what the visual editor shows as plain token prices. Only these are shown and
// edited through the price columns; anything else is edited as expression text.
var (
	simpleTierExpr     = regexp.MustCompile(`^tier\(\s*"([A-Za-z0-9_\-]+)"\s*,\s*(.+?)\s*\)$`)
	simpleTierExprTerm = regexp.MustCompile(`^\s*([a-z]+)\s*\*\s*([0-9]+(?:\.[0-9]+)?(?:[eE][-+]?[0-9]+)?)\s*$`)
	exprVariableCols   = []struct {
		variable string
		col      pricingCol
	}{
		{"p", colInput}, {"c", colCompletion}, {"cr", colCacheRead}, {"cc", colCacheWrite},
		{"img", colImage}, {"ai", colAudioInput}, {"ao", colAudioOutput},
	}
)

// parseSimpleTierExpr returns the tier name and the USD/1M price per column,
// or ok=false when the expression is not a single flat tier.
func parseSimpleTierExpr(expression string) (string, map[pricingCol]float64, bool) {
	match := simpleTierExpr.FindStringSubmatch(strings.TrimSpace(expression))
	if match == nil {
		return "", nil, false
	}
	prices := map[pricingCol]float64{}
	for term := range strings.SplitSeq(match[2], "+") {
		parts := simpleTierExprTerm.FindStringSubmatch(term)
		if parts == nil {
			return "", nil, false
		}
		col, known := pricingCol(0), false
		for _, candidate := range exprVariableCols {
			if candidate.variable == parts[1] {
				col, known = candidate.col, true
				break
			}
		}
		if !known {
			return "", nil, false
		}
		if _, duplicate := prices[col]; duplicate {
			return "", nil, false
		}
		price, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return "", nil, false
		}
		prices[col] = price
	}
	return match[1], prices, true
}

// pricingRowCells renders one model the way the settings page shows it. The
// mode follows the effective configuration (built-in expressions included);
// expression rows show the prices inside a simple expression, other rows show
// the configured legacy prices converted to USD.
func pricingRowCells(name string, configured, effective model.PricingValues) [pricingColCount]any {
	var cells [pricingColCount]any
	cells[colModel] = name
	_, fixed := configured["ModelPrice"]
	switch {
	case effective["billing_setting.billing_mode"] == "tiered_expr":
		cells[colMode] = pricingModeExpression
		expression, _ := effective["billing_setting.billing_expr"].(string)
		cells[colExpression] = expression
		if _, prices, ok := parseSimpleTierExpr(expression); ok {
			for col, price := range prices {
				cells[col] = price
			}
		}
		return cells
	case fixed:
		cells[colMode] = pricingModePerRequest
	default:
		cells[colMode] = pricingModePerToken
	}
	cells[colFixedPrice] = configured["ModelPrice"]

	prices := map[pricingCol]float64{}
	if ratio, ok := pricingNumber(configured, "ModelRatio"); ok {
		prices[colInput] = ratio * 1000000 / common.QuotaPerUnit
		cells[colInput] = prices[colInput]
	}
	for _, lane := range pricingLanes {
		ratio, hasRatio := pricingNumber(configured, lane.key)
		base, hasBase := prices[lane.base]
		if !hasRatio || !hasBase {
			continue
		}
		prices[lane.col] = ratio * base
		cells[lane.col] = prices[lane.col]
	}
	return cells
}

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

// samePricingCell compares cell text, treating numbers equal within the
// precision Excel keeps when it re-saves a workbook (15 significant digits).
func samePricingCell(before, after string) bool {
	if before == after {
		return true
	}
	a, errA := strconv.ParseFloat(before, 64)
	b, errB := strconv.ParseFloat(after, 64)
	if errA != nil || errB != nil {
		return false
	}
	return math.Abs(a-b) <= 1e-12*max(math.Abs(a), math.Abs(b))
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

	headers := make([]any, pricingColCount)
	for col := range pricingColCount {
		headers[col] = pricingHeaders[col][0]
	}
	effectiveHeaders := headers[:colVersion]
	_ = f.SetSheetRow(pricingSheetEditable, "A1", &headers)
	_ = f.SetSheetRow(pricingSheetEffective, "A1", &effectiveHeaders)

	for i, entry := range snapshot.Entries {
		cells := pricingRowCells(entry.ModelName, entry.Configured, entry.Effective)
		cells[colVersion] = entry.Version
		effective := pricingRowCells(entry.ModelName, entry.Effective, entry.Effective)
		editableRow := cells[:]
		effectiveRow := effective[:colVersion]
		cell, _ := excelize.CoordinatesToCellName(1, i+2)
		_ = f.SetSheetRow(pricingSheetEditable, cell, &editableRow)
		_ = f.SetSheetRow(pricingSheetEffective, cell, &effectiveRow)
	}

	for i, line := range pricingExcelGuide {
		_ = f.SetCellValue(pricingSheetGuide, fmt.Sprintf("A%d", i+1), line)
	}

	for _, sheet := range []string{pricingSheetEditable, pricingSheetEffective} {
		_ = f.SetColWidth(sheet, "A", "A", 36)
		_ = f.SetColWidth(sheet, "B", "B", 12)
		_ = f.SetColWidth(sheet, "C", "C", 50)
		_ = f.SetColWidth(sheet, "D", "L", 22)
		_ = f.SetPanes(sheet, &excelize.Panes{Freeze: true, XSplit: 1, YSplit: 1, TopLeftCell: "B2", ActivePane: "bottomRight"})
	}
	_ = f.SetColWidth(pricingSheetGuide, "A", "A", 110)

	c.Header("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=model_pricing_%s.xlsx", time.Now().Format("20060102_150405")))
	if err := f.Write(c.Writer); err != nil {
		common.SysError("failed to write model pricing workbook: " + err.Error())
	}
}

// canonicalPricingMode accepts the English mode names (any case or spacing)
// and the Chinese names used by earlier exports. Blank means "infer".
func canonicalPricingMode(value string) (string, bool) {
	switch strings.ToLower(strings.ReplaceAll(value, " ", "")) {
	case "", "auto":
		return "", true
	case "expression", "tiered_expr", "計費運算式":
		return pricingModeExpression, true
	case "pertoken", "per-token", "ratio", "按token":
		return pricingModePerToken, true
	case "perrequest", "per-request", "按次":
		return pricingModePerRequest, true
	}
	return value, false
}

// pricingDraftFromCells converts an edited row into the draft the settings page
// would save for the same inputs.
func pricingDraftFromCells(cells [pricingColCount]string, changed map[pricingCol]bool, previous model.PricingValues) (model.PricingValues, []string) {
	var problems []string
	header := func(col pricingCol) string { return pricingHeaders[col][0] }
	numbers := map[pricingCol]float64{}
	for col := colFixedPrice; col <= colAudioOutput; col++ {
		if cells[col] == "" {
			continue
		}
		number, err := strconv.ParseFloat(cells[col], 64)
		if err != nil || math.IsNaN(number) || math.IsInf(number, 0) || number < 0 {
			problems = append(problems, fmt.Sprintf("%s must be a non-negative number (got %q)", header(col), cells[col]))
			continue
		}
		numbers[col] = number
	}

	mode, known := canonicalPricingMode(cells[colMode])
	switch {
	case !known:
		problems = append(problems, fmt.Sprintf("%s must be %s, %s, %s, or blank (got %q)", header(colMode), pricingModeExpression, pricingModePerToken, pricingModePerRequest, cells[colMode]))
	case mode != "":
	case cells[colExpression] != "":
		mode = pricingModeExpression
	case cells[colFixedPrice] != "":
		mode = pricingModePerRequest
	default:
		mode = pricingModePerToken
	}
	if len(problems) > 0 {
		return nil, problems
	}

	draft := model.PricingValues{"billing_setting.billing_mode": "ratio"}
	if variants, ok := previous["billing_setting.plugin_billing_expr"]; ok {
		draft["billing_setting.plugin_billing_expr"] = variants
	}
	if mode == pricingModeExpression {
		return expressionPricingDraft(draft, cells, changed, numbers, previous)
	}
	if mode == pricingModePerRequest {
		price, ok := numbers[colFixedPrice]
		if !ok {
			return nil, []string{fmt.Sprintf("%s is required for %s", header(colFixedPrice), pricingModePerRequest)}
		}
		draft["ModelPrice"] = price
		return draft, nil
	}

	// Token prices: the same checks the settings page runs before saving.
	if input, ok := numbers[colInput]; ok {
		draft["ModelRatio"] = input * common.QuotaPerUnit / 1000000
	}
	for _, lane := range pricingLanes {
		price, hasPrice := numbers[lane.col]
		if !hasPrice {
			continue
		}
		base, hasBase := numbers[lane.base]
		if !hasBase {
			return nil, []string{fmt.Sprintf("%s requires %s", header(lane.col), header(lane.base))}
		}
		if base == 0 {
			if price > 0 {
				return nil, []string{fmt.Sprintf("%s must be 0 when %s is 0; use %s for this pricing", header(lane.col), header(lane.base), pricingModeExpression)}
			}
			draft[lane.key] = float64(0)
			continue
		}
		draft[lane.key] = price / base
	}
	// Keep stored ratios when the price round-trip only differs by float noise.
	for key, value := range draft {
		if old, ok := previous[key].(float64); ok {
			if number, ok := value.(float64); ok && samePricingCell(pricingCellText(old), pricingCellText(number)) {
				draft[key] = old
			}
		}
	}
	return draft, nil
}

// expressionPricingDraft saves an expression row. The legacy per-token and
// per-request prices are kept as they were (the page keeps them for switching
// back), and edited price columns are written into a simple expression.
func expressionPricingDraft(draft model.PricingValues, cells [pricingColCount]string, changed map[pricingCol]bool, numbers map[pricingCol]float64, previous model.PricingValues) (model.PricingValues, []string) {
	draft["billing_setting.billing_mode"] = "tiered_expr"
	for _, key := range []string{"ModelPrice", "ModelRatio", "CompletionRatio", "CacheRatio", "CreateCacheRatio", "ImageRatio", "AudioRatio", "AudioCompletionRatio"} {
		if value, ok := previous[key]; ok {
			draft[key] = value
		}
	}
	if changed[colFixedPrice] {
		return nil, []string{fmt.Sprintf("%s is not used by %s; set %s to %s instead", pricingHeaders[colFixedPrice][0], pricingModeExpression, pricingHeaders[colMode][0], pricingModePerRequest)}
	}

	pricesChanged := false
	for col := colInput; col <= colAudioOutput; col++ {
		pricesChanged = pricesChanged || changed[col]
	}
	expression := cells[colExpression]
	if pricesChanged && changed[colExpression] {
		return nil, []string{fmt.Sprintf("edit either %s or the price columns, not both", pricingHeaders[colExpression][0])}
	}
	if !changed[colExpression] && (pricesChanged || expression == "") {
		tierName := "base"
		if expression != "" {
			name, _, ok := parseSimpleTierExpr(expression)
			if !ok {
				return nil, []string{fmt.Sprintf("this %s has tiers or conditions; edit %s directly", pricingModeExpression, pricingHeaders[colExpression][0])}
			}
			tierName = name
		}
		_, hasInput := numbers[colInput]
		_, hasCompletion := numbers[colCompletion]
		if !hasInput || !hasCompletion {
			return nil, []string{fmt.Sprintf("%s needs both %s and %s", pricingModeExpression, pricingHeaders[colInput][0], pricingHeaders[colCompletion][0])}
		}
		var terms []string
		for _, variable := range exprVariableCols {
			if price, ok := numbers[variable.col]; ok {
				terms = append(terms, variable.variable+" * "+strconv.FormatFloat(price, 'f', -1, 64))
			}
		}
		expression = fmt.Sprintf("tier(%q, %s)", tierName, strings.Join(terms, " + "))
	}
	if expression != "" {
		draft["billing_setting.billing_expr"] = expression
	}
	return draft, nil
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
	if len(rows) == 0 {
		common.ApiErrorMsg(c, "the sheet is empty")
		return
	}
	if len(rows) > pricingImportMaxRows {
		common.ApiErrorMsg(c, fmt.Sprintf("too many rows (max %d)", pricingImportMaxRows))
		return
	}

	columnIndex := map[pricingCol]int{}
	for index, raw := range rows[0] {
		name := strings.ToLower(strings.TrimSpace(raw))
		for col := range pricingColCount {
			for _, alias := range pricingHeaders[col] {
				if name == strings.ToLower(alias) {
					columnIndex[col] = index
				}
			}
		}
	}
	if _, ok := columnIndex[colModel]; !ok {
		common.ApiErrorMsg(c, fmt.Sprintf("column %q not found; export a new template first", pricingHeaders[colModel][0]))
		return
	}

	type importRow struct {
		line  int
		cells map[pricingCol]string
	}
	var parsed []importRow
	var names []string
	var rowErrors []string
	seen := map[string]int{}
	for i, row := range rows[1:] {
		line := i + 2
		cells := map[pricingCol]string{}
		for col, index := range columnIndex {
			if index < len(row) {
				cells[col] = strings.TrimSpace(row[index])
			} else {
				cells[col] = ""
			}
		}
		if raw, present := cells[colMode]; present {
			if mode, known := canonicalPricingMode(raw); known {
				cells[colMode] = mode
			}
		}
		name := cells[colModel]
		if name == "" {
			continue
		}
		if previous, duplicate := seen[name]; duplicate {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): duplicate of row %d", line, name, previous))
			continue
		}
		seen[name] = line
		parsed = append(parsed, importRow{line: line, cells: cells})
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
		name := row.cells[colModel]
		entry, exists := current[name]
		configured, effective, version := entry.Configured, entry.Effective, entry.Version
		if !exists {
			configured, effective, version = model.PricingValues{}, model.PricingValues{}, snapshot.EmptyVersion
		}

		currentCells := pricingRowCells(name, configured, effective)
		var merged [pricingColCount]string
		var fields []pricingFieldChange
		changed := map[pricingCol]bool{}
		for col := colModel; col < colVersion; col++ {
			before := pricingCellText(currentCells[col])
			merged[col] = before
			after, present := row.cells[col]
			if col == colMode && after == "" {
				// A blank mode is inferred from the other cells, not a change.
				merged[col] = ""
				continue
			}
			if !present || samePricingCell(before, after) {
				continue
			}
			merged[col] = after
			changed[col] = true
			fields = append(fields, pricingFieldChange{Field: pricingHeaders[col][0], Before: before, After: after})
		}
		if len(fields) == 0 {
			unchanged++
			continue
		}
		if fileVersion := row.cells[colVersion]; fileVersion != "" && fileVersion != version {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): changed by someone else after export; export again", row.line, name))
			continue
		}
		draft, problems := pricingDraftFromCells(merged, changed, configured)
		if len(problems) == 0 {
			if err := model.ValidateModelPricing(name, draft); err != nil {
				problems = append(problems, err.Error())
			}
		}
		if len(problems) > 0 {
			rowErrors = append(rowErrors, fmt.Sprintf("row %d (%s): %s", row.line, name, strings.Join(problems, "; ")))
			continue
		}
		changes = append(changes, model.ModelPricingChange{ModelName: name, ExpectedVersion: version, Pricing: draft})
		summary = append(summary, pricingModelChange{ModelName: name, Fields: fields})
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
