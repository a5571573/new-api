package controller

import (
	"bytes"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/xuri/excelize/v2"
)

type pricingImportResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Data    struct {
		Applied   bool                 `json:"applied"`
		Changes   []pricingModelChange `json:"changes"`
		Unchanged int                  `json:"unchanged"`
		Errors    []string             `json:"errors"`
	} `json:"data"`
}

func exportPricingWorkbook(t *testing.T) *excelize.File {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/api/option/export_model_ratios", nil)
	ExportModelPricingExcel(context)
	require.Equal(t, http.StatusOK, recorder.Code, recorder.Body.String())
	workbook, err := excelize.OpenReader(bytes.NewReader(recorder.Body.Bytes()))
	require.NoError(t, err)
	t.Cleanup(func() { _ = workbook.Close() })
	return workbook
}

func importPricingWorkbook(t *testing.T, workbook *excelize.File, dryRun bool) pricingImportResponse {
	t.Helper()
	var file bytes.Buffer
	require.NoError(t, workbook.Write(&file))
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "pricing.xlsx")
	require.NoError(t, err)
	_, err = part.Write(file.Bytes())
	require.NoError(t, err)
	require.NoError(t, writer.Close())

	path := "/api/option/import_model_ratios"
	if dryRun {
		path += "?dry_run=true"
	}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPost, path, &body)
	context.Request.Header.Set("Content-Type", writer.FormDataContentType())
	context.Set("role", common.RoleRootUser)
	ImportModelPricingExcel(context)
	var response pricingImportResponse
	require.NoError(t, common.Unmarshal(recorder.Body.Bytes(), &response), recorder.Body.String())
	return response
}

// findPricingRow returns the 1-based sheet row holding the model.
func findPricingRow(t *testing.T, workbook *excelize.File, name string) int {
	t.Helper()
	rows, err := workbook.GetRows(pricingSheetEditable)
	require.NoError(t, err)
	for i, row := range rows {
		if len(row) > 0 && row[0] == name {
			return i + 1
		}
	}
	t.Fatalf("model %s not exported", name)
	return 0
}

func configuredPricing(t *testing.T, name string) model.PricingValues {
	t.Helper()
	snapshot, err := model.GetModelPricingSnapshot([]string{name})
	require.NoError(t, err)
	require.Len(t, snapshot.Entries, 1)
	return snapshot.Entries[0].Configured
}

func TestModelPricingExcelDatabaseMatrix(t *testing.T) {
	for _, dialect := range []struct{ kind, env string }{{"sqlite", ""}, {"mysql", "TEST_MYSQL_DSN"}, {"postgres", "TEST_POSTGRES_DSN"}} {
		t.Run(dialect.kind, func(t *testing.T) {
			if dialect.env != "" && os.Getenv(dialect.env) == "" {
				t.Skip("set " + dialect.env + " to run this database")
			}
			modelManagementDB(t, dialect.kind, os.Getenv(dialect.env))
			baseline, err := model.GetModelPricingSnapshot([]string{"excel-ratio", "excel-fixed"})
			require.NoError(t, err)
			require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{
				{ModelName: "excel-ratio", ExpectedVersion: baseline.EmptyVersion, Pricing: model.PricingValues{"ModelRatio": 1.5, "CompletionRatio": float64(2)}},
				{ModelName: "excel-fixed", ExpectedVersion: baseline.EmptyVersion, Pricing: model.PricingValues{"ModelPrice": 0.04}},
			}))

			t.Run("unedited export imports without changes", func(t *testing.T) {
				response := importPricingWorkbook(t, exportPricingWorkbook(t), false)
				require.True(t, response.Success, response.Message)
				assert.Empty(t, response.Data.Changes)
				assert.False(t, response.Data.Applied)
				assert.Positive(t, response.Data.Unchanged)
			})

			t.Run("prices match the settings page and blank cells clear them", func(t *testing.T) {
				workbook := exportPricingWorkbook(t)
				ratioRow := findPricingRow(t, workbook, "excel-ratio")
				input, err := workbook.GetCellValue(pricingSheetEditable, fmt.Sprintf("E%d", ratioRow))
				require.NoError(t, err)
				completion, err := workbook.GetCellValue(pricingSheetEditable, fmt.Sprintf("F%d", ratioRow))
				require.NoError(t, err)
				assert.Equal(t, []string{"3", "6"}, []string{input, completion}, "ratio 1.5 shows as $3 input and completion ratio 2 as $6 output")

				require.NoError(t, workbook.SetCellValue(pricingSheetEditable, fmt.Sprintf("E%d", ratioRow), 6))
				require.NoError(t, workbook.SetCellValue(pricingSheetEditable, fmt.Sprintf("F%d", ratioRow), ""))
				rows, err := workbook.GetRows(pricingSheetEditable)
				require.NoError(t, err)
				require.NoError(t, workbook.SetSheetRow(pricingSheetEditable, fmt.Sprintf("A%d", len(rows)+1), &[]any{"excel-new", nil, nil, 0}))

				preview := importPricingWorkbook(t, workbook, true)
				require.True(t, preview.Success, preview.Message)
				assert.False(t, preview.Data.Applied)
				assert.Equal(t, []pricingModelChange{
					{ModelName: "excel-ratio", Fields: []pricingFieldChange{
						{Field: pricingHeaders[colInput][0], Before: "3", After: "6"},
						{Field: pricingHeaders[colCompletion][0], Before: "6", After: ""},
					}},
					{ModelName: "excel-new", Fields: []pricingFieldChange{{Field: pricingHeaders[colFixedPrice][0], Before: "", After: "0"}}},
				}, preview.Data.Changes)
				assert.Equal(t, model.PricingValues{"ModelRatio": 1.5, "CompletionRatio": float64(2)}, configuredPricing(t, "excel-ratio"), "dry run must not save")

				applied := importPricingWorkbook(t, workbook, false)
				require.True(t, applied.Success, applied.Message)
				assert.True(t, applied.Data.Applied)
				assert.Equal(t, model.PricingValues{"billing_setting.billing_mode": "ratio", "ModelRatio": float64(3)}, configuredPricing(t, "excel-ratio"))
				assert.Equal(t, model.PricingValues{"billing_setting.billing_mode": "ratio", "ModelPrice": float64(0)}, configuredPricing(t, "excel-new"), "a blank mode with a fixed price is per request")
			})

			t.Run("any invalid row rejects the whole import", func(t *testing.T) {
				workbook := exportPricingWorkbook(t)
				fixedRow := findPricingRow(t, workbook, "excel-fixed")
				ratioRow := findPricingRow(t, workbook, "excel-ratio")
				require.NoError(t, workbook.SetCellValue(pricingSheetEditable, fmt.Sprintf("D%d", fixedRow), 0.08))
				require.NoError(t, workbook.SetCellValue(pricingSheetEditable, fmt.Sprintf("B%d", ratioRow), "abc"))
				rows, err := workbook.GetRows(pricingSheetEditable)
				require.NoError(t, err)
				require.NoError(t, workbook.SetSheetRow(pricingSheetEditable, fmt.Sprintf("A%d", len(rows)+1), &[]any{"excel-lane", nil, nil, nil, nil, 5}))

				response := importPricingWorkbook(t, workbook, false)
				assert.False(t, response.Success)
				require.Len(t, response.Data.Errors, 2)
				assert.Contains(t, response.Data.Errors[0], "excel-ratio")
				assert.Contains(t, response.Data.Errors[1], "excel-lane")
				assert.Contains(t, response.Data.Errors[1], "requires", "a completion price needs an input price, as on the settings page")
				assert.Equal(t, model.PricingValues{"ModelPrice": 0.04}, configuredPricing(t, "excel-fixed"))
			})

			t.Run("rows changed after export are rejected", func(t *testing.T) {
				workbook := exportPricingWorkbook(t)
				fixedRow := findPricingRow(t, workbook, "excel-fixed")
				require.NoError(t, workbook.SetCellValue(pricingSheetEditable, fmt.Sprintf("D%d", fixedRow), 0.08))
				current, err := model.GetModelPricingSnapshot([]string{"excel-fixed"})
				require.NoError(t, err)
				require.NoError(t, model.UpdateModelPricing([]model.ModelPricingChange{{ModelName: "excel-fixed", ExpectedVersion: current.Entries[0].Version, Pricing: model.PricingValues{"ModelPrice": 0.05}}}))

				response := importPricingWorkbook(t, workbook, false)
				assert.False(t, response.Success)
				require.Len(t, response.Data.Errors, 1)
				assert.Contains(t, response.Data.Errors[0], "export again")
				assert.Equal(t, model.PricingValues{"ModelPrice": 0.05}, configuredPricing(t, "excel-fixed"))
			})
		})
	}
}
